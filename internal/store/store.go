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
	"sync/atomic"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	_ "modernc.org/sqlite"
)

const (
	schemaVersion               = 7
	historicalRebuildReasonKey  = "historical_rebuild_required"
	pricingAggregatableEventSQL = `e.input_tokens>=0 AND e.cached_input_tokens>=0 AND e.cache_write_input_tokens>=0
		AND e.output_tokens>=0 AND e.reasoning_output_tokens>=0 AND e.total_tokens>=0
		AND e.cached_input_tokens+e.cache_write_input_tokens<=e.input_tokens
		AND e.reasoning_output_tokens<=e.output_tokens
		AND (e.total_tokens=0 OR e.input_tokens+e.output_tokens=0
			OR e.total_tokens=e.input_tokens+e.output_tokens)`
	pricingClassSQL = `CASE
		WHEN e.input_tokens=0 AND e.output_tokens=0 AND e.total_tokens>0 THEN 1
		WHEN e.total_tokens=0 AND e.input_tokens+e.output_tokens>0 THEN 2
		ELSE 0 END`
)

type Store struct {
	// db is the single writer used by ingestion and migrations. readDB is a
	// small read-only pool so dashboard queries do not queue behind a scan.
	db       *sql.DB
	readDB   *sql.DB
	path     string
	machine  model.Machine
	revision atomic.Uint64
}

type FileCursor struct {
	Path          string
	CodexHome     string
	Size          int64
	ModifiedNanos int64
	Offset        int64
	SessionID     string
	ForkedFromID  string
	ReplayOffset  int64
	Model         string
	TurnID        string
	ProjectPath   string
	Source        string
	AgentType     string
	Segment       int64
	PrefixHash    string
	LastEventID   string
	Cumulative    model.TokenUsage
	// InheritedBaseline is transient and is not persisted in file_cursors. It
	// marks a session-wide cumulative value loaded for a new/restored file.
	InheritedBaseline bool
}

