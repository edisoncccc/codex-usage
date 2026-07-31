package meter

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
	}

	result := ScanResult{Homes: len(homes)}
	seenSessionHome := map[string]string{}
	for _, rawHome := range homes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		home, err := filepath.Abs(rawHome)
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
				abs, _ := filepath.Abs(session.RolloutPath)
				metadata[pathKey(abs)] = session
			}
			if err := s.Store.UpsertSession(ctx, session); err != nil {
				return result, err
			}
		}

		var files int64
		for _, path := range discovery.Paths {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					result.Warnings++
					_ = s.Store.AddWarning(ctx, "rollout_stat", path, err.Error())
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
				continue
			}
			files++
			result.Files++
			fileResult, err := s.scanFile(ctx, home, path, metadata[pathKey(path)])
			result.Records += fileResult.Records
			result.EventsInserted += fileResult.EventsInserted
			result.Duplicates += fileResult.Duplicates
			result.Warnings += fileResult.Warnings
			if err != nil {
				result.Warnings++
				_ = s.Store.AddWarning(ctx, "rollout_scan", path, err.Error())
				continue
			}
		}

		increased, fallbackErr := s.Store.ApplyStateFallbacks(ctx, discovery.Sessions)
		if fallbackErr != nil {
			result.Warnings++
			_ = s.Store.AddWarning(ctx, "state_fallback", discovery.StateDB, fallbackErr.Error())
		} else {
			result.Unattributed += increased
		}
		_ = s.Store.UpdateScanState(ctx, home, discovery.StateDB, files, discovery.Warning)
	}
	result.ElapsedMillis = time.Since(started).Milliseconds()
	return result, nil
}

func (s *Scanner) Busy() bool { return s.busy.Load() }

type fileScanResult struct {
	Records        int64
	EventsInserted int64
	Duplicates     int64
	Warnings       int64
}

func (s *Scanner) scanFile(ctx context.Context, home, path string, meta model.SessionInfo) (fileScanResult, error) {
	var result fileScanResult
	info, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	cursor, exists, err := s.Store.GetCursor(ctx, path)
	if err != nil {
		return result, err
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
	}
	if cursor.Offset > info.Size() || cursor.Size > info.Size() {
		_ = s.Store.AddWarning(ctx, "rollout_truncated", path,
			fmt.Sprintf("文件从 %d 缩短到 %d；重新扫描并以稳定事件 ID 去重", cursor.Size, info.Size()))
		result.Warnings++
		cursor.Offset = 0
		cursor.Cumulative = model.TokenUsage{}
		cursor.Segment = 0
	}
	if exists && cursor.Offset >= info.Size() && cursor.Size == info.Size() &&
		cursor.ModifiedNanos != 0 && cursor.ModifiedNanos != info.ModTime().UnixNano() {
		_ = s.Store.AddWarning(ctx, "rollout_rewritten", path,
			"文件大小未变但修改时间变化；从头复核并以 session 高水位去重")
		result.Warnings++
		cursor.Offset = 0
		cursor.Cumulative = model.TokenUsage{}
	}
	if cursor.SessionID != "" {
		if err := s.inheritSessionProgress(ctx, &cursor); err != nil {
			return result, err
		}
	}
	if cursor.Offset >= info.Size() && cursor.Size == info.Size() &&
		cursor.ModifiedNanos == info.ModTime().UnixNano() {
		return result, nil
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
	Input           int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	Output          int64 `json:"output_tokens"`
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
	Total           int64 `json:"total_tokens"`
}

func (v tokenVector) usage() model.TokenUsage {
	return model.TokenUsage{
		Input:           v.Input,
		CachedInput:     v.CachedInput,
		CacheWriteInput: v.CacheWriteInput,
		Output:          v.Output,
		ReasoningOutput: v.ReasoningOutput,
		Total:           v.Total,
	}.Compatible()
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
		cursor.SessionID = firstNonEmpty(payload.ID, payload.SessionID, cursor.SessionID, meta.SessionID)
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
		current := info.Total.usage()
		if current.IsZero() {
			return nil
		}
		current = carryMissingSubsets(current, cursor.Cumulative)
		if current.Equal(cursor.Cumulative) ||
			(current.Total > 0 && current.Total == cursor.Cumulative.Total) {
			result.Duplicates++
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

func DiscoverHome(ctx context.Context, home string) HomeDiscovery {
	out := HomeDiscovery{Home: home}
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
				abs, err := filepath.Abs(session.RolloutPath)
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
			if missing > 0 {
				for _, candidate := range walkRollouts(home) {
					key := pathKey(candidate)
					if seen[key] {
						continue
					}
					seen[key] = true
					out.Paths = append(out.Paths, candidate)
				}
				out.Warning = fmt.Sprintf("状态库有 %d 个 rollout_path 不可读；已用 sessions 目录补充", missing)
			}
			sort.Strings(out.Paths)
			if len(out.Paths) > 0 {
				return out
			}
			if out.Warning == "" {
				out.Warning = "状态库可读，但没有可读的 canonical rollout_path；回退目录扫描"
			}
		} else {
			out.Warning = err.Error()
		}
	}
	out.Fallback = true
	out.Paths = walkRollouts(home)
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
				out = append(out, path)
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

func carryMissingSubsets(current, previous model.TokenUsage) model.TokenUsage {
	if current.Total < previous.Total {
		return current
	}
	if current.CachedInput == 0 && previous.CachedInput > 0 {
		current.CachedInput = previous.CachedInput
	}
	if current.CacheWriteInput == 0 && previous.CacheWriteInput > 0 {
		current.CacheWriteInput = previous.CacheWriteInput
	}
	if current.ReasoningOutput == 0 && previous.ReasoningOutput > 0 {
		current.ReasoningOutput = previous.ReasoningOutput
	}
	return current
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
	clean := filepath.Clean(path)
	if os.PathSeparator == '\\' {
		return strings.ToLower(clean)
	}
	return clean
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
