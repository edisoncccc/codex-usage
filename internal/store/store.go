package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	_ "modernc.org/sqlite"
)

const schemaVersion = 2

type Store struct {
	db       *sql.DB
	path     string
	machine  model.Machine
	mu       sync.Mutex
	revision atomic.Uint64
}

type FileCursor struct {
	Path          string
	CodexHome     string
	Size          int64
	ModifiedNanos int64
	Offset        int64
	SessionID     string
	Model         string
	TurnID        string
	ProjectPath   string
	Source        string
	AgentType     string
	Segment       int64
	Cumulative    model.TokenUsage
	// InheritedBaseline is transient and is not persisted in file_cursors. It
	// marks a session-wide cumulative value loaded for a new/restored file.
	InheritedBaseline bool
}

type OTelSeries struct {
	Key       string
	StartTime string
	Value     float64
	Count     uint64
	LastSeen  time.Time
}

type Status struct {
	Machine          model.Machine `json:"machine"`
	DatabasePath     string        `json:"database_path"`
	LastScan         *time.Time    `json:"last_scan,omitempty"`
	OTelLastReceived *time.Time    `json:"otel_last_received,omitempty"`
	OTelActive       bool          `json:"otel_active"`
	EventCount       int64         `json:"event_count"`
	SessionCount     int64         `json:"session_count"`
	WarningCount     int64         `json:"warning_count"`
	DataRevision     uint64        `json:"data_revision"`
	CoverageGaps     []CoverageGap `json:"coverage_gaps,omitempty"`
	CodexHomes       []HomeStatus  `json:"codex_homes"`
}

type CoverageGap struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end,omitempty"`
	Seconds int64     `json:"seconds"`
	Open    bool      `json:"open"`
}

type HomeStatus struct {
	Path         string     `json:"path"`
	LastScan     *time.Time `json:"last_scan,omitempty"`
	StateDB      string     `json:"state_db,omitempty"`
	FilesScanned int64      `json:"files_scanned"`
	Warning      string     `json:"warning,omitempty"`
}

type SessionRow struct {
	model.SessionInfo
	Usage      model.TokenUsage `json:"usage"`
	EventCount int64            `json:"event_count"`
	Confidence string           `json:"confidence,omitempty"`
	LastUsage  time.Time        `json:"last_usage,omitempty"`
}

type EventQuery struct {
	model.Filter
	Limit  int
	Offset int
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path, ""))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.ensureMachine(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s.revision.Store(1)
	_ = os.Chmod(path, 0o600)
	return s, nil
}