type Status struct {
	Machine          model.Machine `json:"machine"`
	DatabasePath     string        `json:"database_path"`
	AccountingMode   string        `json:"accounting_mode"`
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

// CoverageGap is retained only so the /api/v1/status response remains source
// compatible with pre-1.0 clients. JSONL-only accounting never creates gaps.
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

type DimensionValues struct {
	Models   []string `json:"models"`
	Sources  []string `json:"sources"`
	Projects []string `json:"projects"`
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
	readDB, err := sql.Open("sqlite", sqliteURI(path,
		"mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"))
	if err != nil {
		db.Close()
		return nil, err
	}
	readDB.SetMaxOpenConns(2)
	readDB.SetMaxIdleConns(2)
	if err := readDB.PingContext(ctx); err != nil {
		readDB.Close()
		db.Close()
		return nil, fmt.Errorf("打开只读查询池: %w", err)
	}
	s.readDB = readDB
	s.revision.Store(1)
	_ = os.Chmod(path, 0o600)
	return s, nil
}

func (s *Store) Close() error {
	var readErr error
	if s.readDB != nil {
		readErr = s.readDB.Close()
	}
	return errors.Join(readErr, s.db.Close())
}
func (s *Store) DBPath() string         { return s.path }
func (s *Store) Machine() model.Machine { return s.machine }
func (s *Store) Revision() uint64       { return s.revision.Load() }

func (s *Store) reader() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

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
			local_date TEXT NOT NULL DEFAULT '',
			local_hour TEXT NOT NULL DEFAULT '',
			segment INTEGER NOT NULL DEFAULT 0,
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
		`CREATE INDEX IF NOT EXISTS idx_events_provenance_local_date ON usage_events(provenance,local_date)`,
		`CREATE INDEX IF NOT EXISTS idx_events_provenance_usage_at ON usage_events(provenance,usage_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_provenance_session_usage ON usage_events(provenance,session_id,usage_at)`,
		`CREATE TABLE IF NOT EXISTS file_cursors (
			path TEXT PRIMARY KEY,
			codex_home TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			modified_nanos INTEGER NOT NULL DEFAULT 0,
			offset INTEGER NOT NULL DEFAULT 0,
			session_id TEXT NOT NULL DEFAULT '',
			forked_from_id TEXT NOT NULL DEFAULT '',
			replay_offset INTEGER NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT '',
			turn_id TEXT NOT NULL DEFAULT '',
			project_path TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT 'main',
			segment INTEGER NOT NULL DEFAULT 0,
			prefix_hash TEXT NOT NULL DEFAULT '',
			last_event_id TEXT NOT NULL DEFAULT '',
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
	if databaseVersion < 3 {
		for _, stmt := range []string{
			`ALTER TABLE usage_events ADD COLUMN local_date TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE usage_events ADD COLUMN local_hour TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE file_cursors ADD COLUMN prefix_hash TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE file_cursors ADD COLUMN last_event_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE file_cursors ADD COLUMN forked_from_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE file_cursors ADD COLUMN replay_offset INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return err
			}
		}
	}
	if databaseVersion < 4 {
		if _, err := tx.ExecContext(ctx,
			`ALTER TABLE usage_events ADD COLUMN segment INTEGER NOT NULL DEFAULT 0`); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return err
		}
	}
	if databaseVersion > 0 && databaseVersion < schemaVersion {
		// Parser migrations can invalidate every derived event. Preserve the old
		// ledger until the user explicitly approves a rebuild instead of deleting
		// it while merely opening the database.
		reason := fmt.Sprintf("统计库已从 schema v%d 升级到 v%d，解析规则变化需要全量重建", databaseVersion, schemaVersion)
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, historicalRebuildReasonKey, reason); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('accounting_mode','jsonl_only')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
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
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions(
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
			archived=excluded.archived
		WHERE (excluded.rollout_path<>'' AND excluded.rollout_path<>sessions.rollout_path)
			OR (excluded.codex_home<>'' AND excluded.codex_home<>sessions.codex_home)
			OR (excluded.title<>'' AND excluded.title<>sessions.title)
			OR (excluded.project_path<>'' AND excluded.project_path<>sessions.project_path)
			OR (excluded.model<>'' AND excluded.model<>sessions.model)
			OR (excluded.source<>'' AND excluded.source<>sessions.source)
			OR (excluded.thread_source<>'' AND excluded.thread_source<>sessions.thread_source)
			OR (excluded.agent_type<>'' AND excluded.agent_type<>sessions.agent_type)
			OR (excluded.cli_version<>'' AND excluded.cli_version<>sessions.cli_version)
			OR (excluded.tokens_used>0 AND excluded.tokens_used<>sessions.tokens_used)
			OR (excluded.created_at>0 AND excluded.created_at<>sessions.created_at)
			OR excluded.updated_at>sessions.updated_at
			OR excluded.archived<>sessions.archived`,
		in.SessionID, in.RolloutPath, in.CodexHome, in.Title, in.ProjectPath, in.Model,
		in.Source, in.ThreadSource, defaultAgent(in.AgentType), in.CLIValue, in.TokensUsed,
		unixOrZero(in.CreatedAt), unixOrZero(in.UpdatedAt), boolInt(in.Archived))
	if err != nil {
		return err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr == nil && changed > 0 {
		s.revision.Add(1)
	}
	return nil
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
	if !event.Timestamp.IsZero() {
		local := event.Timestamp.In(time.Local)
		if event.LocalDate == "" {
			event.LocalDate = local.Format("2006-01-02")
		}
		if event.LocalHour == "" {
			event.LocalHour = local.Format("2006-01-02T15")
		}
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO usage_events(
		id,usage_at,local_date,local_hour,segment,observed_at,machine_id,session_id,turn_id,model,source,agent_type,
		project_path,thread_title,input_tokens,cached_input_tokens,cache_write_input_tokens,
		output_tokens,reasoning_output_tokens,total_tokens,provenance,confidence,codex_home,origin_path)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.ID, unixOrZero(event.Timestamp), event.LocalDate, event.LocalHour, event.Segment,
		unixOrZero(event.ObservedAt), event.MachineID,
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

type classificationCorrectionRow struct {
	id     string
	before model.TokenUsage
	after  model.TokenUsage
}

// CorrectEventUsage applies a later same-total classification snapshot across
// the preceding events in the same cumulative segment. Corrections are spread
// newest-first while every stored row remains internally consistent, so a
// correction larger than the last delta is not silently lost.
func (s *Store) CorrectEventUsage(
	ctx context.Context,
	eventID, sessionID string,
	segment int64,
	difference model.TokenUsage,
) (bool, error) {
	return s.correctEventUsage(ctx, eventID, sessionID, segment, difference, nil)
}

// CorrectEventUsageWithSessionProgress applies a classification correction
// and advances the corresponding session high-water mark in one transaction.
// A failed progress write rolls back every event correction.
func (s *Store) CorrectEventUsageWithSessionProgress(
	ctx context.Context,
	eventID, sessionID string,
	segment int64,
	difference, progress model.TokenUsage,
) (bool, error) {
	return s.correctEventUsage(ctx, eventID, sessionID, segment, difference, &progress)
}

func (s *Store) correctEventUsage(
	ctx context.Context,
	eventID, sessionID string,
	segment int64,
	difference model.TokenUsage,
	progress *model.TokenUsage,
) (bool, error) {
	if difference.IsZero() {
		if progress != nil {
			return false, s.PutSessionProgress(ctx, sessionID, segment, *progress)
		}
		return false, nil
	}
	if difference.Total != 0 || difference.Input+difference.Output != 0 {
		return false, fmt.Errorf("classification correction changed additive totals: %s", difference)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed, err := correctEventUsageInTx(ctx, tx, eventID, sessionID, segment, difference)
	if err != nil {
		return false, err
	}
	if progress != nil {
		if err := putSessionProgress(ctx, tx, sessionID, segment, *progress); err != nil {
			return false, err
		}
	}
	if !changed && progress == nil {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if changed {
		s.revision.Add(1)
	}
	return changed, nil
}

func correctEventUsageInTx(
	ctx context.Context,
	tx *sql.Tx,
	eventID, sessionID string,
	segment int64,
	difference model.TokenUsage,
) (bool, error) {
	if eventID != "" {
		var anchorSession string
		var anchorSegment int64
		err := tx.QueryRowContext(ctx, `SELECT session_id,segment FROM usage_events
			WHERE id=? AND provenance='session_jsonl'`, eventID).Scan(&anchorSession, &anchorSegment)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if sessionID == "" {
			sessionID = anchorSession
		}
		if sessionID != anchorSession || segment != anchorSegment {
			return false, fmt.Errorf("classification correction anchor does not match session segment")
		}
	}
	if sessionID == "" {
		return false, nil
	}

	databaseRows, err := tx.QueryContext(ctx, `SELECT id,input_tokens,cached_input_tokens,
		cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens
		FROM usage_events WHERE session_id=? AND segment=? AND provenance='session_jsonl'
		ORDER BY usage_at DESC,observed_at DESC,rowid DESC`, sessionID, segment)
	if err != nil {
		return false, err
	}
	var rows []classificationCorrectionRow
	for databaseRows.Next() {
		var row classificationCorrectionRow
		if err := databaseRows.Scan(&row.id, &row.before.Input, &row.before.CachedInput,
			&row.before.CacheWriteInput, &row.before.Output, &row.before.ReasoningOutput,
			&row.before.Total); err != nil {
			databaseRows.Close()
			return false, err
		}
		row.after = row.before
		rows = append(rows, row)
	}
	if err := databaseRows.Err(); err != nil {
		databaseRows.Close()
		return false, err
	}
	if err := databaseRows.Close(); err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}

	remaining := difference
	for _, category := range []string{"cached", "cache_write", "reasoning"} {
		remaining = applySubsetCorrection(rows, category, remaining, false)
	}
	remaining = applyParentCorrection(rows, remaining)
	for _, category := range []string{"cached", "cache_write", "reasoning"} {
		remaining = applySubsetCorrection(rows, category, remaining, true)
	}
	if !remaining.IsZero() {
		return false, fmt.Errorf("classification correction exceeds preceding session capacity: remaining=(%s)", remaining)
	}

	changed := false
	for _, row := range rows {
		if row.after.Equal(row.before) {
			continue
		}
		if !validEventUsage(row.after) {
			return false, fmt.Errorf("classification correction would make event %s inconsistent: %s", row.id, row.after)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE usage_events SET input_tokens=?,cached_input_tokens=?,
			cache_write_input_tokens=?,output_tokens=?,reasoning_output_tokens=? WHERE id=?`,
			row.after.Input, row.after.CachedInput, row.after.CacheWriteInput,
			row.after.Output, row.after.ReasoningOutput, row.id); err != nil {
			return false, err
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, nil
}

