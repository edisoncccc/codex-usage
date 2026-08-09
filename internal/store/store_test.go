package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/pricing"
)

func BenchmarkPricingEventTraversal(b *testing.B) {
	path := os.Getenv("CODEX_USAGE_BENCH_DB")
	if path == "" {
		b.Skip("set CODEX_USAGE_BENCH_DB to a disposable database copy")
	}
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	for range b.N {
		count := 0
		if err := st.WalkPricingEvents(context.Background(), model.Filter{}, func(model.UsageEvent) error {
			count++
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		if count == 0 {
			b.Fatal("no pricing events")
		}
	}
}

func BenchmarkPricingAggregateTraversal(b *testing.B) {
	path := os.Getenv("CODEX_USAGE_BENCH_DB")
	if path == "" {
		b.Skip("set CODEX_USAGE_BENCH_DB to a disposable database copy")
	}
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	for range b.N {
		count := 0
		if err := st.WalkPricingAggregates(context.Background(), model.Filter{}, func(model.UsageEvent) error {
			count++
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		if count == 0 {
			b.Fatal("no pricing aggregates")
		}
	}
}

func BenchmarkDashboardQueries(b *testing.B) {
	path := os.Getenv("CODEX_USAGE_BENCH_DB")
	if path == "" {
		b.Skip("set CODEX_USAGE_BENCH_DB to a disposable database copy")
	}
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	filter := model.Filter{SinceDate: "2026-07-28", UntilDate: "2026-08-04"}
	b.Run("summary-7d", func(b *testing.B) {
		for range b.N {
			if _, err := st.Summary(context.Background(), filter); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("timeseries-all", func(b *testing.B) {
		for range b.N {
			if _, err := st.Timeseries(context.Background(), model.Filter{}, "day"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("dimensions", func(b *testing.B) {
		for range b.N {
			if _, err := st.Dimensions(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestReadPoolRemainsAvailableWhileWriterConnectionIsBusy(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "committed", Timestamp: time.Now(), Usage: model.TokenUsage{Input: 8, Output: 2, Total: 10},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "fixture.jsonl"); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value=value WHERE key='machine'`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	summary, err := st.Summary(readCtx, model.Filter{})
	if err != nil {
		t.Fatalf("read query waited behind writer: %v", err)
	}
	if summary.GrandTotal != 10 {
		t.Fatalf("unexpected committed view: %+v", summary)
	}
}

func TestPricingAggregatesMatchRawEventsAndDimensions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	base := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	events := []model.UsageEvent{
		{ID: "a", Timestamp: base, SessionID: "s1", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 80, CachedInput: 20, Output: 20, ReasoningOutput: 5, Total: 100}},
		{ID: "b", Timestamp: base.Add(time.Hour), SessionID: "s2", Model: "gpt-5.4", Source: "cli", ProjectPath: "/p2", Usage: model.TokenUsage{Input: 40, CachedInput: 10, Output: 10, Total: 50}},
		{ID: "missing", Timestamp: base.Add(time.Hour), SessionID: "s3", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Total: 12}},
		{ID: "zero-total", Timestamp: base.Add(time.Hour), SessionID: "s3", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 3, Output: 2}},
		{ID: "invalid", Timestamp: base.Add(25 * time.Hour), SessionID: "s3", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 8, CachedInput: 9, Output: 4, Total: 12}},
		{ID: "internal", Timestamp: base.Add(24 * time.Hour), SessionID: "s4", Model: "internal", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 8, Output: 2, Total: 10}},
	}
	for _, event := range events {
		event.Provenance = model.ProvenanceSessionJSONL
		event.Confidence = model.ConfidenceExact
		if _, err := st.InsertEvent(ctx, event, event.ID+".jsonl"); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := pricing.NewBuilder(nil)
	if err := st.WalkPricingEvents(ctx, model.Filter{}, raw.Add); err != nil {
		t.Fatal(err)
	}
	aggregated, _ := pricing.NewBuilder(nil)
	if err := st.WalkPricingAggregates(ctx, model.Filter{}, aggregated.Add); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Report(), aggregated.Report()) {
		t.Fatalf("aggregate pricing drifted\nraw=%#v\naggregated=%#v", raw.Report(), aggregated.Report())
	}
	rawSessions := map[string]*pricing.Builder{}
	for _, sessionID := range []string{"s1", "s2", "s3", "s4"} {
		rawSessions[sessionID], _ = pricing.NewBuilder(nil)
	}
	if err := st.WalkPricingEvents(ctx, model.Filter{}, func(event model.UsageEvent) error {
		return rawSessions[event.SessionID].Add(event)
	}); err != nil {
		t.Fatal(err)
	}
	aggregatedSessions := map[string]*pricing.Builder{}
	for sessionID := range rawSessions {
		aggregatedSessions[sessionID], _ = pricing.NewBuilder(nil)
	}
	if err := st.WalkSessionPricingAggregates(ctx, model.Filter{}, []string{"s1", "s2", "s3", "s4"}, func(event model.UsageEvent) error {
		return aggregatedSessions[event.SessionID].Add(event)
	}); err != nil {
		t.Fatal(err)
	}
	for sessionID, rawBuilder := range rawSessions {
		if !reflect.DeepEqual(rawBuilder.Report().Summary, aggregatedSessions[sessionID].Report().Summary) {
			t.Fatalf("session %s pricing drifted\nraw=%#v\naggregated=%#v", sessionID, rawBuilder.Report().Summary, aggregatedSessions[sessionID].Report().Summary)
		}
	}
	dimensions, err := st.Dimensions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dimensions.Models, []string{"gpt-5.4", "internal"}) ||
		!reflect.DeepEqual(dimensions.Sources, []string{"cli", "desktop"}) ||
		!reflect.DeepEqual(dimensions.Projects, []string{"/p1", "/p2"}) {
		t.Fatalf("unexpected dimensions: %#v", dimensions)
	}
	sessions, err := st.Sessions(ctx, model.Filter{Search: "p2"}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "s2" {
		t.Fatalf("session search mismatch: %#v", sessions)
	}
}

func TestWarningsAreGroupedByKindAndPath(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, detail := range []string{"第一次", "第二次", "第三次"} {
		if err := st.AddWarning(ctx, "cumulative_reset", "same.jsonl", detail); err != nil {
			t.Fatal(err)
		}
	}
	warnings, err := st.Warnings(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Occurrences != 3 || warnings[0].Detail != "第三次" {
		t.Fatalf("warning grouping mismatch: %+v", warnings)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.WarningCount != 1 || status.AccountingMode != "jsonl_only" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestV2MigrationPreservesDerivedRowsUntilApprovedRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "old-json", Timestamp: at, Usage: model.TokenUsage{Input: 8, Output: 2, Total: 10},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "old.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value='2' WHERE key='schema_version';
		CREATE TABLE otel_series(series_key TEXT PRIMARY KEY);
		CREATE TABLE otel_coverage(run_id TEXT PRIMARY KEY,started_at INTEGER,last_at INTEGER);
		INSERT INTO otel_coverage VALUES('legacy',1,2);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	summary, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GrandTotal != 10 || summary.EventCount != 1 {
		t.Fatalf("legacy derived rows were changed before approval: %+v", summary)
	}
	if reason, pending, err := st.HistoricalRebuildReason(ctx); err != nil || !pending || reason == "" {
		t.Fatalf("migration did not request an approved rebuild: pending=%v reason=%q err=%v", pending, reason, err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'otel_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("legacy tables were removed before approval: %d", count)
	}
	if err := st.ResetHistorical(ctx); err != nil {
		t.Fatal(err)
	}
	summary, _ = st.Summary(ctx, model.Filter{})
	if summary.GrandTotal != 0 || summary.EventCount != 0 {
		t.Fatalf("approved rebuild did not clear derived rows: %+v", summary)
	}
	if _, pending, err := st.HistoricalRebuildReason(ctx); err != nil || pending {
		t.Fatalf("approved rebuild left pending marker: pending=%v err=%v", pending, err)
	}
}

func TestV3MigrationAddsEventSegmentAndWaitsForApproval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "v3-derived", Timestamp: time.Now(), SessionID: "session", Segment: 2,
		Usage:      model.TokenUsage{Input: 8, Output: 2, Total: 10},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "old.jsonl"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value='3' WHERE key='schema_version';
		ALTER TABLE usage_events DROP COLUMN segment;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	summary, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventCount != 1 || summary.GrandTotal != 10 {
		t.Fatalf("v3 derived rows were changed before approval: %+v", summary)
	}
	if reason, pending, err := st.HistoricalRebuildReason(ctx); err != nil || !pending || reason == "" {
		t.Fatalf("migration did not request an approved rebuild: pending=%v reason=%q err=%v", pending, reason, err)
	}
	var segmentColumns int
	rows, err := st.db.Query(`PRAGMA table_info(usage_events)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if name == "segment" {
			segmentColumns++
		}
	}
	rows.Close()
	if segmentColumns != 1 {
		t.Fatalf("segment column count = %d", segmentColumns)
	}
}

func TestV4MigrationPreservesHistoryUntilSingleMetadataRebuildApproved(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "v4-derived", Timestamp: time.Now(), SessionID: "single-meta-fork",
		Usage:      model.TokenUsage{Input: 8, Output: 2, Total: 10},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "single-meta-fork.jsonl"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.PutCursor(ctx, FileCursor{
		Path: "single-meta-fork.jsonl", SessionID: "single-meta-fork", ForkedFromID: "parent",
		Cumulative: model.TokenUsage{Input: 8, Output: 2, Total: 10},
	}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value='4' WHERE key='schema_version'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	summary, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventCount != 1 || summary.GrandTotal != 10 {
		t.Fatalf("v4 derived rows were changed before approval: %+v", summary)
	}
	if _, ok, err := st.GetCursor(ctx, "single-meta-fork.jsonl"); err != nil || !ok {
		t.Fatalf("v4 cursor was removed before approval: ok=%v err=%v", ok, err)
	}
	if reason, pending, err := st.HistoricalRebuildReason(ctx); err != nil || !pending || reason == "" {
		t.Fatalf("migration did not request an approved rebuild: pending=%v reason=%q err=%v", pending, reason, err)
	}
	var version string
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "5" {
		t.Fatalf("schema version = %q, want 5", version)
	}
	if err := st.ResetHistorical(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetCursor(ctx, "single-meta-fork.jsonl"); err != nil || ok {
		t.Fatalf("approved rebuild did not clear v4 cursor: ok=%v err=%v", ok, err)
	}
}

func TestCanonicalViewsUseOnlyJSONL(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	for _, event := range []model.UsageEvent{
		{ID: "json", Timestamp: at, SessionID: "session", Usage: model.TokenUsage{Input: 80, Output: 20, Total: 100}, Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact},
		{ID: "legacy-otel", Timestamp: at, Usage: model.TokenUsage{Input: 120, Output: 30, Total: 150}, Provenance: "otel", Confidence: model.ConfidenceExact},
		{ID: "legacy-state", Usage: model.TokenUsage{Total: 90}, Provenance: "state_fallback", Confidence: model.ConfidenceAggregateOnly},
	} {
		if _, err := st.InsertEvent(ctx, event, event.ID); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Usage.Total != 100 || summary.GrandTotal != 100 || summary.Unattributed.Total != 0 {
		t.Fatalf("non-JSONL provenance affected totals: %+v", summary)
	}
}

func TestTimeseriesKeepsIngestionLocalDateAfterTimezoneChange(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 7, 30, 16, 1, 0, 0, time.UTC)
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "event", Timestamp: at, Usage: model.TokenUsage{Input: 8, Output: 2, Total: 10},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "fixture.jsonl"); err != nil {
		t.Fatal(err)
	}
	time.Local = time.FixedZone("UTC-7", -7*60*60)
	points, err := st.Timeseries(ctx, model.Filter{SinceDate: "2026-07-31", UntilDate: "2026-08-01"}, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Date != "2026-07-31" || points[0].Usage.Total != 10 {
		t.Fatalf("stored local day drifted with query timezone: %+v", points)
	}
}