func (s *Store) Close() error           { return s.db.Close() }
func (s *Store) DBPath() string         { return s.path }
func (s *Store) Machine() model.Machine { return s.machine }

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			rollout_path TEXT NOT NULL DEFAULT '',
			codex_home TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			project_path TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			thread_source TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT 'main',
			cli_version TEXT NOT NULL DEFAULT '',
			tokens_used INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			archived INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS usage_events (
			id TEXT PRIMARY KEY,
			usage_at INTEGER NOT NULL DEFAULT 0,
			observed_at INTEGER NOT NULL,
			machine_id TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			turn_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT 'main',
			project_path TEXT NOT NULL DEFAULT '',
			thread_title TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			provenance TEXT NOT NULL,
			confidence TEXT NOT NULL,
			codex_home TEXT NOT NULL DEFAULT '',
			origin_path TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_usage_at ON usage_events(usage_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_session ON usage_events(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_provenance ON usage_events(provenance)`,
		`CREATE TABLE IF NOT EXISTS file_cursors (
			path TEXT PRIMARY KEY,
			codex_home TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			modified_nanos INTEGER NOT NULL DEFAULT 0,
			offset INTEGER NOT NULL DEFAULT 0,
			session_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			turn_id TEXT NOT NULL DEFAULT '',
			project_path TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT 'main',
			segment INTEGER NOT NULL DEFAULT 0,
			cumulative_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS session_cursors (
			session_id TEXT PRIMARY KEY,
			segment INTEGER NOT NULL DEFAULT 0,
			cumulative_json TEXT NOT NULL DEFAULT '{}',
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS warnings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at INTEGER NOT NULL,
			first_seen INTEGER NOT NULL DEFAULT 0,
			occurrences INTEGER NOT NULL DEFAULT 1,
			kind TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL,
			fingerprint TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS scan_state (
			codex_home TEXT PRIMARY KEY,
			last_scan INTEGER NOT NULL DEFAULT 0,
			state_db TEXT NOT NULL DEFAULT '',
			files_scanned INTEGER NOT NULL DEFAULT 0,
			warning TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS otel_series (
			series_key TEXT PRIMARY KEY,
			start_time TEXT NOT NULL DEFAULT '',
			last_value REAL NOT NULL DEFAULT 0,
			last_count INTEGER NOT NULL DEFAULT 0,
			last_seen INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS otel_coverage (
			run_id TEXT PRIMARY KEY,
			started_at INTEGER NOT NULL,
			last_at INTEGER NOT NULL
		)`,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	databaseVersion := 0
	for index, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
		if index == 0 {
			var rawVersion string
			versionErr := tx.QueryRowContext(ctx,
				`SELECT value FROM meta WHERE key='schema_version'`).Scan(&rawVersion)
			if versionErr == nil {
				version, parseErr := strconv.Atoi(rawVersion)
				if parseErr != nil {
					return fmt.Errorf("无效 Codex Usage schema_version %q", rawVersion)
				}
				if version > schemaVersion {
					return fmt.Errorf("数据库 schema v%d 来自更高版本；当前程序仅支持到 v%d",
						version, schemaVersion)
				}
				databaseVersion = version
			} else if !errors.Is(versionErr, sql.ErrNoRows) {
				return versionErr
			}
		}
	}
	if databaseVersion < 2 {
		if err := migrateWarningsV2(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_warnings_kind_path ON warnings(kind,path)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('schema_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateWarningsV2(ctx context.Context, tx *sql.Tx) error {
	columns := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(warnings)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !columns["first_seen"] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE warnings ADD COLUMN first_seen INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !columns["occurrences"] {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE warnings ADD COLUMN occurrences INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE warnings SET first_seen=created_at WHERE first_seen=0`); err != nil {
		return err
	}
	groups, err := tx.QueryContext(ctx, `SELECT kind,path,COUNT(*),MIN(first_seen),MAX(id)
		FROM warnings GROUP BY kind,path`)
	if err != nil {
		return err
	}
	type warningGroup struct {
		kind, path       string
		count, first, id int64
	}
	var items []warningGroup
	for groups.Next() {
		var item warningGroup
		if err := groups.Scan(&item.kind, &item.path, &item.count, &item.first, &item.id); err != nil {
			groups.Close()
			return err
		}
		items = append(items, item)
	}
	if err := groups.Close(); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx,
			`UPDATE warnings SET first_seen=?,occurrences=? WHERE id=?`, item.first, item.count, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM warnings WHERE kind=? AND path=? AND id<>?`, item.kind, item.path, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureMachine(ctx context.Context) error {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='machine'`).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &s.machine); err == nil && s.machine.ID != "" {
			return nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown-host"
	}
	s.machine = model.Machine{
		ID:       newUUID(),
		Label:    hostname + " · " + runtime.GOOS,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}
	data, _ := json.Marshal(s.machine)
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('machine',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(data))
	return err
}

func (s *Store) UpsertSession(ctx context.Context, in model.SessionInfo) error {
	if in.SessionID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(
		session_id,rollout_path,codex_home,title,project_path,model,source,thread_source,
		agent_type,cli_version,tokens_used,created_at,updated_at,archived)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET
			rollout_path=CASE WHEN excluded.rollout_path<>'' THEN excluded.rollout_path ELSE sessions.rollout_path END,
			codex_home=CASE WHEN excluded.codex_home<>'' THEN excluded.codex_home ELSE sessions.codex_home END,
			title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE sessions.title END,
			project_path=CASE WHEN excluded.project_path<>'' THEN excluded.project_path ELSE sessions.project_path END,
			model=CASE WHEN excluded.model<>'' THEN excluded.model ELSE sessions.model END,
			source=CASE WHEN excluded.source<>'' THEN excluded.source ELSE sessions.source END,
			thread_source=CASE WHEN excluded.thread_source<>'' THEN excluded.thread_source ELSE sessions.thread_source END,
			agent_type=CASE WHEN excluded.agent_type<>'' THEN excluded.agent_type ELSE sessions.agent_type END,
			cli_version=CASE WHEN excluded.cli_version<>'' THEN excluded.cli_version ELSE sessions.cli_version END,
			tokens_used=CASE WHEN excluded.tokens_used>0 THEN excluded.tokens_used ELSE sessions.tokens_used END,
			created_at=CASE WHEN excluded.created_at>0 THEN excluded.created_at ELSE sessions.created_at END,
			updated_at=CASE WHEN excluded.updated_at>sessions.updated_at THEN excluded.updated_at ELSE sessions.updated_at END,
			archived=excluded.archived`,
		in.SessionID, in.RolloutPath, in.CodexHome, in.Title, in.ProjectPath, in.Model,
		in.Source, in.ThreadSource, defaultAgent(in.AgentType), in.CLIValue, in.TokensUsed,
		unixOrZero(in.CreatedAt), unixOrZero(in.UpdatedAt), boolInt(in.Archived))
	return err
}

func (s *Store) Session(ctx context.Context, id string) (model.SessionInfo, error) {
	var out model.SessionInfo
	var created, updated int64
	var archived int
	err := s.db.QueryRowContext(ctx, `SELECT session_id,rollout_path,codex_home,title,
		project_path,model,source,thread_source,agent_type,cli_version,tokens_used,
		created_at,updated_at,archived FROM sessions WHERE session_id=?`, id).Scan(
		&out.SessionID, &out.RolloutPath, &out.CodexHome, &out.Title, &out.ProjectPath,
		&out.Model, &out.Source, &out.ThreadSource, &out.AgentType, &out.CLIValue,
		&out.TokensUsed, &created, &updated, &archived)
	out.CreatedAt = timeFromUnix(created)
	out.UpdatedAt = timeFromUnix(updated)
	out.Archived = archived != 0
	return out, err
}

func (s *Store) InsertEvent(ctx context.Context, event model.UsageEvent, originPath string) (bool, error) {
	if event.ID == "" {
		return false, errors.New("usage event id is empty")
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now()
	}
	if event.MachineID == "" {
		event.MachineID = s.machine.ID
	}
	event.Usage = event.Usage.Compatible()
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO usage_events(
		id,usage_at,observed_at,machine_id,session_id,turn_id,model,source,agent_type,
		project_path,thread_title,input_tokens,cached_input_tokens,cache_write_input_tokens,
		output_tokens,reasoning_output_tokens,total_tokens,provenance,confidence,codex_home,origin_path)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.ID, unixOrZero(event.Timestamp), unixOrZero(event.ObservedAt), event.MachineID,
		event.SessionID, event.TurnID, event.Model, event.Source, defaultAgent(event.AgentType),
		event.ProjectPath, event.ThreadTitle, event.Usage.Input, event.Usage.CachedInput,
		event.Usage.CacheWriteInput, event.Usage.Output, event.Usage.ReasoningOutput,
		event.Usage.Total, event.Provenance, event.Confidence, event.CodexHome, originPath)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		s.revision.Add(1)
	}
	return n > 0, nil
}

func (s *Store) GetCursor(ctx context.Context, path string) (FileCursor, bool, error) {
	var out FileCursor
	var cumulative string
	err := s.db.QueryRowContext(ctx, `SELECT path,codex_home,size,modified_nanos,offset,
		session_id,model,turn_id,project_path,source,agent_type,segment,cumulative_json
		FROM file_cursors WHERE path=?`, path).Scan(
		&out.Path, &out.CodexHome, &out.Size, &out.ModifiedNanos, &out.Offset,
		&out.SessionID, &out.Model, &out.TurnID, &out.ProjectPath, &out.Source,
		&out.AgentType, &out.Segment, &cumulative)
	if errors.Is(err, sql.ErrNoRows) {
		return FileCursor{Path: path}, false, nil
	}
	if err != nil {
		return FileCursor{}, false, err
	}
	_ = json.Unmarshal([]byte(cumulative), &out.Cumulative)
	return out, true, nil
}

func (s *Store) PutCursor(ctx context.Context, in FileCursor) error {
	data, _ := json.Marshal(in.Cumulative)
	_, err := s.db.ExecContext(ctx, `INSERT INTO file_cursors(
		path,codex_home,size,modified_nanos,offset,session_id,model,turn_id,project_path,
		source,agent_type,segment,cumulative_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET codex_home=excluded.codex_home,size=excluded.size,
		modified_nanos=excluded.modified_nanos,offset=excluded.offset,session_id=excluded.session_id,
		model=excluded.model,turn_id=excluded.turn_id,project_path=excluded.project_path,
		source=excluded.source,agent_type=excluded.agent_type,segment=excluded.segment,
		cumulative_json=excluded.cumulative_json`,
		in.Path, in.CodexHome, in.Size, in.ModifiedNanos, in.Offset, in.SessionID,
		in.Model, in.TurnID, in.ProjectPath, in.Source, defaultAgent(in.AgentType),
		in.Segment, string(data))
	return err
}

func (s *Store) GetSessionProgress(ctx context.Context, sessionID string) (model.TokenUsage, int64, bool, error) {
	if sessionID == "" {
		return model.TokenUsage{}, 0, false, nil
	}
	var raw string
	var segment int64
	err := s.db.QueryRowContext(ctx, `SELECT cumulative_json,segment
		FROM session_cursors WHERE session_id=?`, sessionID).Scan(&raw, &segment)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TokenUsage{}, 0, false, nil
	}
	if err != nil {
		return model.TokenUsage{}, 0, false, err
	}
	var usage model.TokenUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return model.TokenUsage{}, 0, false, err
	}
	return usage, segment, true, nil
}

func (s *Store) PutSessionProgress(ctx context.Context, sessionID string, segment int64, usage model.TokenUsage) error {
	if sessionID == "" || usage.IsZero() {
		return nil
	}
	raw, _ := json.Marshal(usage)
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_cursors(
		session_id,segment,cumulative_json,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET segment=excluded.segment,
		cumulative_json=excluded.cumulative_json,updated_at=excluded.updated_at`,
		sessionID, segment, string(raw), time.Now().Unix())
	return err
}

func (s *Store) ResetHistorical(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM usage_events WHERE provenance IN ('session_jsonl','state_fallback')`,
		`DELETE FROM file_cursors`,
		`DELETE FROM session_cursors`,
		`DELETE FROM sessions`,
		`DELETE FROM scan_state`,
		`DELETE FROM warnings`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revision.Add(1)
	return nil
}

func (s *Store) AddWarning(ctx context.Context, kind, path, detail string) error {
	fp := hashString(kind + "\x00" + path + "\x00" + detail)
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO warnings(
		created_at,first_seen,occurrences,kind,path,detail,fingerprint) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(kind,path) DO UPDATE SET created_at=excluded.created_at,
		detail=excluded.detail,fingerprint=excluded.fingerprint,
		occurrences=warnings.occurrences+1`,
		now, now, 1, kind, path, detail, fp)
	return err
}

func (s *Store) Warnings(ctx context.Context, limit int) ([]model.Warning, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,created_at,first_seen,occurrences,kind,path,detail
		FROM warnings ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Warning
	for rows.Next() {
		var item model.Warning
		var created, first int64
		if err := rows.Scan(&item.ID, &created, &first, &item.Occurrences,
			&item.Kind, &item.Path, &item.Detail); err != nil {
			return nil, err
		}
		item.CreatedAt = timeFromUnix(created)
		item.FirstSeen = timeFromUnix(first)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateScanState(ctx context.Context, home, stateDB string, files int64, warning string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scan_state(
		codex_home,last_scan,state_db,files_scanned,warning) VALUES(?,?,?,?,?)
		ON CONFLICT(codex_home) DO UPDATE SET last_scan=excluded.last_scan,
		state_db=excluded.state_db,files_scanned=excluded.files_scanned,warning=excluded.warning`,
		home, time.Now().Unix(), stateDB, files, warning)
	return err
}

func (s *Store) ApplyStateFallback(ctx context.Context, session model.SessionInfo) error {
	_, err := s.ApplyStateFallbacks(ctx, []model.SessionInfo{session})
	return err
}

type stateFallbackRow struct {
	total int64
	home  string
}

// ApplyStateFallbacks reconciles all state totals in one transaction. A full
// home can contain hundreds of sessions, so this avoids several SQL round
// trips per session on every incremental scan.
func (s *Store) ApplyStateFallbacks(ctx context.Context, sessions []model.SessionInfo) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	jsonTotals := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT session_id,COALESCE(SUM(total_tokens),0)
		FROM usage_events WHERE provenance='session_jsonl' GROUP BY session_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var sessionID string
		var total int64
		if err := rows.Scan(&sessionID, &total); err != nil {
			rows.Close()
			return 0, err
		}
		jsonTotals[sessionID] = total
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	existing := map[string]stateFallbackRow{}
	rows, err = tx.QueryContext(ctx, `SELECT session_id,total_tokens,codex_home
		FROM usage_events WHERE provenance='state_fallback'`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var sessionID string
		var item stateFallbackRow
		if err := rows.Scan(&sessionID, &item.total, &item.home); err != nil {
			rows.Close()
			return 0, err
		}
		existing[sessionID] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	var firstCoverage int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(started_at),0) FROM otel_coverage`).Scan(&firstCoverage); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	var increased int64
	changed := false
	for _, session := range sessions {
		if session.SessionID == "" || session.TokensUsed <= 0 {
			continue
		}
		item, found := existing[session.SessionID]
		difference := session.TokensUsed - jsonTotals[session.SessionID]
		sessionMayOverlapOTel := firstCoverage > 0 &&
			(session.UpdatedAt.IsZero() || session.UpdatedAt.Unix() >= firstCoverage-90)
		if sessionMayOverlapOTel && difference > 0 {
			originalDifference := difference
			if found {
				if difference > item.total {
					difference = item.total
				}
			} else {
				difference = 0
			}
			if difference < originalDifference {
				kind := "state_fallback_suppressed_otel"
				detail := fmt.Sprintf("session %s 的状态库差额从 %d 增至 %d，但该 session 可能与 OTel 覆盖重叠；未将新增差额重复计入",
					session.SessionID, item.total, originalDifference)
				fp := hashString(kind + "\x00" + session.RolloutPath + "\x00" + detail)
				if _, err := tx.ExecContext(ctx, `INSERT INTO warnings(
					created_at,first_seen,occurrences,kind,path,detail,fingerprint) VALUES(?,?,?,?,?,?,?)
					ON CONFLICT(kind,path) DO UPDATE SET created_at=excluded.created_at,
					detail=excluded.detail,fingerprint=excluded.fingerprint,
					occurrences=warnings.occurrences+1`,
					now, now, 1, kind, session.RolloutPath, detail, fp); err != nil {
					return 0, err
				}
			}
		}
		id := "state:" + hashString(session.SessionID)
		if difference <= 0 {
			if found && item.home != "" && !sameDatabasePath(item.home, session.CodexHome) {
				continue
			}
			if found {
				result, err := tx.ExecContext(ctx, `DELETE FROM usage_events WHERE id=?`, id)
				if err != nil {
					return 0, err
				}
				rows, _ := result.RowsAffected()
				changed = changed || rows > 0
				delete(existing, session.SessionID)
			}
			continue
		}
		if found && !sameDatabasePath(item.home, session.CodexHome) && item.total > difference {
			continue
		}
		if !found || difference > item.total {
			increased++
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO usage_events(
			id,usage_at,observed_at,machine_id,session_id,turn_id,model,source,agent_type,
			project_path,thread_title,input_tokens,cached_input_tokens,cache_write_input_tokens,
			output_tokens,reasoning_output_tokens,total_tokens,provenance,confidence,codex_home,origin_path)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET observed_at=excluded.observed_at,
			total_tokens=excluded.total_tokens,model=excluded.model,source=excluded.source,
			agent_type=excluded.agent_type,project_path=excluded.project_path,
			thread_title=excluded.thread_title,codex_home=excluded.codex_home
			WHERE usage_events.total_tokens<>excluded.total_tokens OR usage_events.model<>excluded.model OR
			usage_events.source<>excluded.source OR usage_events.agent_type<>excluded.agent_type OR
			usage_events.project_path<>excluded.project_path OR usage_events.thread_title<>excluded.thread_title OR
			usage_events.codex_home<>excluded.codex_home`,
			id, 0, now, s.machine.ID, session.SessionID, "",
			session.Model, session.Source, defaultAgent(session.AgentType), session.ProjectPath,
			session.Title, 0, 0, 0, 0, 0, difference, model.ProvenanceState,
			model.ConfidenceAggregateOnly, session.CodexHome, "")
		if err != nil {
			return 0, err
		}
		rowsAffected, _ := result.RowsAffected()
		changed = changed || rowsAffected > 0
		existing[session.SessionID] = stateFallbackRow{total: difference, home: session.CodexHome}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if changed {
		s.revision.Add(1)
	}
	return increased, nil
}

func (s *Store) Summary(ctx context.Context, filter model.Filter) (model.Summary, error) {
	where, args := canonicalWhere(filter, "e")
	if requiresAttribution(filter) {
		where, args = attributionWhere(filter, "e", false)
	}
	query := `SELECT COALESCE(SUM(e.input_tokens),0),COALESCE(SUM(e.cached_input_tokens),0),
		COALESCE(SUM(e.cache_write_input_tokens),0),COALESCE(SUM(e.output_tokens),0),
		COALESCE(SUM(e.reasoning_output_tokens),0),COALESCE(SUM(e.total_tokens),0),
		COUNT(*),COUNT(DISTINCT NULLIF(e.session_id,'')),COALESCE(MIN(NULLIF(e.usage_at,0)),0),
		COALESCE(MAX(e.usage_at),0) FROM usage_events e WHERE ` + where
	var out model.Summary
	var first, last int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&out.Usage.Input, &out.Usage.CachedInput, &out.Usage.CacheWriteInput,
		&out.Usage.Output, &out.Usage.ReasoningOutput, &out.Usage.Total,
		&out.EventCount, &out.SessionCount, &first, &last)
	if err != nil {
		return out, err
	}
	out.FirstEvent = timeFromUnix(first)
	out.LastEvent = timeFromUnix(last)
	fallbackClause, fallbackArgs := fallbackWhere(filter, "e")
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(cached_input_tokens),0),COALESCE(SUM(cache_write_input_tokens),0),
		COALESCE(SUM(output_tokens),0),COALESCE(SUM(reasoning_output_tokens),0),
		COALESCE(SUM(total_tokens),0) FROM usage_events e WHERE `+fallbackClause, fallbackArgs...).Scan(
		&out.Unattributed.Input, &out.Unattributed.CachedInput, &out.Unattributed.CacheWriteInput,
		&out.Unattributed.Output, &out.Unattributed.ReasoningOutput, &out.Unattributed.Total); err != nil {
		return out, err
	}
	out.GrandTotal = out.Usage.Total
	if filter.Since.IsZero() && filter.Until.IsZero() {
		out.GrandTotal += out.Unattributed.Total
	}
	var warnings int64
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM warnings
		WHERE kind<>'state_fallback_suppressed_otel'`).Scan(&warnings)
	out.CoverageIncomplete = warnings > 0 || out.Unattributed.Total > 0
	return out, nil
}

func (s *Store) Timeseries(ctx context.Context, filter model.Filter, bucket string) ([]model.Point, error) {
	where, args := canonicalWhere(filter, "e")
	if requiresAttribution(filter) {
		where, args = attributionWhere(filter, "e", false)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.usage_at,e.input_tokens,
		e.cached_input_tokens,e.cache_write_input_tokens,e.output_tokens,
		e.reasoning_output_tokens,e.total_tokens
		FROM usage_events e WHERE `+where+` AND e.usage_at>0 ORDER BY e.usage_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := map[int64]model.TokenUsage{}
	for rows.Next() {
		var ts int64
		var usage model.TokenUsage
		if err := rows.Scan(&ts, &usage.Input, &usage.CachedInput,
			&usage.CacheWriteInput, &usage.Output, &usage.ReasoningOutput,
			&usage.Total); err != nil {
			return nil, err
		}
		local := time.Unix(ts, 0).In(time.Local)
		var start time.Time
		if bucket == "hour" {
			start = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, time.Local)
		} else {
			start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
		}
		key := start.Unix()
		points[key] = points[key].Add(usage)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]int64, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]model.Point, 0, len(keys))
	for _, key := range keys {
		out = append(out, model.Point{Time: time.Unix(key, 0).UTC(), Usage: points[key]})
	}
	return out, nil
}

func (s *Store) Breakdown(ctx context.Context, filter model.Filter, dimension string, limit int) ([]model.BreakdownItem, error) {
	columns := map[string]string{
		"model":      "e.model",
		"source":     "e.source",
		"agent_type": "e.agent_type",
		"project":    "e.project_path",
		"thread":     "e.thread_title",
		"confidence": "e.confidence",
		"provenance": "e.provenance",
	}
	column, ok := columns[dimension]
	if !ok {
		return nil, fmt.Errorf("不支持的分解维度 %q", dimension)
	}
	if limit <= 0 || limit > 500 {
		limit = 25
	}
	where, args := canonicalWithFallbackWhere(filter, "e")
	if requiresAttribution(filter) || dimension == "project" || dimension == "thread" {
		where, args = attributionWhere(filter, "e", true)
	}
	args = append(args, limit)
	query := fmt.Sprintf(`SELECT COALESCE(NULLIF(%s,''),'未知') AS item,
		COALESCE(SUM(e.input_tokens),0),COALESCE(SUM(e.cached_input_tokens),0),
		COALESCE(SUM(e.cache_write_input_tokens),0),COALESCE(SUM(e.output_tokens),0),
		COALESCE(SUM(e.reasoning_output_tokens),0),COALESCE(SUM(e.total_tokens),0),
		COUNT(*),COUNT(DISTINCT NULLIF(e.session_id,''))
		FROM usage_events e WHERE %s GROUP BY item ORDER BY SUM(e.total_tokens) DESC LIMIT ?`, column, where)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BreakdownItem
	for rows.Next() {
		var item model.BreakdownItem
		if err := rows.Scan(&item.Key, &item.Usage.Input, &item.Usage.CachedInput,
			&item.Usage.CacheWriteInput, &item.Usage.Output, &item.Usage.ReasoningOutput,
			&item.Usage.Total, &item.Events, &item.Sessions); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Sessions(ctx context.Context, filter model.Filter, limit, offset int) ([]SessionRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := attributionWhere(filter, "e", true)
	args = append(args, limit, offset)
	query := `SELECT COALESCE(NULLIF(e.session_id,''),'otel-unattributed') sid,
		COALESCE(MAX(s.rollout_path),''),COALESCE(MAX(s.codex_home),MAX(e.codex_home),''),
		COALESCE(MAX(NULLIF(s.title,'')),MAX(e.thread_title),''),
		COALESCE(MAX(NULLIF(s.project_path,'')),MAX(e.project_path),''),
		COALESCE(MAX(NULLIF(s.model,'')),MAX(e.model),''),
		COALESCE(MAX(NULLIF(s.source,'')),MAX(e.source),''),
		COALESCE(MAX(s.thread_source),''),COALESCE(MAX(NULLIF(s.agent_type,'')),MAX(e.agent_type),'main'),
		COALESCE(MAX(s.cli_version),''),COALESCE(MAX(s.tokens_used),0),
		COALESCE(MAX(s.created_at),0),COALESCE(MAX(s.updated_at),0),COALESCE(MAX(s.archived),0),
		SUM(e.input_tokens),SUM(e.cached_input_tokens),SUM(e.cache_write_input_tokens),
		SUM(e.output_tokens),SUM(e.reasoning_output_tokens),SUM(e.total_tokens),
		COUNT(*),CASE
			WHEN MAX(e.confidence='aggregate_only')=1 THEN 'aggregate_only'
			WHEN MAX(e.confidence='gap_fallback')=1 THEN 'gap_fallback'
			ELSE COALESCE(MAX(e.confidence),'')
		END,COALESCE(MAX(e.usage_at),0)
		FROM usage_events e LEFT JOIN sessions s ON s.session_id=e.session_id
		WHERE ` + where + ` GROUP BY sid ORDER BY MAX(e.usage_at) DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var item SessionRow
		var created, updated, last int64
		var archived int
		if err := rows.Scan(&item.SessionID, &item.RolloutPath, &item.CodexHome,
			&item.Title, &item.ProjectPath, &item.Model, &item.Source,
			&item.ThreadSource, &item.AgentType, &item.CLIValue, &item.TokensUsed,
			&created, &updated, &archived, &item.Usage.Input, &item.Usage.CachedInput,
			&item.Usage.CacheWriteInput, &item.Usage.Output, &item.Usage.ReasoningOutput,
			&item.Usage.Total, &item.EventCount, &item.Confidence, &last); err != nil {
			return nil, err
		}
		item.CreatedAt = timeFromUnix(created)
		item.UpdatedAt = timeFromUnix(updated)
		item.LastUsage = timeFromUnix(last)
		item.Archived = archived != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Events(ctx context.Context, query EventQuery) ([]model.UsageEvent, error) {
	return s.queryEvents(ctx, query, false)
}

// PricingEvents applies the same canonical source and attribution rules as the
// aggregate APIs, while still exposing normalized events to the estimator.
func (s *Store) PricingEvents(ctx context.Context, query EventQuery) ([]model.UsageEvent, error) {
	return s.queryEvents(ctx, query, true)
}

// WalkPricingEvents streams the canonical pricing view through one SQL query.
// Cost estimation does not require event ordering, so this avoids repeatedly
// sorting and rescanning the same canonical view for OFFSET pages.
func (s *Store) WalkPricingEvents(ctx context.Context, filter model.Filter, fn func(model.UsageEvent) error) error {
	where, args := canonicalWithFallbackWhere(filter, "e")
	if requiresAttribution(filter) {
		where, args = attributionWhere(filter, "e", true)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,usage_at,observed_at,machine_id,
		session_id,turn_id,model,source,agent_type,project_path,thread_title,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens,provenance,confidence,codex_home
		FROM usage_events e WHERE `+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item model.UsageEvent
		var usageAt, observedAt int64
		if err := rows.Scan(&item.ID, &usageAt, &observedAt, &item.MachineID,
			&item.SessionID, &item.TurnID, &item.Model, &item.Source, &item.AgentType,
			&item.ProjectPath, &item.ThreadTitle, &item.Usage.Input,
			&item.Usage.CachedInput, &item.Usage.CacheWriteInput, &item.Usage.Output,
			&item.Usage.ReasoningOutput, &item.Usage.Total, &item.Provenance,
			&item.Confidence, &item.CodexHome); err != nil {
			return err
		}
		item.Timestamp = timeFromUnix(usageAt)
		item.ObservedAt = timeFromUnix(observedAt)
		if err := fn(item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) queryEvents(ctx context.Context, query EventQuery, pricingView bool) ([]model.UsageEvent, error) {
	if query.Limit <= 0 || query.Limit > 10000 {
		query.Limit = 1000
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	where, args := canonicalWithFallbackWhere(query.Filter, "e")
	if pricingView && requiresAttribution(query.Filter) {
		where, args = attributionWhere(query.Filter, "e", true)
	}
	args = append(args, query.Limit, query.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,usage_at,observed_at,machine_id,
		session_id,turn_id,model,source,agent_type,project_path,thread_title,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens,provenance,confidence,codex_home
		FROM usage_events e WHERE `+where+` ORDER BY usage_at DESC,observed_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageEvent
	for rows.Next() {
		var item model.UsageEvent
		var usageAt, observedAt int64
		if err := rows.Scan(&item.ID, &usageAt, &observedAt, &item.MachineID,
			&item.SessionID, &item.TurnID, &item.Model, &item.Source, &item.AgentType,
			&item.ProjectPath, &item.ThreadTitle, &item.Usage.Input,
			&item.Usage.CachedInput, &item.Usage.CacheWriteInput, &item.Usage.Output,
			&item.Usage.ReasoningOutput, &item.Usage.Total, &item.Provenance,
			&item.Confidence, &item.CodexHome); err != nil {
			return nil, err
		}
		item.Timestamp = timeFromUnix(usageAt)
		item.ObservedAt = timeFromUnix(observedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{Machine: s.machine, DatabasePath: s.path, DataRevision: s.revision.Load()}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&out.EventCount); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&out.SessionCount); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM warnings
		WHERE kind<>'state_fallback_suppressed_otel'`).Scan(&out.WarningCount); err != nil {
		return out, err
	}
	var scan, otel int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_scan),0) FROM scan_state`).Scan(&scan)
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_at),0) FROM otel_coverage`).Scan(&otel)
	if scan > 0 {
		value := timeFromUnix(scan)
		out.LastScan = &value
	}
	if otel > 0 {
		value := timeFromUnix(otel)
		out.OTelLastReceived = &value
		out.OTelActive = time.Since(value) < 2*time.Minute
	}
	coverageRows, coverageErr := s.db.QueryContext(ctx, `SELECT started_at,last_at
		FROM otel_coverage ORDER BY started_at`)
	if coverageErr == nil {
		var previousEnd int64
		for coverageRows.Next() {
			var start, end int64
			if scanErr := coverageRows.Scan(&start, &end); scanErr != nil {
				coverageRows.Close()
				return out, scanErr
			}
			if previousEnd > 0 && start > previousEnd+180 {
				out.CoverageGaps = append(out.CoverageGaps, CoverageGap{
					Start: timeFromUnix(previousEnd), End: timeFromUnix(start),
					Seconds: start - previousEnd,
				})
			}
			if end > previousEnd {
				previousEnd = end
			}
		}
		coverageRows.Close()
		if previousEnd > 0 && time.Now().Unix() > previousEnd+120 {
			out.CoverageGaps = append(out.CoverageGaps, CoverageGap{
				Start: timeFromUnix(previousEnd), Seconds: time.Now().Unix() - previousEnd, Open: true,
			})
		}
		if len(out.CoverageGaps) > 20 {
			out.CoverageGaps = out.CoverageGaps[len(out.CoverageGaps)-20:]
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT codex_home,last_scan,state_db,files_scanned,warning
		FROM scan_state ORDER BY codex_home`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item HomeStatus
		var last int64
		if err := rows.Scan(&item.Path, &last, &item.StateDB, &item.FilesScanned, &item.Warning); err != nil {
			return out, err
		}
		if last > 0 {
			value := timeFromUnix(last)
			item.LastScan = &value
		}
		out.CodexHomes = append(out.CodexHomes, item)
	}
	return out, rows.Err()
}

func (s *Store) TouchCoverage(ctx context.Context, runID string, at time.Time) error {
	return s.TouchCoverageInterval(ctx, runID, at, at)
}

func (s *Store) TouchCoverageInterval(ctx context.Context, runID string, start, end time.Time) error {
	if runID == "" {
		return errors.New("empty OTel run id")
	}
	if start.IsZero() {
		start = end
	}
	if end.IsZero() {
		end = time.Now()
	}
	if start.After(end) {
		start = end
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO otel_coverage(run_id,started_at,last_at)
		VALUES(?,?,?) ON CONFLICT(run_id) DO UPDATE SET
		started_at=MIN(started_at,excluded.started_at),last_at=MAX(last_at,excluded.last_at)`,
		runID, start.Unix(), end.Unix())
	return err
}

// OTelDelta atomically updates a series and returns the value to count for this
// export. Cumulative series reset safely at process/start-time boundaries.
func (s *Store) OTelDelta(ctx context.Context, incoming OTelSeries, cumulative bool) (float64, bool, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, time.Time{}, false, err
	}
	defer tx.Rollback()
	var previous OTelSeries
	var previousSeen int64
	err = tx.QueryRowContext(ctx, `SELECT series_key,start_time,last_value,last_count,last_seen
		FROM otel_series WHERE series_key=?`, incoming.Key).Scan(
		&previous.Key, &previous.StartTime, &previous.Value, &previous.Count, &previousSeen)
	previous.LastSeen = timeFromUnix(previousSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, time.Time{}, false, err
	}
	delta := incoming.Value
	changed := incoming.Value != 0
	coveredSince := incoming.LastSeen
	newSegment := errors.Is(err, sql.ErrNoRows) || (err == nil && previous.StartTime != incoming.StartTime)
	if cumulative && err == nil && previous.StartTime == incoming.StartTime {
		coveredSince = previous.LastSeen
		switch {
		case incoming.Value > previous.Value:
			delta = incoming.Value - previous.Value
			changed = true
		case incoming.Value == previous.Value:
			delta = 0
			changed = false
		case incoming.Value < previous.Value:
			// Same start time + lower cumulative value is a delayed/out-of-order
			// export. A legitimate producer restart changes start_time.
			_, updateErr := tx.ExecContext(ctx, `UPDATE otel_series SET last_seen=?
				WHERE series_key=?`, incoming.LastSeen.Unix(), incoming.Key)
			if updateErr != nil {
				return 0, false, time.Time{}, false, updateErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return 0, false, time.Time{}, false, commitErr
			}
			return 0, false, coveredSince, false, nil
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO otel_series(
		series_key,start_time,last_value,last_count,last_seen) VALUES(?,?,?,?,?)
		ON CONFLICT(series_key) DO UPDATE SET start_time=excluded.start_time,
		last_value=excluded.last_value,last_count=excluded.last_count,last_seen=excluded.last_seen`,
		incoming.Key, incoming.StartTime, incoming.Value, incoming.Count, incoming.LastSeen.Unix())
	if err != nil {
		return 0, false, time.Time{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, time.Time{}, false, err
	}
	return delta, changed, coveredSince, newSegment, nil
}

func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA optimize`)
	return err
}

func canonicalWhere(filter model.Filter, alias string) (string, []any) {
	if alias == "" {
		alias = "e"
	}
	parts := []string{
		"(" + alias + ".provenance='otel' OR (" + alias + ".provenance='session_jsonl' AND NOT EXISTS (" +
			"SELECT 1 FROM otel_coverage c WHERE " + alias + ".usage_at>0 AND " +
			alias + ".usage_at BETWEEN c.started_at-90 AND c.last_at+90)))",
	}
	var args []any
	if !filter.Since.IsZero() {
		parts = append(parts, alias+".usage_at>=?")
		args = append(args, filter.Since.Unix())
	}
	if !filter.Until.IsZero() {
		parts = append(parts, alias+".usage_at<?")
		args = append(args, filter.Until.Unix())
	}
	for column, value := range map[string]string{
		"model": filter.Model, "source": filter.Source, "agent_type": filter.AgentType,
		"project_path": filter.Project, "session_id": filter.SessionID,
		"confidence": filter.Confidence,
	} {
		if value == "" {
			continue
		}
		parts = append(parts, alias+"."+column+"=?")
		args = append(args, value)
	}
	return strings.Join(parts, " AND "), args
}

func fallbackWhere(filter model.Filter, alias string) (string, []any) {
	if alias == "" {
		alias = "e"
	}
	parts := []string{alias + ".provenance='state_fallback'"}
	var args []any
	for column, value := range map[string]string{
		"model": filter.Model, "source": filter.Source, "agent_type": filter.AgentType,
		"project_path": filter.Project, "session_id": filter.SessionID,
		"confidence": filter.Confidence,
	} {
		if value == "" {
			continue
		}
		parts = append(parts, alias+"."+column+"=?")
		args = append(args, value)
	}
	return strings.Join(parts, " AND "), args
}

func canonicalWithFallbackWhere(filter model.Filter, alias string) (string, []any) {
	canonical, canonicalArgs := canonicalWhere(filter, alias)
	if !filter.Since.IsZero() || !filter.Until.IsZero() {
		return canonical, canonicalArgs
	}
	fallback, fallbackArgs := fallbackWhere(filter, alias)
	args := append(canonicalArgs, fallbackArgs...)
	return "((" + canonical + ") OR (" + fallback + "))", args
}

func requiresAttribution(filter model.Filter) bool {
	return filter.Project != "" || filter.SessionID != ""
}

// attributionWhere deliberately uses session JSONL for project/thread/session
// drill-down even inside an OTel coverage window. OTel remains authoritative
// for machine totals; JSONL supplies local attribution when the official
// metric lacks a session id or cwd. OTel rows are used only for sessions that
// have no JSONL counterpart.
func attributionWhere(filter model.Filter, alias string, includeFallback bool) (string, []any) {
	if alias == "" {
		alias = "e"
	}
	base := "(" + alias + ".provenance='session_jsonl' OR (" +
		alias + ".provenance='otel' AND " + alias + ".session_id<>'' AND NOT EXISTS (" +
		"SELECT 1 FROM usage_events j WHERE j.provenance='session_jsonl' AND j.session_id=" +
		alias + ".session_id)))"
	if includeFallback && filter.Since.IsZero() && filter.Until.IsZero() {
		base = "(" + base + " OR " + alias + ".provenance='state_fallback')"
	}
	parts := []string{base}
	var args []any
	if !filter.Since.IsZero() {
		parts = append(parts, alias+".usage_at>=?")
		args = append(args, filter.Since.Unix())
	}
	if !filter.Until.IsZero() {
		parts = append(parts, alias+".usage_at<?")
		args = append(args, filter.Until.Unix())
	}
	for column, value := range map[string]string{
		"model": filter.Model, "source": filter.Source, "agent_type": filter.AgentType,
		"project_path": filter.Project, "session_id": filter.SessionID,
		"confidence": filter.Confidence,
	} {
		if value == "" {
			continue
		}
		parts = append(parts, alias+"."+column+"=?")
		args = append(args, value)
	}
	return strings.Join(parts, " AND "), args
}

// ReadStateThreads reads only metadata from a Codex state database. It probes
// columns first, so older/newer internal schemas fall back without a crash.
func ReadStateThreads(ctx context.Context, path, codexHome string) ([]model.SessionInfo, error) {
	dsn := sqliteURI(path, "mode=ro")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.ExecContext(ctx, `PRAGMA busy_timeout=3000`)
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(threads)`)
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		columns[name] = true
	}
	rows.Close()
	if !columns["id"] || !columns["rollout_path"] {
		return nil, fmt.Errorf("threads schema 缺少 id/rollout_path")
	}
	textExpr := func(name string) string {
		if columns[name] {
			return "COALESCE(CAST(" + name + " AS TEXT),'')"
		}
		return "''"
	}
	intExpr := func(name string) string {
		if columns[name] {
			return "COALESCE(CAST(" + name + " AS INTEGER),0)"
		}
		return "0"
	}
	fields := []string{
		textExpr("id"), textExpr("rollout_path"), textExpr("cwd"), textExpr("title"),
		textExpr("model"), textExpr("source"), textExpr("thread_source"),
		textExpr("agent_role"), textExpr("cli_version"), intExpr("tokens_used"), intExpr("created_at"),
		intExpr("updated_at"), intExpr("archived"),
	}
	query := "SELECT " + strings.Join(fields, ",") + " FROM threads"
	threadRows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer threadRows.Close()
	var out []model.SessionInfo
	for threadRows.Next() {
		var item model.SessionInfo
		var agentRole string
		var created, updated, archived int64
		if err := threadRows.Scan(&item.SessionID, &item.RolloutPath, &item.ProjectPath,
			&item.Title, &item.Model, &item.Source, &item.ThreadSource, &agentRole, &item.CLIValue,
			&item.TokensUsed, &created, &updated, &archived); err != nil {
			return nil, err
		}
		item.CodexHome = codexHome
		item.Source = compactStateSource(item.Source)
		item.AgentType = model.ClassifyAgent(item.Source, item.ThreadSource, agentRole)
		item.CreatedAt = flexibleEpoch(created)
		item.UpdatedAt = flexibleEpoch(updated)
		item.Archived = archived != 0
		out = append(out, item)
	}
	return out, threadRows.Err()
}

func FindLatestStateDB(home string) string {
	matches, _ := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	sort.Slice(matches, func(i, j int) bool {
		a, ea := os.Stat(matches[i])
		b, eb := os.Stat(matches[j])
		if ea != nil || eb != nil {
			return matches[i] > matches[j]
		}
		return a.ModTime().After(b.ModTime())
	})
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func sqliteURI(path, rawQuery string) string {
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	normalized := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && strings.HasPrefix(normalized, "//") {
		parts := strings.SplitN(strings.TrimPrefix(normalized, "//"), "/", 2)
		value := &url.URL{Scheme: "file", Host: parts[0], RawQuery: rawQuery}
		if len(parts) == 2 {
			value.Path = "/" + parts[1]
		}
		return value.String()
	}
	if runtime.GOOS == "windows" && len(normalized) >= 2 && normalized[1] == ':' {
		normalized = "/" + normalized
	}
	value := &url.URL{Scheme: "file", Path: normalized, RawQuery: rawQuery}
	return value.String()
}

func timeFromUnix(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func flexibleEpoch(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	switch {
	case value > 1e18:
		return time.Unix(0, value).UTC()
	case value > 1e15:
		return time.Unix(0, value*1e3).UTC()
	case value > 1e12:
		return time.UnixMilli(value).UTC()
	default:
		return time.Unix(value, 0).UTC()
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func defaultAgent(value string) string {
	if value == "" {
		return "main"
	}
	return value
}

func compactStateSource(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, `"guardian"`):
		return "guardian"
	case strings.Contains(lower, `"memory"`):
		return "memory"
	case strings.Contains(lower, `"subagent"`), strings.Contains(lower, `"thread_spawn"`):
		return "subagent"
	default:
		return "structured"
	}
}

func sameDatabasePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
