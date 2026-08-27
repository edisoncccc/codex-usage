package usage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/store"
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
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &Scanner{Store: st, Now: func() time.Time { return time.Unix(2000000000, 0) }}
	first, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventsInserted != 3 || first.Corrections != 1 || first.Duplicates != 1 {
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
	if models[0].Usage.CachedInput+models[1].Usage.CachedInput != 32 {
		t.Fatalf("classification totals drifted: %+v", models)
	}
	for _, item := range models {
		if item.Key == "gpt-5.4" && item.Usage.CachedInput != 12 {
			t.Fatalf("same-total correction was not attributed to the original model: %+v", models)
		}
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

func TestScannerSplitsOneSessionAcrossLocalDaysAtEachTokenEvent(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })

	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions", "2026", "07", "15")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"timestamp":"2026-07-15T15:58:00Z","type":"session_meta","payload":{"id":"cross-midnight","cwd":"/project","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-07-15T15:58:01Z","type":"turn_context","payload":{"turn_id":"turn","cwd":"/project","model":"gpt-5.4"}}`,
		tokenLine("2026-07-15T15:59:00Z", usage(1600000, 0, 0, 400000, 0, 2000000), usage(1600000, 0, 0, 400000, 0, 2000000)),
		tokenLine("2026-07-15T16:01:00Z", usage(2400000, 0, 0, 600000, 0, 3000000), usage(800000, 0, 0, 200000, 0, 1000000)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "cross-midnight.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	points, err := st.Timeseries(context.Background(), model.Filter{}, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Usage.Total != 2000000 || points[1].Usage.Total != 1000000 {
		t.Fatalf("cross-midnight increments were not split by event timestamp: %+v", points)
	}
	if points[0].Time.In(time.Local).Day() != 15 || points[1].Time.In(time.Local).Day() != 16 {
		t.Fatalf("unexpected local-day buckets: %+v", points)
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
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
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
	if summary.Usage.Total != 100 || summary.Unattributed.Total != 0 || summary.GrandTotal != 100 {
		t.Fatalf("state tokens_used affected JSONL-only totals: %+v", summary)
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
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
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
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
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
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
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

func TestStateIndexOmittedRowStillSupplementsSessionDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	indexed := filepath.Join(dir, "indexed.jsonl")
	omitted := filepath.Join(dir, "omitted.jsonl")
	for _, path := range []string{indexed, omitted} {
		if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+filepath.Base(path)+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT, rollout_path TEXT); INSERT INTO threads VALUES('indexed',?)`, indexed); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	discovery := DiscoverHome(context.Background(), home)
	if len(discovery.Paths) != 2 || discovery.Warning == "" {
		t.Fatalf("state omission was not supplemented: %+v", discovery)
	}
}

func TestForkReplayPrefixIsSkippedAndFileOwnerNeverChanges(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"parent","cwd":"/parent","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-07-30T01:00:01Z","type":"turn_context","payload":{"turn_id":"parent-turn","cwd":"/parent","model":"gpt-5.4"}}`,
		tokenLine("2026-07-30T01:00:02Z", usage(120, 20, 0, 30, 3, 150), usage(120, 20, 0, 30, 3, 150)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "z-parent.jsonl"), []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, total := range []int64{180, 170, 160} {
		childID := fmt.Sprintf("child-%d", index)
		content := strings.Join([]string{
			fmt.Sprintf(`{"timestamp":"2026-07-30T02:00:00Z","type":"session_meta","payload":{"id":%q,"forked_from_id":"parent","cwd":"/child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`, childID),
			tokenLine("2026-07-30T01:00:02Z", usage(120, 20, 0, 30, 3, 150), usage(120, 20, 0, 30, 3, 150)),
			`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"parent","cwd":"/parent","originator":"codex_cli_rs"}}`,
			fmt.Sprintf(`{"timestamp":"2026-07-30T02:00:01Z","type":"turn_context","payload":{"turn_id":%q,"cwd":"/child","model":"gpt-5.6-terra"}}`, "turn-"+childID),
			tokenLine("2026-07-30T02:00:02Z", usage(total-30, 25, 0, 30, 3, total), usage(total-150, 5, 0, 0, 0, total-150)),
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("a-fork-%d.jsonl", index)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 210 || result.EventsInserted != 4 {
		t.Fatalf("fork replay was counted as new usage: summary=%+v scan=%+v", summary, result)
	}
	sessions, err := st.Sessions(context.Background(), model.Filter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	usageBySession := map[string]int64{}
	for _, session := range sessions {
		usageBySession[session.SessionID] = session.Usage.Total
		if strings.HasPrefix(session.SessionID, "child-") && (session.AgentType != "subagent" || session.ProjectPath != "/child") {
			t.Fatalf("fork continuation attribution changed to parent: %+v", session)
		}
	}
	if usageBySession["parent"] != 150 || usageBySession["child-0"] != 30 || usageBySession["child-1"] != 20 || usageBySession["child-2"] != 10 {
		t.Fatalf("unexpected per-session usage: %+v", usageBySession)
	}
}

func TestModernForkReplayBeforeChildTaskIsSkipped(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parentID := "019ff753-9210-7a41-9670-8f8d3a738a9d"
	childID := "019ffaca-c65e-78a3-8383-70d9d427eaf3"
	parent := strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-08-13T01:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/parent"}}`, parentID),
		tokenLine("2026-08-13T01:00:01Z", usage(240, 180, 0, 60, 10, 300), usage(240, 180, 0, 60, 10, 300)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "z-parent.jsonl"), []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "a-modern-child.jsonl")
	replayed := strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-08-13T02:00:00Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"cwd":"/child","source":{"subagent":{"thread_spawn":{"parent_thread_id":%q}}}}}`, childID, parentID, parentID),
		fmt.Sprintf(`{"timestamp":"2026-08-13T02:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/parent"}}`, parentID),
		`{"timestamp":"2026-08-13T02:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019ff753-96f5-7ba1-9874-706a1c2e5a07"}}`,
		tokenLine("2026-08-13T02:00:00Z", usage(120, 90, 0, 30, 5, 150), usage(120, 90, 0, 30, 5, 150)),
		`{"timestamp":"2026-08-13T02:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019ff974-bc57-7d93-8712-3627856b327a"}}`,
		tokenLine("2026-08-13T02:00:00Z", usage(240, 180, 0, 60, 10, 300), usage(120, 90, 0, 30, 5, 150)),
	}, "\n") + "\n"
	if err := os.WriteFile(childPath, []byte(replayed), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &Scanner{Store: st}
	first, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GrandTotal != 300 || first.EventsInserted != 1 {
		t.Fatalf("modern replay was exposed before the child task boundary: scan=%+v summary=%+v", first, summary)
	}
	cursor, ok, err := st.GetCursor(context.Background(), childPath)
	if err != nil || !ok || cursor.Offset != 0 || cursor.ReplayOffset != 0 {
		t.Fatalf("modern replay cursor should remain pending: ok=%v cursor=%+v err=%v", ok, cursor, err)
	}

	pending, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err = st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if pending.EventsInserted != 0 || summary.GrandTotal != 300 {
		t.Fatalf("unchanged pending modern fork exposed replay history: scan=%+v summary=%+v", pending, summary)
	}
	cursor, ok, err = st.GetCursor(context.Background(), childPath)
	if err != nil || !ok || cursor.Offset != 0 || cursor.ReplayOffset != 0 {
		t.Fatalf("unchanged pending modern fork advanced unexpectedly: ok=%v cursor=%+v err=%v", ok, cursor, err)
	}

	file, err := os.OpenFile(childPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	continuation := strings.Join([]string{
		`{"timestamp":"2026-08-13T02:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019ffaca-cac2-7122-bd5c-e30f9b7c3715"}}`,
		`{"timestamp":"2026-08-13T02:00:02Z","type":"turn_context","payload":{"turn_id":"019ffaca-cac2-7122-bd5c-e30f9b7c3715","cwd":"/child","model":"gpt-5.6-sol"}}`,
		tokenLine("2026-08-13T02:00:03Z", usage(264, 198, 0, 66, 11, 330), usage(24, 18, 0, 6, 1, 30)),
		tokenLine("2026-08-13T02:00:04Z", usage(280, 210, 0, 70, 12, 350), usage(16, 12, 0, 4, 1, 20)),
	}, "\n") + "\n"
	_, writeErr := file.WriteString(continuation)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	second, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err = st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GrandTotal != 350 || second.EventsInserted != 2 {
		t.Fatalf("modern child continuation was not isolated from replay: scan=%+v summary=%+v", second, summary)
	}
	sessions, err := st.Sessions(context.Background(), model.Filter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.SessionID == childID && (session.Usage.Total != 50 || session.AgentType != "subagent") {
			t.Fatalf("modern child attribution mismatch: %+v", session)
		}
	}
}

func TestModernForkRepeatedParentMetadataStaysPendingWithoutChildTask(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parentID := "019ff753-9210-7a41-9670-8f8d3a738a9d"
	childID := "019ffaca-c65e-78a3-8383-70d9d427eaf3"
	parent := strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-08-13T01:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/parent"}}`, parentID),
		tokenLine("2026-08-13T01:00:01Z", usage(240, 180, 0, 60, 10, 300), usage(240, 180, 0, 60, 10, 300)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "z-parent.jsonl"), []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	child := strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-08-13T02:00:00Z","type":"session_meta","payload":{"id":%q,"forked_from_id":%q,"cwd":"/child"}}`, childID, parentID),
		fmt.Sprintf(`{"timestamp":"2026-08-13T02:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/parent"}}`, parentID),
		`{"timestamp":"2026-08-13T02:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019ff753-96f5-7ba1-9874-706a1c2e5a07"}}`,
		tokenLine("2026-08-13T02:00:00Z", usage(120, 90, 0, 30, 5, 150), usage(120, 90, 0, 30, 5, 150)),
		fmt.Sprintf(`{"timestamp":"2026-08-13T02:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/parent"}}`, parentID),
		tokenLine("2026-08-13T02:00:00Z", usage(240, 180, 0, 60, 10, 300), usage(120, 90, 0, 30, 5, 150)),
	}, "\n") + "\n"
	childPath := filepath.Join(dir, "a-modern-repeated-parent.jsonl")
	if err := os.WriteFile(childPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GrandTotal != 300 || result.EventsInserted != 1 {
		t.Fatalf("repeated parent metadata exposed replay history: scan=%+v summary=%+v", result, summary)
	}
	cursor, ok, err := st.GetCursor(context.Background(), childPath)
	if err != nil || !ok || cursor.Offset != 0 || cursor.ReplayOffset != 0 {
		t.Fatalf("modern fork without a child task should remain pending: ok=%v cursor=%+v err=%v", ok, cursor, err)
	}
}

