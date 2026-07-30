package meter

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local-first/codex-meter/internal/model"
	"github.com/local-first/codex-meter/internal/store"
)

func TestScannerCumulativeDedupeModelSwitchResetAndPartialAppend(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	sessionDir := filepath.Join(home, "sessions", "2026", "07", "30")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-test.jsonl")
	lines := []string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-a","cwd":"/project/a","originator":"codex_cli_rs","cli_version":"0.145.0"}}`,
		`{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/project/a","model":"gpt-5.4"}}`,
		`{"timestamp":"2026-07-30T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
		tokenLine("2026-07-30T01:00:03Z", usage(80, 10, 5, 20, 2, 100), usage(80, 10, 5, 20, 2, 100)),
		tokenLine("2026-07-30T01:00:04Z", usage(80, 10, 5, 20, 2, 100), usage(80, 10, 5, 20, 2, 100)),
		tokenLine("2026-07-30T01:00:04.500Z", usage(80, 12, 5, 20, 2, 100), usage(0, 2, 0, 0, 0, 0)),
		`{"timestamp":"2026-07-30T01:00:05Z","type":"turn_context","payload":{"turn_id":"turn-2","cwd":"/project/a","model":"gpt-5.5"}}`,
		tokenLine("2026-07-30T01:00:06Z", usage(150, 30, 10, 50, 10, 200), usage(70, 20, 5, 30, 8, 100)),
		tokenLine("2026-07-30T01:00:07Z", usage(20, 2, 1, 5, 1, 25), usage(20, 2, 1, 5, 1, 25)),
		`{"timestamp":"2026-07-30T01:00:07Z","type":"event_msg","payload":{"type":"token_count",BROKEN}`,
		`{"timestamp":"2026-07-30T01:00:08Z","type":"event_msg","payload":{"type":"token_count","info":`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "meter.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &Scanner{Store: st, Now: func() time.Time { return time.Unix(2000000000, 0) }}
	first, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventsInserted != 3 || first.Duplicates != 2 {
		t.Fatalf("unexpected first scan: %+v", first)
	}
	summary, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Usage != (model.TokenUsage{Input: 170, CachedInput: 32, CacheWriteInput: 11, Output: 55, ReasoningOutput: 11, Total: 225}) {
		t.Fatalf("unexpected usage: %+v", summary.Usage)
	}
	models, err := st.Breakdown(context.Background(), model.Filter{}, "model", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, item := range models {
		got[item.Key] = item.Usage.Total
	}
	if got["gpt-5.4"] != 100 || got["gpt-5.5"] != 125 {
		t.Fatalf("model attribution mismatch: %+v", got)
	}
	warnings, _ := st.Warnings(context.Background(), 20)
	foundReset := false
	for _, warning := range warnings {
		foundReset = foundReset || warning.Kind == "cumulative_reset"
	}
	if !foundReset {
		t.Fatal("cumulative reset was not made visible")
	}

	completion := `{"total_token_usage":{"input_tokens":40,"cached_input_tokens":4,"cache_write_input_tokens":2,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":50},"last_token_usage":{"input_tokens":20,"cached_input_tokens":2,"cache_write_input_tokens":1,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":25}}` + "}}\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(completion); err != nil {
		t.Fatal(err)
	}
	file.Close()
	second, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsInserted != 1 {
		warnings, _ := st.Warnings(context.Background(), 20)
		t.Fatalf("partial append was not replayed: %+v warnings=%+v", second, warnings)
	}
	summary, _ = st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 250 {
		t.Fatalf("want 250 after append, got %d", summary.Usage.Total)
	}
}

func TestScannerStateFallbackAndSchemaChange(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(home, "sessions", "one.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"session-state","cwd":"/project/state","originator":"codex_desktop"}}`,
		tokenLine("2026-07-30T01:01:00Z", usage(80, 20, 0, 20, 5, 100), usage(80, 20, 0, 20, 5, 100)),
	}, "\n") + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(home, "state_5.sqlite")
	db, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT, title TEXT, model TEXT,
		source TEXT, thread_source TEXT, cli_version TEXT, tokens_used INTEGER,
		created_at INTEGER, updated_at INTEGER, archived INTEGER, future_column TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO threads VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"session-state", rollout, "/project/state", "状态库线程", "gpt-5.4",
		"codex_desktop", "main", "0.145.0", 150, 100, 200, 0, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, _ := store.Open(filepath.Join(root, "meter.sqlite"))
	defer st.Close()
	scanner := &Scanner{Store: st}
	result, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 {
		t.Fatalf("canonical state path not used: %+v", result)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 100 || summary.Unattributed.Total != 50 {
		t.Fatalf("fallback mismatch: %+v", summary)
	}
	sessions, err := st.Sessions(context.Background(), model.Filter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "状态库线程" || sessions[0].ProjectPath != "/project/state" {
		t.Fatalf("metadata mismatch: %+v", sessions)
	}
}

func TestMultipleHomesDuplicateRolloutCountsOnce(t *testing.T) {
	root := t.TempDir()
	homes := []string{filepath.Join(root, "a"), filepath.Join(root, "b")}
	for _, home := range homes {
		dir := filepath.Join(home, "sessions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"shared-session","cwd":"/p","originator":"codex_cli_rs"}}` + "\n" +
			tokenLine("2026-07-30T01:00:01Z", usage(40, 5, 0, 10, 1, 50), usage(40, 5, 0, 10, 1, 50)) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "same.jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := store.Open(filepath.Join(root, "meter.sqlite"))
	defer st.Close()
	result, err := (&Scanner{Store: st}).Scan(context.Background(), homes, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 50 || result.Duplicates == 0 {
		t.Fatalf("duplicate home was double counted: summary=%+v scan=%+v", summary, result)
	}
}

func TestResumedSessionUsesSessionWideCumulativeBaseline(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"resumed-session","cwd":"/p","originator":"codex_cli_rs"}}` + "\n"
	first := meta + tokenLine("2026-07-30T01:00:01Z",
		usage(80, 10, 0, 20, 2, 100), usage(80, 10, 0, 20, 2, 100)) + "\n"
	second := meta + tokenLine("2026-07-30T02:00:01Z",
		usage(120, 20, 0, 30, 4, 150), usage(40, 10, 0, 10, 2, 50)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.jsonl"), []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(root, "meter.sqlite"))
	defer st.Close()
	scanner := &Scanner{Store: st}
	result, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 150 || result.EventsInserted != 2 {
		t.Fatalf("resumed session double counted old cumulative: summary=%+v scan=%+v", summary, result)
	}
}

func TestStateSchemaMismatchFallsBackToSessionDirectories(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"schema-fallback","cwd":"/p","originator":"codex_cli_rs"}}` + "\n" +
		tokenLine("2026-07-30T01:00:01Z", usage(8, 1, 0, 2, 0, 10), usage(8, 1, 0, 2, 0, 10)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fallback.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDB, err := sql.Open("sqlite", filepath.Join(home, "state_99.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateDB.Exec(`CREATE TABLE unrelated(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	stateDB.Close()
	discovery := DiscoverHome(context.Background(), home)
	if !discovery.Fallback || discovery.Warning == "" || len(discovery.Paths) != 1 {
		t.Fatalf("schema mismatch did not fall back: %+v", discovery)
	}
	st, _ := store.Open(filepath.Join(root, "meter.sqlite"))
	defer st.Close()
	if _, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 10 {
		t.Fatalf("fallback scan missed data: %+v", summary)
	}
}

func TestStateStaleRolloutPathSupplementsSessionDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(dir, "actual.jsonl")
	if err := os.WriteFile(actual, []byte(
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"actual","cwd":"/p","originator":"codex_cli_rs"}}`+"\n"+
			tokenLine("2026-07-30T01:00:01Z", usage(8, 1, 0, 2, 0, 10), usage(8, 1, 0, 2, 0, 10))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDB, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateDB.Exec(`CREATE TABLE threads (id TEXT, rollout_path TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := stateDB.Exec(`INSERT INTO threads VALUES('stale','missing.jsonl')`); err != nil {
		t.Fatal(err)
	}
	stateDB.Close()

	discovery := DiscoverHome(context.Background(), home)
	if len(discovery.Paths) != 1 || discovery.Paths[0] != actual || discovery.Warning == "" {
		t.Fatalf("stale canonical path was not supplemented: %+v", discovery)
	}
}

func TestSelectiveReaderSkipsHugeIrrelevantRecord(t *testing.T) {
	huge := `{"type":"response_item","payload":{"content":"` + strings.Repeat("x", 12<<20) + `"}}` + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(huge), 64<<10)
	record, consumed, complete, tooLarge, err := readSelectiveRecord(reader, 1<<20)
	if err != nil || !complete || tooLarge || len(record) != 0 || consumed != int64(len(huge)) {
		t.Fatalf("unexpected selective read: len=%d consumed=%d complete=%v tooLarge=%v err=%v",
			len(record), consumed, complete, tooLarge, err)
	}
}

func TestSelectiveReaderCapsHugeRelevantRecord(t *testing.T) {
	huge := `{"type":"event_msg","payload":{"type":"token_count","info":{"padding":"` +
		strings.Repeat("x", 2<<20) + `"}}}` + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(huge), 64<<10)
	record, consumed, complete, tooLarge, err := readSelectiveRecord(reader, 1<<20)
	if err != nil || !complete || !tooLarge || len(record) != 0 || consumed != int64(len(huge)) {
		t.Fatalf("unexpected capped read: len=%d consumed=%d complete=%v tooLarge=%v err=%v",
			len(record), consumed, complete, tooLarge, err)
	}
}

func TestSessionSourceClassifiesBackgroundAgentsWithoutPersistingSourceJSON(t *testing.T) {
	for raw, want := range map[string]string{
		`{"subagent":{"other":"guardian"}}`: "guardian",
		`{"subagent":{"thread_spawn":{"parent_thread_id":"secret-parent","agent_role":"worker"}}}`: "subagent",
		`{"subagent":{"other":"memory"}}`: "memory",
	} {
		got := rawText(json.RawMessage(raw))
		if got != want {
			t.Fatalf("rawText(%s)=%q want %q", raw, got, want)
		}
		if strings.Contains(got, "secret-parent") {
			t.Fatal("source descriptor leaked an internal parent id")
		}
	}
}

func TestScannerNeverPersistsConversationOrCredentials(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	conversationSecret := "PROMPT-SECRET-DO-NOT-PERSIST-8d305f"
	authSecret := "AUTH-SECRET-DO-NOT-PERSIST-c71b2e"
	content := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"privacy-session","cwd":"/safe/project","originator":"codex_cli_rs"}}` + "\n" +
		`{"timestamp":"2026-07-30T01:00:01Z","type":"response_item","payload":{"content":"` + conversationSecret + `"}}` + "\n" +
		tokenLine("2026-07-30T01:00:02Z", usage(8, 1, 0, 2, 0, 10), usage(8, 1, 0, 2, 0, 10)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "privacy.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"token":"`+authSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "meter.sqlite")
	st, _ := store.Open(dbPath)
	if _, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(dbPath + "*")
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), conversationSecret) || strings.Contains(string(data), authSecret) {
			t.Fatalf("sensitive content leaked into %s", path)
		}
	}
}

func usage(input, cached, cacheWrite, output, reasoning, total int64) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}`,
		input, cached, cacheWrite, output, reasoning, total)
}

func tokenLine(timestamp, total, last string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s}}}`,
		timestamp, total, last)
}