func applySubsetCorrection(
	rows []classificationCorrectionRow,
	category string,
	remaining model.TokenUsage,
	positive bool,
) model.TokenUsage {
	var delta int64
	switch category {
	case "cached":
		delta = remaining.CachedInput
	case "cache_write":
		delta = remaining.CacheWriteInput
	case "reasoning":
		delta = remaining.ReasoningOutput
	default:
		return remaining
	}
	if (positive && delta <= 0) || (!positive && delta >= 0) {
		return remaining
	}
	for index := range rows {
		if delta == 0 {
			break
		}
		row := &rows[index].after
		if delta < 0 {
			available := int64(0)
			switch category {
			case "cached":
				available = row.CachedInput
			case "cache_write":
				available = row.CacheWriteInput
			case "reasoning":
				available = row.ReasoningOutput
			}
			take := minInt64(-delta, available)
			switch category {
			case "cached":
				row.CachedInput -= take
			case "cache_write":
				row.CacheWriteInput -= take
			case "reasoning":
				row.ReasoningOutput -= take
			}
			delta += take
			continue
		}
		capacity := int64(0)
		switch category {
		case "cached", "cache_write":
			capacity = row.Input - row.CachedInput - row.CacheWriteInput
		case "reasoning":
			capacity = row.Output - row.ReasoningOutput
		}
		if capacity <= 0 {
			continue
		}
		take := minInt64(delta, capacity)
		switch category {
		case "cached":
			row.CachedInput += take
		case "cache_write":
			row.CacheWriteInput += take
		case "reasoning":
			row.ReasoningOutput += take
		}
		delta -= take
	}
	switch category {
	case "cached":
		remaining.CachedInput = delta
	case "cache_write":
		remaining.CacheWriteInput = delta
	case "reasoning":
		remaining.ReasoningOutput = delta
	}
	return remaining
}