func TestSingleMetadataForkFixturesUseImplicitFirstSnapshotBaseline(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		sessionID   string
		want        model.TokenUsage
		appendTotal string
		appendLast  string
		wantAfter   model.TokenUsage
	}{
		{
			name:        "zero inherited baseline counts the complete child cumulative",
			fixture:     "single-meta-fork-zero-baseline.jsonl",
			sessionID:   "fork-child-zero-baseline",
			want:        model.TokenUsage{Input: 370, CachedInput: 300, Output: 55, ReasoningOutput: 10, Total: 425},
			appendTotal: usage(410, 330, 0, 65, 12, 475),
			appendLast:  usage(40, 30, 0, 10, 2, 50),
			wantAfter:   model.TokenUsage{Input: 410, CachedInput: 330, Output: 65, ReasoningOutput: 12, Total: 475},
		},
		{
			name:        "inherited baseline is removed while first last usage remains",
			fixture:     "single-meta-fork-inherited-baseline.jsonl",
			sessionID:   "fork-child-inherited-baseline",
			want:        model.TokenUsage{Input: 264, CachedInput: 230, Output: 32, ReasoningOutput: 7, Total: 296},
			appendTotal: usage(630, 550, 0, 78, 16, 708),
			appendLast:  usage(40, 30, 0, 10, 2, 50),
			wantAfter:   model.TokenUsage{Input: 304, CachedInput: 260, Output: 42, ReasoningOutput: 9, Total: 346},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, ".codex")
			dir := filepath.Join(home, "sessions")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, test.fixture)
			if err := os.WriteFile(path, fixture, 0o600); err != nil {
				t.Fatal(err)
			}

			st, err := store.Open(filepath.Join(root, "usage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			scanner := &Scanner{Store: st}
			result, err := scanner.Scan(context.Background(), []string{home}, false)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := st.Summary(context.Background(), model.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if result.EventsInserted != 2 || result.Warnings != 0 || summary.Usage != test.want {
				t.Fatalf("implicit fork baseline mismatch: scan=%+v want=%+v got=%+v", result, test.want, summary.Usage)
			}
			cursor, ok, err := st.GetCursor(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || cursor.SessionID != test.sessionID || cursor.ReplayOffset != implicitForkReplayOffset {
				t.Fatalf("implicit fork cursor mismatch: ok=%v cursor=%+v", ok, cursor)
			}

			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString(tokenLine("2026-08-01T00:00:00Z", test.appendTotal, test.appendLast) + "\n")
			closeErr := file.Close()
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			incremental, err := scanner.Scan(context.Background(), []string{home}, false)
			if err != nil {
				t.Fatal(err)
			}
			summary, err = st.Summary(context.Background(), model.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if incremental.EventsInserted != 1 || incremental.Warnings != 0 || summary.Usage != test.wantAfter {
				t.Fatalf("implicit fork incremental scan drifted: scan=%+v want=%+v got=%+v", incremental, test.wantAfter, summary.Usage)
			}
		})
	}
}

func TestImplicitForkWaitsForApprovalIfExplicitParentBoundaryArrivesLater(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"staged-parent","cwd":"/parent"}}`,
		tokenLine("2026-07-30T01:00:01Z", usage(120, 20, 0, 30, 3, 150), usage(120, 20, 0, 30, 3, 150)),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "z-parent.jsonl"), []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "a-staged-child.jsonl")
	staged := strings.Join([]string{
		`{"timestamp":"2026-07-30T02:00:00Z","type":"session_meta","payload":{"id":"staged-child","forked_from_id":"staged-parent","cwd":"/child"}}`,
		tokenLine("2026-07-30T01:00:01Z", usage(120, 20, 0, 30, 3, 150), usage(120, 20, 0, 30, 3, 150)),
	}, "\n") + "\n"
	if err := os.WriteFile(childPath, []byte(staged), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &Scanner{Store: st}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	provisional, _ := st.Summary(context.Background(), model.Filter{})
	if provisional.GrandTotal != 300 {
		t.Fatalf("staged prefix was not represented by the provisional implicit interpretation: %+v", provisional)
	}

	file, err := os.OpenFile(childPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	continuation := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"staged-parent","cwd":"/parent"}}`,
		`{"timestamp":"2026-07-30T02:00:01Z","type":"turn_context","payload":{"turn_id":"child-turn","cwd":"/child","model":"gpt-test"}}`,
		tokenLine("2026-07-30T02:00:02Z", usage(145, 25, 0, 35, 4, 180), usage(25, 5, 0, 5, 1, 30)),
	}, "\n") + "\n"
	_, writeErr := file.WriteString(continuation)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	result, err := scanner.Scan(context.Background(), []string{home}, false)
	var rebuildErr *RebuildRequiredError
	if !errors.As(err, &rebuildErr) || rebuildErr.Kind != "fork_replay_detected" {
		t.Fatalf("late explicit boundary did not request approval: result=%+v err=%v", result, err)
	}
	preserved, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if preserved.GrandTotal != 300 || result.Warnings == 0 {
		t.Fatalf("history changed before rebuild approval: scan=%+v summary=%+v", result, preserved)
	}
	warnings, err := st.Warnings(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || warnings[0].Kind != "fork_replay_detected" {
		t.Fatalf("pending rebuild was not visible: %+v", warnings)
	}
	approved, err := scanner.Scan(context.Background(), []string{home}, true)
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.GrandTotal != 180 || approved.EventsInserted == 0 {
		t.Fatalf("approved rebuild did not correct history: scan=%+v summary=%+v", approved, corrected)
	}
}

