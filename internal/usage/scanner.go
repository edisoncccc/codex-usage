package usage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/store"
)

const (
	defaultMaxRelevantRecord = 8 << 20
	recordProbeLimit         = 16 << 10
	// Explicit fork replay boundaries persist their positive byte offset.
	// A negative value marks the alternate single-metadata format whose first
	// token snapshot supplied an implicit inherited baseline.
	implicitForkReplayOffset = -1
)

type Scanner struct {
	Store             *store.Store
	MaxRelevantRecord int
	Now               func() time.Time
	mu                sync.Mutex
	busy              atomic.Bool
}

type ScanResult struct {
	Homes          int      `json:"homes"`
	Files          int64    `json:"files"`
	Records        int64    `json:"records"`
	EventsInserted int64    `json:"events_inserted"`
	Corrections    int64    `json:"classification_corrections"`
	Duplicates     int64    `json:"duplicates"`
	Warnings       int64    `json:"warnings"`
	Unattributed   int64    `json:"unattributed_sessions"`
	StateDatabases []string `json:"state_databases,omitempty"`
	ElapsedMillis  int64    `json:"elapsed_ms"`
}

type HomeDiscovery struct {
	Home     string
	StateDB  string
	Sessions []model.SessionInfo
	Paths    []string
	Fallback bool
	Warning  string
}

func (s *Scanner) Scan(ctx context.Context, homes []string, rebuild bool) (ScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy.Store(true)
	defer s.busy.Store(false)
	started := time.Now()
	if s.Store == nil {
		return ScanResult{}, errors.New("scanner store is nil")
	}
	if s.MaxRelevantRecord <= 0 {
		s.MaxRelevantRecord = defaultMaxRelevantRecord
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if rebuild {
		if err := s.Store.ResetHistorical(ctx); err != nil {
			return ScanResult{}, err
		}
	} else if reason, required, err := s.Store.HistoricalRebuildReason(ctx); err != nil {
		return ScanResult{}, err
	} else if required {
		rebuildErr := &RebuildRequiredError{Kind: "schema_upgrade_rebuild", Detail: reason}
		_ = s.Store.AddWarning(ctx, rebuildErr.Kind, rebuildErr.Path, rebuildErr.Error())
		return ScanResult{Homes: len(homes), Warnings: 1}, rebuildErr
	}
	result, err := s.scanPass(ctx, homes)
	var changed *RebuildRequiredError
	if errors.As(err, &changed) {
		result.Warnings++
		_ = s.Store.AddWarning(ctx, changed.Kind, changed.Path, changed.Error())
	}
	result.ElapsedMillis = time.Since(started).Milliseconds()
	return result, err
}

func (s *Scanner) scanPass(ctx context.Context, homes []string) (ScanResult, error) {
	result := ScanResult{Homes: len(homes)}
	seenSessionHome := map[string]string{}
	for _, rawHome := range homes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		home, err := canonicalPath(rawHome)
		if err != nil {
			result.Warnings++
			_ = s.Store.AddWarning(ctx, "home_path", rawHome, err.Error())
			continue
		}
		discovery := DiscoverHome(ctx, home)
		if discovery.StateDB != "" {
			result.StateDatabases = append(result.StateDatabases, discovery.StateDB)
		}
		if discovery.Warning != "" {
			result.Warnings++
			_ = s.Store.AddWarning(ctx, "state_schema", discovery.StateDB, discovery.Warning)
		}

		metadata := make(map[string]model.SessionInfo, len(discovery.Sessions))
		for _, session := range discovery.Sessions {
			if previous, ok := seenSessionHome[session.SessionID]; ok && !samePath(previous, home) {
				result.Warnings++
				_ = s.Store.AddWarning(ctx, "shared_codex_home_history", session.SessionID,
					fmt.Sprintf("同一 session 同时出现在 %s 和 %s；安装前历史无法可靠按电脑拆分，已按 session 去重", previous, home))
			} else if session.SessionID != "" {
				seenSessionHome[session.SessionID] = home
			}
			if session.RolloutPath != "" {
				abs, pathErr := canonicalPath(session.RolloutPath)
				if pathErr == nil {
					session.RolloutPath = abs
					metadata[pathKey(abs)] = session
				}
			}
			if err := s.Store.UpsertSession(ctx, session); err != nil {
				return result, err
			}
		}

		var files int64
		for _, path := range discovery.Paths {
			info, statErr := os.Stat(path)
			if statErr != nil || info.IsDir() {
				if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					result.Warnings++
					_ = s.Store.AddWarning(ctx, "rollout_stat", path, statErr.Error())
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
				continue
			}
			files++
			result.Files++
			fileResult, err := s.scanFile(ctx, home, path, metadata[pathKey(path)], info)
			result.Records += fileResult.Records
			result.EventsInserted += fileResult.EventsInserted
			result.Corrections += fileResult.Corrections
			result.Duplicates += fileResult.Duplicates
			result.Warnings += fileResult.Warnings
			if err != nil {
				var changed *RebuildRequiredError
				if errors.As(err, &changed) {
					return result, err
				}
				result.Warnings++
				_ = s.Store.AddWarning(ctx, "rollout_scan", path, err.Error())
				continue
			}
		}

		_ = s.Store.UpdateScanState(ctx, home, discovery.StateDB, files, discovery.Warning)
	}
	return result, nil
}