func applyParentCorrection(rows []classificationCorrectionRow, remaining model.TokenUsage) model.TokenUsage {
	for index := range rows {
		if remaining.Input == 0 {
			break
		}
		row := &rows[index].after
		if remaining.Input > 0 {
			capacity := row.Output - row.ReasoningOutput
			take := minInt64(remaining.Input, capacity)
			row.Input += take
			row.Output -= take
			remaining.Input -= take
			remaining.Output += take
			continue
		}
		capacity := row.Input - row.CachedInput - row.CacheWriteInput
		take := minInt64(-remaining.Input, capacity)
		row.Input -= take
		row.Output += take
		remaining.Input += take
		remaining.Output -= take
	}
	return remaining
}

func validEventUsage(usage model.TokenUsage) bool {
	return usage.NonNegative() &&
		usage.CachedInput+usage.CacheWriteInput <= usage.Input &&
		usage.ReasoningOutput <= usage.Output &&
		usage.Input+usage.Output == usage.Total
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func (s *Store) GetCursor(ctx context.Context, path string) (FileCursor, bool, error) {
	var out FileCursor
	var cumulative string
	err := s.db.QueryRowContext(ctx, `SELECT path,codex_home,size,modified_nanos,offset,
		session_id,forked_from_id,replay_offset,model,turn_id,project_path,source,agent_type,segment,prefix_hash,last_event_id,cumulative_json
		FROM file_cursors WHERE path=?`, path).Scan(
		&out.Path, &out.CodexHome, &out.Size, &out.ModifiedNanos, &out.Offset,
		&out.SessionID, &out.ForkedFromID, &out.ReplayOffset, &out.Model, &out.TurnID, &out.ProjectPath, &out.Source,
		&out.AgentType, &out.Segment, &out.PrefixHash, &out.LastEventID, &cumulative)
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
		path,codex_home,size,modified_nanos,offset,session_id,forked_from_id,replay_offset,model,turn_id,project_path,
		source,agent_type,segment,prefix_hash,last_event_id,cumulative_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET codex_home=excluded.codex_home,size=excluded.size,
		modified_nanos=excluded.modified_nanos,offset=excluded.offset,session_id=excluded.session_id,
		forked_from_id=excluded.forked_from_id,replay_offset=excluded.replay_offset,
		model=excluded.model,turn_id=excluded.turn_id,project_path=excluded.project_path,
		source=excluded.source,agent_type=excluded.agent_type,segment=excluded.segment,
		prefix_hash=excluded.prefix_hash,
		last_event_id=excluded.last_event_id,
		cumulative_json=excluded.cumulative_json`,
		in.Path, in.CodexHome, in.Size, in.ModifiedNanos, in.Offset, in.SessionID,
		in.ForkedFromID, in.ReplayOffset,
		in.Model, in.TurnID, in.ProjectPath, in.Source, defaultAgent(in.AgentType),
		in.Segment, in.PrefixHash, in.LastEventID, string(data))
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
	return putSessionProgress(ctx, s.db, sessionID, segment, usage)
}

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func putSessionProgress(
	ctx context.Context,
	execer contextExecer,
	sessionID string,
	segment int64,
	usage model.TokenUsage,
) error {
	if sessionID == "" || usage.IsZero() {
		return nil
	}
	raw, _ := json.Marshal(usage)
	_, err := execer.ExecContext(ctx, `INSERT INTO session_cursors(
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
		`DELETE FROM usage_events`,
		`DELETE FROM file_cursors`,
		`DELETE FROM session_cursors`,
		`DELETE FROM sessions`,
		`DELETE FROM scan_state`,
		`DELETE FROM warnings`,
		`DELETE FROM meta WHERE key='historical_rebuild_required'`,
		`DROP TABLE IF EXISTS otel_series`,
		`DROP TABLE IF EXISTS otel_coverage`,
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

// HistoricalRebuildReason reports a pending parser migration without changing
// or discarding the existing derived ledger.
func (s *Store) HistoricalRebuildReason(ctx context.Context) (string, bool, error) {
	var reason string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, historicalRebuildReasonKey).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return reason, true, nil
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
	rows, err := s.reader().QueryContext(ctx, `SELECT id,created_at,first_seen,occurrences,kind,path,detail
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
	err := s.reader().QueryRowContext(ctx, query, args...).Scan(
		&out.Usage.Input, &out.Usage.CachedInput, &out.Usage.CacheWriteInput,
		&out.Usage.Output, &out.Usage.ReasoningOutput, &out.Usage.Total,
		&out.EventCount, &out.SessionCount, &first, &last)
	if err != nil {
		return out, err
	}
	out.FirstEvent = timeFromUnix(first)
	out.LastEvent = timeFromUnix(last)
	out.GrandTotal = out.Usage.Total
	var warnings int64
	_ = s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM warnings`).Scan(&warnings)
	out.CoverageIncomplete = warnings > 0
	return out, nil
}

func (s *Store) Timeseries(ctx context.Context, filter model.Filter, bucket string) ([]model.Point, error) {
	where, args := canonicalWhere(filter, "e")
	if requiresAttribution(filter) {
		where, args = attributionWhere(filter, "e", false)
	}
	bucketColumn := "e.local_date"
	if bucket == "hour" {
		bucketColumn = "e.local_hour"
	}
	rows, err := s.reader().QueryContext(ctx, `SELECT `+bucketColumn+`,
		COALESCE(SUM(e.input_tokens),0),COALESCE(SUM(e.cached_input_tokens),0),
		COALESCE(SUM(e.cache_write_input_tokens),0),COALESCE(SUM(e.output_tokens),0),
		COALESCE(SUM(e.reasoning_output_tokens),0),COALESCE(SUM(e.total_tokens),0)
		FROM usage_events e WHERE `+where+` AND e.usage_at>0 AND `+bucketColumn+`<>''
		GROUP BY `+bucketColumn+` ORDER BY `+bucketColumn, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Point
	for rows.Next() {
		var key string
		var usage model.TokenUsage
		if err := rows.Scan(&key, &usage.Input, &usage.CachedInput,
			&usage.CacheWriteInput, &usage.Output, &usage.ReasoningOutput,
			&usage.Total); err != nil {
			return nil, err
		}
		layout := "2006-01-02"
		if bucket == "hour" {
			layout = "2006-01-02T15"
		}
		parsed, _ := time.ParseInLocation(layout, key, time.UTC)
		out = append(out, model.Point{Time: parsed, Date: key, Usage: usage})
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	rows, err := s.reader().QueryContext(ctx, query, args...)
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

// Dimensions returns only distinct filter values. The dashboard uses this
// lightweight query lazily instead of running three full token breakdowns at
// startup merely to populate select controls.
func (s *Store) Dimensions(ctx context.Context) (DimensionValues, error) {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT 'model' AS kind,model AS value FROM usage_events
			WHERE provenance=? AND model<>'' GROUP BY model
		UNION ALL
		SELECT 'source' AS kind,source AS value FROM usage_events
			WHERE provenance=? AND source<>'' GROUP BY source
		UNION ALL
		SELECT 'project' AS kind,project_path AS value FROM usage_events
			WHERE provenance=? AND project_path<>'' GROUP BY project_path
		ORDER BY kind,value COLLATE NOCASE`,
		model.ProvenanceSessionJSONL, model.ProvenanceSessionJSONL, model.ProvenanceSessionJSONL)
	if err != nil {
		return DimensionValues{}, err
	}
	defer rows.Close()
	var out DimensionValues
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return DimensionValues{}, err
		}
		switch kind {
		case "model":
			if len(out.Models) < 500 {
				out.Models = append(out.Models, value)
			}
		case "source":
			if len(out.Sources) < 500 {
				out.Sources = append(out.Sources, value)
			}
		case "project":
			if len(out.Projects) < 500 {
				out.Projects = append(out.Projects, value)
			}
		}
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
	if filter.Search != "" {
		pattern := "%" + strings.NewReplacer("~", "~~", "%", "~%", "_", "~_").Replace(filter.Search) + "%"
		where += ` AND (e.session_id LIKE ? ESCAPE '~'
			OR e.thread_title LIKE ? ESCAPE '~'
			OR e.project_path LIKE ? ESCAPE '~'
			OR e.model LIKE ? ESCAPE '~'
			OR e.source LIKE ? ESCAPE '~'
			OR EXISTS (SELECT 1 FROM sessions search_session
				WHERE search_session.session_id=e.session_id AND (
					search_session.title LIKE ? ESCAPE '~'
					OR search_session.project_path LIKE ? ESCAPE '~'
					OR search_session.model LIKE ? ESCAPE '~'
					OR search_session.source LIKE ? ESCAPE '~')))`
		for range 9 {
			args = append(args, pattern)
		}
	}
	args = append(args, limit, offset)
	query := `SELECT COALESCE(NULLIF(e.session_id,''),'jsonl-unknown') sid,
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
	rows, err := s.reader().QueryContext(ctx, query, args...)
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

// WalkSessionPricingAggregates emits the pricing view for a bounded set of
// sessions in one query. It preserves model boundaries so sessions that switch
// models receive the same estimate as their underlying events.
func (s *Store) WalkSessionPricingAggregates(ctx context.Context, filter model.Filter, sessionIDs []string, fn func(model.UsageEvent) error) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	if len(sessionIDs) > 500 {
		return fmt.Errorf("Session 费用估算最多支持 500 项")
	}
	where, args := attributionWhere(filter, "e", true)
	sessionExpr := "COALESCE(NULLIF(e.session_id,''),'jsonl-unknown')"
	placeholders := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		placeholders = append(placeholders, "?")
		args = append(args, sessionID)
	}
	where += " AND " + sessionExpr + " IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.reader().QueryContext(ctx, `SELECT `+sessionExpr+`,e.model,
		COALESCE(MIN(NULLIF(e.usage_at,0)),0),
		COALESCE(SUM(e.input_tokens),0),COALESCE(SUM(e.cached_input_tokens),0),
		COALESCE(SUM(e.cache_write_input_tokens),0),COALESCE(SUM(e.output_tokens),0),
		COALESCE(SUM(e.reasoning_output_tokens),0),COALESCE(SUM(e.total_tokens),0)
		FROM usage_events e WHERE `+where+` AND `+pricingAggregatableEventSQL+`
		GROUP BY `+sessionExpr+`,e.model,`+pricingClassSQL, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var event model.UsageEvent
		var usageAt int64
		if err := rows.Scan(&event.SessionID, &event.Model, &usageAt,
			&event.Usage.Input, &event.Usage.CachedInput, &event.Usage.CacheWriteInput,
			&event.Usage.Output, &event.Usage.ReasoningOutput, &event.Usage.Total); err != nil {
			rows.Close()
			return err
		}
		event.Timestamp = timeFromUnix(usageAt)
		if err := fn(event); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	invalidArgs := append([]any{}, args...)
	invalidRows, err := s.reader().QueryContext(ctx, `SELECT `+sessionExpr+`,usage_at,model,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens
		FROM usage_events e WHERE `+where+` AND NOT (`+pricingAggregatableEventSQL+`)`, invalidArgs...)
	if err != nil {
		return err
	}
	defer invalidRows.Close()
	for invalidRows.Next() {
		var event model.UsageEvent
		var usageAt int64
		if err := invalidRows.Scan(&event.SessionID, &usageAt, &event.Model,
			&event.Usage.Input, &event.Usage.CachedInput, &event.Usage.CacheWriteInput,
			&event.Usage.Output, &event.Usage.ReasoningOutput, &event.Usage.Total); err != nil {
			return err
		}
		event.Timestamp = timeFromUnix(usageAt)
		if err := fn(event); err != nil {
			return err
		}
	}
	return invalidRows.Err()
}

func (s *Store) Events(ctx context.Context, query EventQuery) ([]model.UsageEvent, error) {
	return s.queryEvents(ctx, query, false)
}

// WalkEvents traverses the canonical export view in one stable SQLite read
// snapshot. A total ordering avoids duplicate or skipped rows when timestamps
// are equal, while avoiding the repeated work of OFFSET pagination.
func (s *Store) WalkEvents(ctx context.Context, filter model.Filter, fn func(model.UsageEvent) error) error {
	where, args := canonicalWithFallbackWhere(filter, "e")
	rows, err := s.reader().QueryContext(ctx, `SELECT id,usage_at,local_date,local_hour,observed_at,machine_id,
		session_id,turn_id,model,source,agent_type,project_path,thread_title,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens,provenance,confidence,codex_home
		FROM usage_events e WHERE `+where+` ORDER BY e.usage_at DESC,e.observed_at DESC,e.id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanUsageEvent(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := fn(item); err != nil {
			return err
		}
	}
	return rows.Err()
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
	rows, err := s.reader().QueryContext(ctx, `SELECT id,usage_at,local_date,local_hour,observed_at,machine_id,
		session_id,turn_id,model,source,agent_type,project_path,thread_title,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens,provenance,confidence,codex_home
		FROM usage_events e WHERE `+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanUsageEvent(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := fn(item); err != nil {
			return err
		}
	}
	return rows.Err()
}

// WalkPricingAggregates emits one valid aggregate per local day and model.
// Rows that are unsafe to aggregate remain individual so pricing diagnostics
// are byte-for-byte equivalent to evaluating raw events.
func (s *Store) WalkPricingAggregates(ctx context.Context, filter model.Filter, fn func(model.UsageEvent) error) error {
	where, args := canonicalWithFallbackWhere(filter, "e")
	if requiresAttribution(filter) {
		where, args = attributionWhere(filter, "e", true)
	}
	rows, err := s.reader().QueryContext(ctx, `SELECT e.local_date,e.model,
		COALESCE(MIN(NULLIF(e.usage_at,0)),0),
		COALESCE(SUM(e.input_tokens),0),COALESCE(SUM(e.cached_input_tokens),0),
		COALESCE(SUM(e.cache_write_input_tokens),0),COALESCE(SUM(e.output_tokens),0),
		COALESCE(SUM(e.reasoning_output_tokens),0),COALESCE(SUM(e.total_tokens),0)
		FROM usage_events e WHERE `+where+` AND `+pricingAggregatableEventSQL+`
		GROUP BY e.local_date,e.model,`+pricingClassSQL, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var event model.UsageEvent
		var usageAt int64
		if err := rows.Scan(&event.LocalDate, &event.Model, &usageAt,
			&event.Usage.Input, &event.Usage.CachedInput, &event.Usage.CacheWriteInput,
			&event.Usage.Output, &event.Usage.ReasoningOutput, &event.Usage.Total); err != nil {
			rows.Close()
			return err
		}
		event.Timestamp = timeFromUnix(usageAt)
		if err := fn(event); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	invalidArgs := append([]any{}, args...)
	invalidRows, err := s.reader().QueryContext(ctx, `SELECT usage_at,local_date,model,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens
		FROM usage_events e WHERE `+where+` AND NOT (`+pricingAggregatableEventSQL+`)`, invalidArgs...)
	if err != nil {
		return err
	}
	defer invalidRows.Close()
	for invalidRows.Next() {
		var event model.UsageEvent
		var usageAt int64
		if err := invalidRows.Scan(&usageAt, &event.LocalDate, &event.Model,
			&event.Usage.Input, &event.Usage.CachedInput, &event.Usage.CacheWriteInput,
			&event.Usage.Output, &event.Usage.ReasoningOutput, &event.Usage.Total); err != nil {
			return err
		}
		event.Timestamp = timeFromUnix(usageAt)
		if err := fn(event); err != nil {
			return err
		}
	}
	return invalidRows.Err()
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
	rows, err := s.reader().QueryContext(ctx, `SELECT id,usage_at,local_date,local_hour,observed_at,machine_id,
		session_id,turn_id,model,source,agent_type,project_path,thread_title,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,
		reasoning_output_tokens,total_tokens,provenance,confidence,codex_home
		FROM usage_events e WHERE `+where+` ORDER BY e.usage_at DESC,e.observed_at DESC,e.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageEvent
	for rows.Next() {
		item, scanErr := scanUsageEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type usageEventScanner interface {
	Scan(dest ...any) error
}

func scanUsageEvent(scanner usageEventScanner) (model.UsageEvent, error) {
	var item model.UsageEvent
	var usageAt, observedAt int64
	if err := scanner.Scan(&item.ID, &usageAt, &item.LocalDate, &item.LocalHour, &observedAt, &item.MachineID,
		&item.SessionID, &item.TurnID, &item.Model, &item.Source, &item.AgentType,
		&item.ProjectPath, &item.ThreadTitle, &item.Usage.Input,
		&item.Usage.CachedInput, &item.Usage.CacheWriteInput, &item.Usage.Output,
		&item.Usage.ReasoningOutput, &item.Usage.Total, &item.Provenance,
		&item.Confidence, &item.CodexHome); err != nil {
		return model.UsageEvent{}, err
	}
	item.Timestamp = timeFromUnix(usageAt)
	item.ObservedAt = timeFromUnix(observedAt)
	return item, nil
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{Machine: s.machine, DatabasePath: s.path, AccountingMode: "jsonl_only", DataRevision: s.revision.Load()}
	reader := s.reader()
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&out.EventCount); err != nil {
		return out, err
	}
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&out.SessionCount); err != nil {
		return out, err
	}
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM warnings`).Scan(&out.WarningCount); err != nil {
		return out, err
	}
	var scan int64
	_ = reader.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_scan),0) FROM scan_state`).Scan(&scan)
	if scan > 0 {
		value := timeFromUnix(scan)
		out.LastScan = &value
	}
	rows, err := reader.QueryContext(ctx, `SELECT codex_home,last_scan,state_db,files_scanned,warning
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

func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA optimize`)
	return err
}

func canonicalWhere(filter model.Filter, alias string) (string, []any) {
	if alias == "" {
		alias = "e"
	}
	parts := []string{alias + ".provenance='session_jsonl'"}
	var args []any
	if filter.SinceDate != "" {
		parts = append(parts, alias+".local_date>=?")
		args = append(args, filter.SinceDate)
	} else if !filter.Since.IsZero() {
		parts = append(parts, alias+".usage_at>=?")
		args = append(args, filter.Since.Unix())
	}
	if filter.UntilDate != "" {
		parts = append(parts, alias+".local_date<?")
		args = append(args, filter.UntilDate)
	} else if !filter.Until.IsZero() {
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

func canonicalWithFallbackWhere(filter model.Filter, alias string) (string, []any) {
	return canonicalWhere(filter, alias)
}

func requiresAttribution(filter model.Filter) bool {
	return filter.Project != "" || filter.SessionID != ""
}

func attributionWhere(filter model.Filter, alias string, includeFallback bool) (string, []any) {
	return canonicalWhere(filter, alias)
}

type stateThreadMetadata struct {
	info           model.SessionInfo
	parentThreadID string
	agentRole      string
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
	var threads []stateThreadMetadata
	for threadRows.Next() {
		var item model.SessionInfo
		var rawSource, agentRole string
		var created, updated, archived int64
		if err := threadRows.Scan(&item.SessionID, &item.RolloutPath, &item.ProjectPath,
			&item.Title, &item.Model, &rawSource, &item.ThreadSource, &agentRole, &item.CLIValue,
			&item.TokensUsed, &created, &updated, &archived); err != nil {
			return nil, err
		}
		parentThreadID, sourceAgentRole := stateSubagentMetadata(rawSource)
		if agentRole == "" {
			agentRole = sourceAgentRole
		}
		item.CodexHome = codexHome
		item.Source = compactStateSource(rawSource)
		item.AgentType = model.ClassifyAgent(item.Source, item.ThreadSource, agentRole)
		item.CreatedAt = flexibleEpoch(created)
		item.UpdatedAt = flexibleEpoch(updated)
		item.Archived = archived != 0
		threads = append(threads, stateThreadMetadata{info: item, parentThreadID: parentThreadID, agentRole: agentRole})
	}
	if err := threadRows.Err(); err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(threads))
	for index := range threads {
		byID[threads[index].info.SessionID] = index
	}
	out := make([]model.SessionInfo, 0, len(threads))
	for _, thread := range threads {
		item := thread.info
		if item.Title == "" && item.AgentType == "subagent" {
			parentTitle := stateAncestorTitle(thread.parentThreadID, threads, byID)
			switch {
			case parentTitle != "" && thread.agentRole != "":
				item.Title = thread.agentRole + " · " + parentTitle
			case parentTitle != "":
				item.Title = "Subagent · " + parentTitle
			case thread.agentRole != "":
				item.Title = "Subagent · " + thread.agentRole
			default:
				item.Title = "Subagent"
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func stateSubagentMetadata(value string) (parentThreadID, agentRole string) {
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
				AgentRole      string `json:"agent_role"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal([]byte(value), &source) != nil {
		return "", ""
	}
	return strings.TrimSpace(source.Subagent.ThreadSpawn.ParentThreadID),
		strings.TrimSpace(source.Subagent.ThreadSpawn.AgentRole)
}

func stateAncestorTitle(parentThreadID string, threads []stateThreadMetadata, byID map[string]int) string {
	seen := map[string]bool{}
	for parentThreadID != "" && !seen[parentThreadID] {
		seen[parentThreadID] = true
		index, ok := byID[parentThreadID]
		if !ok {
			return ""
		}
		thread := threads[index]
		if title := stateThreadTitleLine(thread.info.Title); title != "" {
			return title
		}
		parentThreadID = thread.parentThreadID
	}
	return ""
}

func stateThreadTitleLine(value string) string {
	title := strings.TrimSpace(value)
	if index := strings.IndexAny(title, "\r\n"); index >= 0 {
		title = strings.TrimSpace(title[:index])
	}
	return title
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