func TestSameTotalClassificationCorrectionPersists(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "correction.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"corrected","cwd":"/p","originator":"codex_cli_rs"}}`,
		tokenLine("2026-07-30T01:00:01Z", usage(80, 10, 0, 20, 2, 100), usage(80, 10, 0, 20, 2, 100)),
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
	defer st.Close()
	scanner := &Scanner{Store: st}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(tokenLine("2026-07-30T01:00:02Z", usage(80, 12, 0, 20, 3, 100), usage(0, 2, 0, 0, 1, 0)) + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	result, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if result.Corrections != 1 || summary.Usage.CachedInput != 12 || summary.Usage.ReasoningOutput != 3 || summary.GrandTotal != 100 {
		t.Fatalf("classification correction was lost: summary=%+v scan=%+v", summary, result)
	}
}

func TestSameTotalClassificationCorrectionCanReachEarlierEvents(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "correction-across-events.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"corrected-across-events","cwd":"/p","originator":"codex_cli_rs"}}`,
		tokenLine("2026-07-30T01:00:01Z", usage(80, 10, 0, 20, 2, 100), usage(80, 10, 0, 20, 2, 100)),
		tokenLine("2026-07-30T01:00:02Z", usage(160, 10, 0, 40, 2, 200), usage(80, 0, 0, 20, 0, 100)),
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &Scanner{Store: st}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	correction := tokenLine("2026-07-30T01:00:03Z",
		usage(150, 0, 0, 50, 2, 200), usage(0, 0, 0, 0, 0, 0)) + "\n"
	if _, err := file.WriteString(correction); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	result, err := scanner.Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := model.TokenUsage{Input: 150, Output: 50, ReasoningOutput: 2, Total: 200}
	if result.Corrections != 1 || summary.Usage != want {
		t.Fatalf("cross-event classification correction was lost: want=%+v summary=%+v scan=%+v", want, summary, result)
	}
}

func TestRewriteAndTruncationWaitForApprovalBeforeReplacingHistory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rewrite.jsonl")
	makeContent := func(input, output, total int64) string {
		return `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"rewrite","cwd":"/p","originator":"codex_cli_rs"}}` + "\n" +
			tokenLine("2026-07-30T01:00:01Z", usage(input, 0, 0, output, 0, total), usage(input, 0, 0, output, 0, total)) + "\n"
	}
	if err := os.WriteFile(path, []byte(makeContent(700, 200, 900)), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(root, "usage.sqlite"))
	defer st.Close()
	scanner := &Scanner{Store: st}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte(makeContent(600, 200, 800)), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err == nil {
		t.Fatal("same-size rewrite did not request rebuild approval")
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 900 {
		t.Fatalf("same-size rewrite changed history before approval: %+v", summary)
	}
	if _, err := scanner.Scan(context.Background(), []string{home}, true); err != nil {
		t.Fatal(err)
	}
	summary, _ = st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 800 {
		t.Fatalf("approved same-size rewrite left stale usage: %+v", summary)
	}
	if err := os.WriteFile(path, []byte(makeContent(40, 10, 50)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err == nil {
		t.Fatal("truncation did not request rebuild approval")
	}
	summary, _ = st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 800 {
		t.Fatalf("truncation changed history before approval: %+v", summary)
	}
	if _, err := scanner.Scan(context.Background(), []string{home}, true); err != nil {
		t.Fatal(err)
	}
	summary, _ = st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 50 {
		t.Fatalf("approved truncation left stale usage: %+v", summary)
	}
}

func TestScannerPutCursorFailureIsHardError(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"turn_context","payload":{"turn_id":"cursor-failure","model":"gpt-test"}}`,
	)
	databasePath := filepath.Join(root, "usage.sqlite")
	st, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	installScannerAbortTrigger(t, databasePath, "file_cursors", "forced PutCursor failure")

	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err == nil || !strings.Contains(err.Error(), "forced PutCursor failure") {
		t.Fatalf("PutCursor failure was downgraded: result=%+v err=%v", result, err)
	}
}

func TestScannerRecordUpsertSessionFailureIsHardError(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"session_meta","payload":{"id":"upsert-failure","cwd":"/project"}}`,
		tokenLine("2026-08-27T01:00:01Z", usage(8, 1, 0, 2, 0, 10), usage(8, 1, 0, 2, 0, 10)),
	)
	databasePath := filepath.Join(root, "usage.sqlite")
	st, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	installScannerAbortTrigger(t, databasePath, "sessions", "forced UpsertSession failure")

	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err == nil || !strings.Contains(err.Error(), "forced UpsertSession failure") {
		t.Fatalf("record UpsertSession failure was downgraded: result=%+v err=%v", result, err)
	}
}

func TestScannerContextCancellationInsideFileIsHardError(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"turn_context","payload":{"turn_id":"cancelled-scan","model":"gpt-test"}}`,
	)
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := (&Scanner{Store: st}).ScanWithProgress(ctx, []string{home}, false, func(progress ScanProgress) {
		if progress.HomesDiscovered == 1 && progress.FilesDiscovered == 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("in-file context cancellation was downgraded: result=%+v err=%v", result, err)
	}
}

func TestScannerUpdateScanStateFailureIsHardError(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"turn_context","payload":{"turn_id":"scan-state-failure","model":"gpt-test"}}`,
	)
	databasePath := filepath.Join(root, "usage.sqlite")
	st, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	installScannerAbortTrigger(t, databasePath, "scan_state", "forced UpdateScanState failure")

	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err == nil || !strings.Contains(err.Error(), "forced UpdateScanState failure") {
		t.Fatalf("UpdateScanState failure was ignored: result=%+v err=%v", result, err)
	}
}

