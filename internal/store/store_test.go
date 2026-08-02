package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
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

func TestV2MigrationPurgesDerivedRowsAndDropsOTelTables(t *testing.T) {
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
	if summary.GrandTotal != 0 || summary.EventCount != 0 {
		t.Fatalf("legacy derived rows survived migration: %+v", summary)
	}
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'otel_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy OTel tables remain: %d", count)
	}
}

func TestV3MigrationAddsEventSegmentAndPurgesDerivedRows(t *testing.T) {
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
	if summary.EventCount != 0 || summary.GrandTotal != 0 {
		t.Fatalf("v3 derived rows survived v4 migration: %+v", summary)
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