func (s *Scanner) Busy() bool { return s.busy.Load() }

type fileScanResult struct {
	Records        int64
	EventsInserted int64
	Corrections    int64
	Duplicates     int64
	Warnings       int64
}

// RebuildRequiredError means continuing safely requires deleting and deriving
// the historical ledger again. Detection itself never performs that deletion.
type RebuildRequiredError struct {
	Kind   string
	Path   string
	Detail string
}

func (e *RebuildRequiredError) Error() string {
	return e.Detail + "；现有统计已保留，需要用户确认后才能重建"
}

func (s *Scanner) scanFile(ctx context.Context, home, path string, meta model.SessionInfo, info os.FileInfo) (fileScanResult, error) {
	var result fileScanResult
	cursor, exists, err := s.Store.GetCursor(ctx, path)
	if err != nil {
		return result, err
	}
	if exists {
		if cursor.Offset >= info.Size() && cursor.Size == info.Size() &&
			cursor.ModifiedNanos == info.ModTime().UnixNano() {
			return result, nil
		}
		switch {
		case cursor.Offset > info.Size() || cursor.Size > info.Size():
			return result, &RebuildRequiredError{Kind: "rollout_truncated", Path: path,
				Detail: fmt.Sprintf("文件从 %d 缩短到 %d", cursor.Size, info.Size())}
		case cursor.Size == info.Size() && cursor.ModifiedNanos != 0 && cursor.ModifiedNanos != info.ModTime().UnixNano():
			return result, &RebuildRequiredError{Kind: "rollout_rewritten", Path: path,
				Detail: "文件大小未变但修改时间变化"}
		}
		probeBytes := cursor.Size
		if probeBytes > 4096 {
			probeBytes = 4096
		}
		prefix, hashErr := hashFilePrefix(path, probeBytes)
		if hashErr != nil {
			return result, hashErr
		}
		if cursor.PrefixHash != "" && prefix != cursor.PrefixHash {
			return result, &RebuildRequiredError{Kind: "rollout_rewritten", Path: path,
				Detail: "文件已在原有扫描范围内重写"}
		}
		if cursor.ForkedFromID != "" && cursor.ReplayOffset == 0 && info.Size() > cursor.Size {
			inspection, inspectErr := s.inspectRollout(ctx, path)
			if inspectErr != nil {
				return result, inspectErr
			}
			if inspection.ReplayOffset > 0 {
				if cursor.Offset > 0 {
					return result, &RebuildRequiredError{Kind: "fork_replay_detected", Path: path,
						Detail: "fork 文件补全了父线程历史重放边界"}
				}
				cursor.Offset = inspection.ReplayOffset
				cursor.ReplayOffset = inspection.persistedReplayOffset()
				cursor.Cumulative = inspection.Baseline
			} else {
				cursor.Size = info.Size()
				cursor.ModifiedNanos = info.ModTime().UnixNano()
				cursor.PrefixHash, err = hashFilePrefix(path, minInt64(info.Size(), 4096))
				if err != nil {
					return result, err
				}
				if err := s.Store.PutCursor(ctx, cursor); err != nil {
					return result, err
				}
				return result, nil
			}
		}
	}
	if !exists {
		cursor = store.FileCursor{
			Path:        path,
			CodexHome:   home,
			SessionID:   meta.SessionID,
			Model:       meta.Model,
			ProjectPath: meta.ProjectPath,
			Source:      firstNonEmpty(meta.Source, meta.ThreadSource),
			AgentType:   meta.AgentType,
		}
		inspection, inspectErr := s.inspectRollout(ctx, path)
		if inspectErr != nil {
			return result, inspectErr
		}
		if inspection.OwnerID != "" {
			cursor.SessionID = inspection.OwnerID
			cursor.ForkedFromID = inspection.ForkedFromID
			cursor.ProjectPath = firstNonEmpty(inspection.Owner.Cwd, cursor.ProjectPath)
			cursor.Source = firstNonEmpty(inspection.Owner.Originator, rawText(inspection.Owner.Source),
				inspection.Owner.ThreadSource, cursor.Source)
			cursor.AgentType = model.ClassifyAgent(cursor.Source, rawText(inspection.Owner.Source),
				inspection.Owner.ThreadSource)
		}
		if inspection.ReplayOffset > 0 {
			cursor.Offset = inspection.ReplayOffset
			cursor.ReplayOffset = inspection.persistedReplayOffset()
			cursor.Cumulative = inspection.Baseline
		} else if inspection.ForkedFromID != "" {
			// A fork is written in stages. Do not expose its copied parent
			// prefix as child usage while the parent boundary is incomplete.
			cursor.Size = info.Size()
			cursor.ModifiedNanos = info.ModTime().UnixNano()
			cursor.PrefixHash, err = hashFilePrefix(path, minInt64(info.Size(), 4096))
			if err != nil {
				return result, err
			}
			if err := s.Store.PutCursor(ctx, cursor); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	if cursor.SessionID != "" {
		if err := s.inheritSessionProgress(ctx, &cursor); err != nil {
			return result, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	if _, err := file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return result, err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	committedOffset := cursor.Offset
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		recordStart := committedOffset
		record, consumed, complete, tooLarge, err := readSelectiveRecord(reader, s.MaxRelevantRecord)
		if err != nil && !errors.Is(err, io.EOF) {
			return result, err
		}
		if consumed == 0 && errors.Is(err, io.EOF) {
			break
		}
		if !complete {
			// Leave the cursor at the start of a partial line. A later append
			// will replay only this incomplete record.
			break
		}
		committedOffset += consumed
		result.Records++
		if tooLarge {
			result.Warnings++
			_ = s.Store.AddWarning(ctx, "record_too_large", path,
				fmt.Sprintf("相关 JSONL 记录超过 %d 字节，已跳过且未载入完整内容（offset=%d）", s.MaxRelevantRecord, recordStart))
			continue
		}
		if len(record) == 0 {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		if parseErr := s.processRecord(ctx, record, recordStart, path, home, meta, &cursor, &result); parseErr != nil {
			var changed *RebuildRequiredError
			if errors.As(parseErr, &changed) {
				return result, parseErr
			}
			result.Warnings++
			_ = s.Store.AddWarning(ctx, "jsonl_record", path,
				fmt.Sprintf("offset=%d: %v", recordStart, parseErr))
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}

	cursor.Path = path
	cursor.CodexHome = home
	cursor.Offset = committedOffset
	cursor.Size = info.Size()
	cursor.ModifiedNanos = info.ModTime().UnixNano()
	cursor.PrefixHash, err = hashFilePrefix(path, minInt64(info.Size(), 4096))
	if err != nil {
		return result, err
	}
	if err := s.Store.PutCursor(ctx, cursor); err != nil {
		return result, err
	}
	if cursor.SessionID != "" {
		session := meta
		session.SessionID = cursor.SessionID
		session.RolloutPath = path
		session.CodexHome = home
		session.ProjectPath = firstNonEmpty(cursor.ProjectPath, meta.ProjectPath)
		session.Model = firstNonEmpty(cursor.Model, meta.Model)
		session.Source = firstNonEmpty(cursor.Source, meta.Source)
		session.AgentType = firstNonEmpty(cursor.AgentType, meta.AgentType, "main")
		if err := s.Store.UpsertSession(ctx, session); err != nil {
			return result, err
		}
	}
	return result, nil
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	Originator    string          `json:"originator"`
	CLIValue      string          `json:"cli_version"`
	Source        json.RawMessage `json:"source"`
	ThreadSource  string          `json:"thread_source"`
	ModelProvider string          `json:"model_provider"`
	ForkedFromID  string          `json:"forked_from_id"`
}

type turnContextPayload struct {
	TurnID string `json:"turn_id"`
	Cwd    string `json:"cwd"`
	Model  string `json:"model"`
}

type eventPayload struct {
	Type string          `json:"type"`
	Info json.RawMessage `json:"info"`
}

type tokenInfo struct {
	Total tokenVector `json:"total_token_usage"`
	Last  tokenVector `json:"last_token_usage"`
}

type tokenVector struct {
	Input           *int64 `json:"input_tokens"`
	CachedInput     *int64 `json:"cached_input_tokens"`
	CacheWriteInput *int64 `json:"cache_write_input_tokens"`
	Output          *int64 `json:"output_tokens"`
	ReasoningOutput *int64 `json:"reasoning_output_tokens"`
	Total           *int64 `json:"total_tokens"`
}

func (v tokenVector) usage() model.TokenUsage {
	return model.TokenUsage{
		Input:           int64Value(v.Input),
		CachedInput:     int64Value(v.CachedInput),
		CacheWriteInput: int64Value(v.CacheWriteInput),
		Output:          int64Value(v.Output),
		ReasoningOutput: int64Value(v.ReasoningOutput),
		Total:           int64Value(v.Total),
	}.Compatible()
}

func (v tokenVector) withMissingSubsets(previous model.TokenUsage) model.TokenUsage {
	current := v.usage()
	if v.CachedInput == nil {
		current.CachedInput = previous.CachedInput
	}
	if v.CacheWriteInput == nil {
		current.CacheWriteInput = previous.CacheWriteInput
	}
	if v.ReasoningOutput == nil {
		current.ReasoningOutput = previous.ReasoningOutput
	}
	return current
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Scanner) processRecord(
	ctx context.Context,
	record []byte,
	offset int64,
	path, home string,
	meta model.SessionInfo,
	cursor *store.FileCursor,
	result *fileScanResult,
) error {
	var env envelope
	if err := json.Unmarshal(record, &env); err != nil {
		return fmt.Errorf("损坏 JSON: %w", err)
	}
	switch env.Type {
	case "session_meta":
		var payload sessionMetaPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return err
		}
		candidateID := firstNonEmpty(payload.ID, payload.SessionID)
		if cursor.SessionID == "" {
			cursor.SessionID = firstNonEmpty(candidateID, meta.SessionID)
			cursor.ForkedFromID = payload.ForkedFromID
		} else if candidateID != "" && candidateID != cursor.SessionID {
			// Fork rollouts can embed a copied parent session_meta after the
			// child's own metadata. Ownership is immutable per physical file.
			// If an implicit single-metadata fork later grows such a boundary,
			// discard its provisional interpretation and rebuild from the now
			// explicit replay boundary.
			if cursor.ReplayOffset == implicitForkReplayOffset &&
				(cursor.ForkedFromID == "" || candidateID == cursor.ForkedFromID) {
				return &RebuildRequiredError{Kind: "fork_replay_detected", Path: path,
					Detail: "单 metadata fork 后续补全了父线程历史重放边界"}
			}
			return nil
		}
		if err := s.inheritSessionProgress(ctx, cursor); err != nil {
			return err
		}
		cursor.ProjectPath = firstNonEmpty(payload.Cwd, cursor.ProjectPath, meta.ProjectPath)
		cursor.Source = firstNonEmpty(payload.Originator, rawText(payload.Source),
			payload.ThreadSource, cursor.Source, meta.Source)
		cursor.AgentType = model.ClassifyAgent(cursor.Source, rawText(payload.Source),
			payload.ThreadSource, meta.ThreadSource)
		if existing, err := s.Store.Session(ctx, cursor.SessionID); err == nil &&
			existing.CodexHome != "" && !samePath(existing.CodexHome, home) {
			_ = s.Store.AddWarning(ctx, "shared_codex_home_history", cursor.SessionID,
				fmt.Sprintf("同一 session 同时出现在 %s 和 %s；安装前历史无法可靠按电脑拆分，已按 session 去重", existing.CodexHome, home))
			result.Warnings++
		}
		return s.Store.UpsertSession(ctx, model.SessionInfo{
			SessionID:    cursor.SessionID,
			RolloutPath:  path,
			CodexHome:    home,
			Title:        meta.Title,
			ProjectPath:  cursor.ProjectPath,
			Model:        firstNonEmpty(cursor.Model, meta.Model),
			Source:       cursor.Source,
			ThreadSource: firstNonEmpty(payload.ThreadSource, meta.ThreadSource),
			AgentType:    cursor.AgentType,
			CLIValue:     firstNonEmpty(payload.CLIValue, meta.CLIValue),
			TokensUsed:   meta.TokensUsed,
			CreatedAt:    meta.CreatedAt,
			UpdatedAt:    meta.UpdatedAt,
			Archived:     meta.Archived,
		})
	case "turn_context":
		var payload turnContextPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return err
		}
		cursor.TurnID = firstNonEmpty(payload.TurnID, cursor.TurnID)
		cursor.Model = firstNonEmpty(payload.Model, cursor.Model, meta.Model)
		cursor.ProjectPath = firstNonEmpty(payload.Cwd, cursor.ProjectPath, meta.ProjectPath)
		return nil
	case "event_msg":
		var payload eventPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return err
		}
		if payload.Type != "token_count" {
			return nil
		}
		if len(payload.Info) == 0 || bytes.Equal(bytes.TrimSpace(payload.Info), []byte("null")) {
			return nil
		}
		var info tokenInfo
		if err := json.Unmarshal(payload.Info, &info); err != nil {
			return err
		}
		current := info.Total.withMissingSubsets(cursor.Cumulative)
		if current.IsZero() {
			return nil
		}
		if current.Equal(cursor.Cumulative) {
			result.Duplicates++
			return nil
		}
		if current.Total > 0 && current.Total == cursor.Cumulative.Total {
			difference := current.Sub(cursor.Cumulative)
			corrected, err := s.Store.CorrectEventUsage(
				ctx, cursor.LastEventID, cursor.SessionID, cursor.Segment, difference,
			)
			if err != nil {
				return err
			}
			cursor.Cumulative = current
			if corrected {
				result.Corrections++
			} else {
				result.Duplicates++
			}
			if cursor.SessionID != "" {
				return s.Store.PutSessionProgress(ctx, cursor.SessionID, cursor.Segment, current)
			}
			return nil
		}
		if cursor.InheritedBaseline && !current.MonotonicFrom(cursor.Cumulative) {
			// A restored/resumed rollout can replay an older prefix. Keep the
			// session-wide high-water mark and wait until the file catches up.
			if cursor.Cumulative.MonotonicFrom(current) || current.Total <= cursor.Cumulative.Total {
				result.Duplicates++
				return nil
			}
		}
		delta := current
		confidence := model.ConfidenceExact
		if !cursor.Cumulative.IsZero() {
			if current.MonotonicFrom(cursor.Cumulative) {
				delta = current.Sub(cursor.Cumulative)
				cursor.InheritedBaseline = false
			} else {
				last := info.Last.usage()
				if last.IsZero() || !last.NonNegative() {
					return fmt.Errorf("累计 Token 回退且 last_token_usage 不可用: previous=(%s), current=(%s)",
						cursor.Cumulative, current)
				}
				cursor.Segment++
				delta = last
				confidence = model.ConfidenceGapFallback
				_ = s.Store.AddWarning(ctx, "cumulative_reset", path,
					fmt.Sprintf("offset=%d previous=(%s) current=(%s)，使用 last_token_usage 补位", offset, cursor.Cumulative, current))
				result.Warnings++
			}
		}
		if delta.IsZero() {
			cursor.Cumulative = current
			return nil
		}
		if !delta.NonNegative() {
			return fmt.Errorf("计算出负 Token 增量: %s", delta)
		}
		timestamp, parseErr := parseTimestamp(env.Timestamp)
		if parseErr != nil {
			confidence = model.ConfidenceGapFallback
			_ = s.Store.AddWarning(ctx, "timestamp", path,
				fmt.Sprintf("offset=%d: %v；事件保留为未归属时间", offset, parseErr))
			result.Warnings++
		}
		sessionID := firstNonEmpty(cursor.SessionID, meta.SessionID, "unknown-"+shortHash(path))
		event := model.UsageEvent{
			ID:          stableJSONLEventID(sessionID, cursor.Segment, current),
			Timestamp:   timestamp,
			Segment:     cursor.Segment,
			ObservedAt:  s.Now(),
			MachineID:   s.Store.Machine().ID,
			SessionID:   sessionID,
			TurnID:      cursor.TurnID,
			Model:       firstNonEmpty(cursor.Model, meta.Model),
			Source:      firstNonEmpty(cursor.Source, meta.Source, meta.ThreadSource),
			AgentType:   firstNonEmpty(cursor.AgentType, meta.AgentType, "main"),
			ProjectPath: firstNonEmpty(cursor.ProjectPath, meta.ProjectPath),
			ThreadTitle: meta.Title,
			Usage:       delta,
			Provenance:  model.ProvenanceSessionJSONL,
			Confidence:  confidence,
			CodexHome:   home,
		}
		inserted, err := s.Store.InsertEvent(ctx, event, path)
		if err != nil {
			return err
		}
		if inserted {
			result.EventsInserted++
		} else {
			result.Duplicates++
		}
		cursor.LastEventID = event.ID
		cursor.Cumulative = current
		if err := s.Store.PutSessionProgress(ctx, sessionID, cursor.Segment, current); err != nil {
			return err
		}
		return nil
	default:
		return nil
	}
}

func (s *Scanner) inheritSessionProgress(ctx context.Context, cursor *store.FileCursor) error {
	if cursor.SessionID == "" {
		return nil
	}
	usage, segment, ok, err := s.Store.GetSessionProgress(ctx, cursor.SessionID)
	if err != nil {
		return err
	}
	if !ok {
		if !cursor.Cumulative.IsZero() {
			return s.Store.PutSessionProgress(ctx, cursor.SessionID, cursor.Segment, cursor.Cumulative)
		}
		return nil
	}
	if usage.Equal(cursor.Cumulative) {
		if segment > cursor.Segment {
			cursor.Segment = segment
		}
		return nil
	}
	if usage.Total >= cursor.Cumulative.Total {
		cursor.Cumulative = usage
		cursor.Segment = segment
		cursor.InheritedBaseline = true
	}
	return nil
}

type rolloutInspection struct {
	Owner        sessionMetaPayload
	OwnerID      string
	ForkedFromID string
	ReplayOffset int64
	Baseline     model.TokenUsage
	Implicit     bool

	implicitCandidate bool
	implicitOffset    int64
	implicitBaseline  model.TokenUsage
}

// inspectRollout reads only metadata and token_count records. A fork file owns
// the first session_meta; a later parent session_meta terminates the copied
// history prefix. Some fork writers omit that later parent metadata entirely;
// for those files, the first total-last snapshot establishes an implicit
// inherited baseline and the first last_token_usage remains child usage.
func (s *Scanner) inspectRollout(ctx context.Context, path string) (rolloutInspection, error) {
	var out rolloutInspection
	file, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		record, consumed, complete, tooLarge, readErr := readSelectiveRecord(reader, s.MaxRelevantRecord)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return out, readErr
		}
		if consumed == 0 && errors.Is(readErr, io.EOF) {
			return out.finalize(), nil
		}
		if !complete {
			return out, nil
		}
		recordStart := offset
		offset += consumed
		if !tooLarge && len(record) > 0 {
			var env envelope
			if json.Unmarshal(record, &env) == nil {
				switch env.Type {
				case "session_meta":
					var payload sessionMetaPayload
					if json.Unmarshal(env.Payload, &payload) == nil {
						candidate := firstNonEmpty(payload.ID, payload.SessionID)
						if out.OwnerID == "" {
							out.Owner = payload
							out.OwnerID = candidate
							out.ForkedFromID = payload.ForkedFromID
							if out.ForkedFromID == "" {
								return out, nil
							}
						} else if candidate != "" && candidate != out.OwnerID &&
							(out.ForkedFromID == "" || candidate == out.ForkedFromID) {
							out.ReplayOffset = offset
							return out, nil
						}
					}
				case "event_msg":
					if out.OwnerID != "" && out.ForkedFromID != "" {
						var payload eventPayload
						if json.Unmarshal(env.Payload, &payload) == nil && payload.Type == "token_count" && len(payload.Info) > 0 {
							var info tokenInfo
							if json.Unmarshal(payload.Info, &info) == nil {
								current := info.Total.withMissingSubsets(out.Baseline)
								if !current.IsZero() {
									if !out.implicitCandidate {
										if baseline, ok := implicitForkBaseline(current, info.Last.usage()); ok {
											out.implicitCandidate = true
											out.implicitOffset = recordStart
											out.implicitBaseline = baseline
										}
									}
									out.Baseline = current
								}
							}
						}
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return out.finalize(), nil
		}
	}
}

func (in rolloutInspection) finalize() rolloutInspection {
	if in.ReplayOffset == 0 && in.ForkedFromID != "" && in.implicitCandidate {
		in.ReplayOffset = in.implicitOffset
		in.Baseline = in.implicitBaseline
		in.Implicit = true
	}
	return in
}

func (in rolloutInspection) persistedReplayOffset() int64 {
	if in.Implicit {
		return implicitForkReplayOffset
	}
	return in.ReplayOffset
}

func implicitForkBaseline(total, last model.TokenUsage) (model.TokenUsage, bool) {
	if last.IsZero() || !validTokenUsage(total) || !validTokenUsage(last) || !total.MonotonicFrom(last) {
		return model.TokenUsage{}, false
	}
	baseline := total.Sub(last)
	return baseline, validTokenUsage(baseline)
}

func validTokenUsage(usage model.TokenUsage) bool {
	return usage.NonNegative() &&
		usage.CachedInput+usage.CacheWriteInput <= usage.Input &&
		usage.ReasoningOutput <= usage.Output &&
		usage.Input+usage.Output == usage.Total
}

func hashFilePrefix(path string, length int64) (string, error) {
	if length <= 0 {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data := make([]byte, length)
	n, err := io.ReadFull(file, data)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	sum := sha256.Sum256(data[:n])
	return hex.EncodeToString(sum[:]), nil
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func DiscoverHome(ctx context.Context, home string) HomeDiscovery {
	out := HomeDiscovery{Home: home}
	if canonical, err := canonicalPath(home); err == nil {
		home = canonical
		out.Home = canonical
	}
	directoryPaths := walkRollouts(home)
	out.StateDB = store.FindLatestStateDB(home)
	if out.StateDB != "" {
		sessions, err := store.ReadStateThreads(ctx, out.StateDB, home)
		if err == nil {
			seen := map[string]bool{}
			missing := 0
			for index, session := range sessions {
				if session.RolloutPath == "" {
					continue
				}
				if !filepath.IsAbs(session.RolloutPath) {
					session.RolloutPath = filepath.Join(home, session.RolloutPath)
				}
				abs, err := canonicalPath(session.RolloutPath)
				if err != nil || seen[pathKey(abs)] {
					continue
				}
				sessions[index].RolloutPath = abs
				seen[pathKey(abs)] = true
				info, statErr := os.Stat(abs)
				if statErr != nil || info.IsDir() {
					missing++
					continue
				}
				out.Paths = append(out.Paths, abs)
			}
			out.Sessions = sessions
			extras := 0
			for _, candidate := range directoryPaths {
				key := pathKey(candidate)
				if seen[key] {
					continue
				}
				seen[key] = true
				extras++
				out.Paths = append(out.Paths, candidate)
			}
			switch {
			case missing > 0 && extras > 0:
				out.Warning = fmt.Sprintf("状态库有 %d 个 rollout_path 不可读且漏列 %d 个 JSONL；已用 sessions 目录补充", missing, extras)
			case missing > 0:
				out.Warning = fmt.Sprintf("状态库有 %d 个 rollout_path 不可读；已检查 sessions 目录", missing)
			case extras > 0:
				out.Warning = fmt.Sprintf("状态库漏列 %d 个 JSONL；已用 sessions 目录补充", extras)
			}
			sort.Strings(out.Paths)
			return out
		} else {
			out.Warning = err.Error()
		}
	}
	out.Fallback = true
	out.Paths = directoryPaths
	return out
}

func walkRollouts(home string) []string {
	var out []string
	for _, root := range []string{
		filepath.Join(home, "sessions"),
		filepath.Join(home, "archived_sessions"),
	} {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
				if canonical, canonicalErr := canonicalPath(path); canonicalErr == nil {
					out = append(out, canonical)
				}
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// readSelectiveRecord streams a JSONL record. It keeps only a small probe for
// irrelevant records and never allocates an entire prompt/reply/tool-output
// line. Relevant metadata/token records are capped separately.
func readSelectiveRecord(reader *bufio.Reader, maxRelevant int) (record []byte, consumed int64, complete, tooLarge bool, finalErr error) {
	var probe []byte
	var kept []byte
	relevant := false
	decided := false
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		if !decided {
			remaining := recordProbeLimit - len(probe)
			if remaining > 0 {
				if len(fragment) < remaining {
					probe = append(probe, fragment...)
				} else {
					probe = append(probe, fragment[:remaining]...)
				}
			}
			relevant = interestingProbe(probe)
			if relevant || len(probe) >= recordProbeLimit || bytes.Contains(fragment, []byte{'\n'}) || err == io.EOF {
				decided = true
				if relevant {
					if len(fragment) <= maxRelevant {
						kept = append(kept, fragment...)
					} else {
						tooLarge = true
					}
				}
			}
		} else if relevant && !tooLarge {
			if len(kept)+len(fragment) <= maxRelevant {
				kept = append(kept, fragment...)
			} else {
				tooLarge = true
				kept = nil
			}
		}
		if relevant && decided && !tooLarge && len(kept) > maxRelevant {
			tooLarge = true
			kept = nil
		}
		if err == nil {
			complete = true
			record = bytes.TrimSuffix(kept, []byte{'\n'})
			record = bytes.TrimSuffix(record, []byte{'\r'})
			return record, consumed, complete, tooLarge, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(fragment) == 0 {
				return nil, consumed, false, tooLarge, io.EOF
			}
			if !relevant {
				return nil, consumed, true, false, io.EOF
			}
			if tooLarge {
				return nil, consumed, true, true, io.EOF
			}
			// JSONL writers can finish a valid final line without '\n'. It is
			// complete only when the JSON itself is syntactically complete.
			if relevant && !tooLarge && json.Valid(bytes.TrimSpace(kept)) {
				complete = true
				return bytes.TrimSpace(kept), consumed, true, false, io.EOF
			}
			return nil, consumed, false, tooLarge, io.EOF
		}
		return nil, consumed, false, tooLarge, err
	}
}

func interestingProbe(probe []byte) bool {
	return bytes.Contains(probe, []byte(`"session_meta"`)) ||
		bytes.Contains(probe, []byte(`"turn_context"`)) ||
		bytes.Contains(probe, []byte(`"token_count"`))
}

func parseTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("缺少 timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析 timestamp %q", value)
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	lower := strings.ToLower(string(raw))
	switch {
	case strings.Contains(lower, `"guardian"`):
		return "guardian"
	case strings.Contains(lower, `"memory"`):
		return "memory"
	case strings.Contains(lower, `"subagent"`), strings.Contains(lower, `"thread_spawn"`):
		return "subagent"
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		for _, key := range []string{"type", "name", "source"} {
			if v, ok := value[key].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

func stableJSONLEventID(sessionID string, segment int64, cumulative model.TokenUsage) string {
	value := fmt.Sprintf("jsonl\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d",
		sessionID, segment, cumulative.Input, cumulative.CachedInput,
		cumulative.CacheWriteInput, cumulative.Output, cumulative.ReasoningOutput,
		cumulative.Total)
	sum := sha256.Sum256([]byte(value))
	return "jsonl:" + hex.EncodeToString(sum[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func pathKey(path string) string {
	clean := filepath.Clean(stripExtendedPath(path))
	if os.PathSeparator == '\\' {
		return strings.ToLower(clean)
	}
	return clean
}

func canonicalPath(path string) (string, error) {
	clean := filepath.Clean(stripExtendedPath(strings.TrimSpace(path)))
	if clean == "." && strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func stripExtendedPath(path string) string {
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, `\\?\unc\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(lower, `\\?\`) {
		return path[len(`\\?\`):]
	}
	return path
}

func samePath(a, b string) bool { return pathKey(a) == pathKey(b) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