func TestScannerContextCancellationBeforeScanStateCommitIsHardError(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"turn_context","payload":{"turn_id":"cancel-before-state","model":"gpt-test"}}`,
	)
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := (&Scanner{Store: st}).ScanWithProgress(ctx, []string{home}, false, func(progress ScanProgress) {
		if progress.FilesProcessed == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation before scan-state commit was ignored: result=%+v err=%v", result, err)
	}
}

func TestScannerMalformedRecordRemainsWarningAndContinues(t *testing.T) {
	root, home, _ := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"session_meta","payload":BROKEN}`,
		`{"timestamp":"2026-08-27T01:00:01Z","type":"session_meta","payload":{"id":"valid-after-warning","cwd":"/project"}}`,
		tokenLine("2026-08-27T01:00:02Z", usage(8, 1, 0, 2, 0, 10), usage(8, 1, 0, 2, 0, 10)),
	)
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	result, err := (&Scanner{Store: st}).Scan(context.Background(), []string{home}, false)
	if err != nil {
		t.Fatalf("malformed input should remain recoverable: %v", err)
	}
	if result.Warnings != 1 || result.EventsInserted != 1 {
		t.Fatalf("scanner did not continue after malformed input: %+v", result)
	}
}

func TestScannerVanishedRolloutRemainsWarning(t *testing.T) {
	root, home, path := writeScannerRollout(t,
		`{"timestamp":"2026-08-27T01:00:00Z","type":"turn_context","payload":{"turn_id":"vanished-rollout","model":"gpt-test"}}`,
	)
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	movedPath := path + ".moved"

	result, err := (&Scanner{Store: st}).ScanWithProgress(context.Background(), []string{home}, false, func(progress ScanProgress) {
		if progress.FilesDiscovered == 1 {
			if renameErr := os.Rename(path, movedPath); renameErr != nil && !errors.Is(renameErr, os.ErrNotExist) {
				t.Fatalf("move rollout after discovery: %v", renameErr)
			}
		}
	})
	if err != nil {
		t.Fatalf("a rollout that vanished after discovery should remain recoverable: %v", err)
	}
	if result.Warnings != 1 {
		t.Fatalf("vanished rollout was not reported once: %+v", result)
	}
}

func writeScannerRollout(t *testing.T, lines ...string) (root, home, path string) {
	t.Helper()
	root = t.TempDir()
	home = filepath.Join(root, ".codex")
	directory := filepath.Join(home, "sessions")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(directory, "scanner-error.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, home, path
}

func installScannerAbortTrigger(t *testing.T, databasePath, table, message string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statement := fmt.Sprintf(`CREATE TRIGGER fail_%s BEFORE INSERT ON %s
		BEGIN SELECT RAISE(ABORT, %q); END`, table, table, message)
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestExtendedWindowsPathPrefixNormalizesToOrdinaryPath(t *testing.T) {
	ordinary := `C:\Users\demo\.codex\sessions\one.jsonl`
	extended := `\\?\C:\Users\demo\.codex\sessions\one.jsonl`
	if pathKey(ordinary) != pathKey(extended) {
		t.Fatalf("extended path was not normalized: %q != %q", pathKey(ordinary), pathKey(extended))
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
	dbPath := filepath.Join(root, "usage.sqlite")
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
