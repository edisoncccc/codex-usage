package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/pricing"
)

func BenchmarkEventExportTraversal(b *testing.B) {
	path := os.Getenv("CODEX_USAGE_BENCH_DB")
	if path == "" {
		b.Skip("set CODEX_USAGE_BENCH_DB to a disposable database copy")
	}
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	b.Run("offset-pages", func(b *testing.B) {
		for range b.N {
			count := 0
			for offset := 0; ; offset += 5000 {
				items, err := st.Events(context.Background(), EventQuery{Limit: 5000, Offset: offset})
				if err != nil {
					b.Fatal(err)
				}
				count += len(items)
				if len(items) < 5000 {
					break
				}
			}
			if count == 0 {
				b.Fatal("no export events")
			}
		}
	})
	b.Run("single-snapshot", func(b *testing.B) {
		for range b.N {
			count := 0
			if err := st.WalkEvents(context.Background(), model.Filter{}, func(model.UsageEvent) error {
				count++
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			if count == 0 {
				b.Fatal("no export events")
			}
		}
	})
}

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
	b.Run("breakdown-model-7d", func(b *testing.B) {
		for range b.N {
			if _, err := st.Breakdown(context.Background(), filter, "model", 100); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("breakdown-thread-7d", func(b *testing.B) {
		for range b.N {
			if _, err := st.Breakdown(context.Background(), filter, "thread", 100); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("sessions-7d", func(b *testing.B) {
		for range b.N {
			if _, err := st.Sessions(context.Background(), filter, 100, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestHourlyTimeseriesUsesInclusiveSinceAndExclusiveUntil(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	base := time.Date(2026, 8, 10, 10, 59, 59, 0, time.Local)
	events := []model.UsageEvent{
		{ID: "before", Timestamp: base, SessionID: "hourly", Usage: model.TokenUsage{Input: 7, Output: 3, Total: 10}},
		{ID: "start", Timestamp: base.Add(time.Second), SessionID: "hourly", Usage: model.TokenUsage{Input: 12, CachedInput: 4, Output: 8, ReasoningOutput: 3, Total: 20}},
		{ID: "inside", Timestamp: base.Add(time.Hour), SessionID: "hourly", Usage: model.TokenUsage{Input: 20, CachedInput: 5, Output: 10, ReasoningOutput: 2, Total: 30}},
		{ID: "until", Timestamp: base.Add(time.Hour + time.Second), SessionID: "hourly", Usage: model.TokenUsage{Input: 30, Output: 10, Total: 40}},
	}
	for _, event := range events {
		event.Provenance = model.ProvenanceSessionJSONL
		event.Confidence = model.ConfidenceExact
		if _, err := st.InsertEvent(ctx, event, event.ID+".jsonl"); err != nil {
			t.Fatal(err)
		}
	}

	since := time.Date(2026, 8, 10, 11, 0, 0, 0, time.Local)
	until := since.Add(time.Hour)
	filter := model.Filter{Since: since, Until: until}
	points, err := st.Timeseries(ctx, filter, "hour")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("expected one complete hour, got %#v", points)
	}
	point := points[0]
	if point.Date != "2026-08-10T11" {
		t.Fatalf("unexpected local-hour key %q", point.Date)
	}
	if point.Usage.Total != 50 || point.Usage.Input != 32 || point.Usage.Output != 18 ||
		point.Usage.CachedInput != 9 || point.Usage.ReasoningOutput != 5 {
		t.Fatalf("unexpected hourly usage: %#v", point.Usage)
	}

	summary, err := st.Summary(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if summary.GrandTotal != 50 {
		t.Fatalf("summary and hourly bucket drifted: %#v", summary)
	}
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
		// Individually invalid totals whose opposite errors cancel after SUM. They
		// must remain separate or aggregate pricing would incorrectly accept both.
		{ID: "mismatch-high", Timestamp: base.Add(2 * time.Hour), SessionID: "s5", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 10, Total: 12}},
		{ID: "mismatch-low", Timestamp: base.Add(3 * time.Hour), SessionID: "s5", Model: "gpt-5.4", Source: "desktop", ProjectPath: "/p1", Usage: model.TokenUsage{Input: 10, Total: 8}},
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
	for _, sessionID := range []string{"s1", "s2", "s3", "s4", "s5"} {
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
	if err := st.WalkSessionPricingAggregates(ctx, model.Filter{}, []string{"s1", "s2", "s3", "s4", "s5"}, func(event model.UsageEvent) error {
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

func TestWalkEventsUsesStableOrderAndPropagatesCallbackErrors(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)
	for _, event := range []model.UsageEvent{
		{ID: "b", Timestamp: at, ObservedAt: at, Model: "gpt-5.4"},
		{ID: "a", Timestamp: at, ObservedAt: at, Model: "gpt-5.4"},
		{ID: "ignored", Timestamp: at.Add(time.Hour), ObservedAt: at.Add(time.Hour), Model: "internal"},
		{ID: "c", Timestamp: at, ObservedAt: at, Model: "gpt-5.4"},
	} {
		event.Provenance = model.ProvenanceSessionJSONL
		event.Confidence = model.ConfidenceExact
		event.Usage = model.TokenUsage{Input: 8, Output: 2, Total: 10}
		if _, err := st.InsertEvent(ctx, event, event.ID+".jsonl"); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	if err := st.WalkEvents(ctx, model.Filter{Model: "gpt-5.4"}, func(event model.UsageEvent) error {
		ids = append(ids, event.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"c", "b", "a"}) {
		t.Fatalf("unstable event order: %v", ids)
	}
	sentinel := errors.New("stop export")
	if err := st.WalkEvents(ctx, model.Filter{}, func(model.UsageEvent) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("callback error was not propagated: %v", err)
	}
}

func TestSessionMetadataAdvancesRevisionOnlyWhenChanged(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info := model.SessionInfo{SessionID: "session", Title: "before", UpdatedAt: time.Unix(10, 0)}
	before := st.Revision()
	if err := st.UpsertSession(ctx, info); err != nil {
		t.Fatal(err)
	}
	afterInsert := st.Revision()
	if afterInsert <= before {
		t.Fatalf("session insert did not advance revision: before=%d after=%d", before, afterInsert)
	}
	if err := st.UpsertSession(ctx, info); err != nil {
		t.Fatal(err)
	}
	if got := st.Revision(); got != afterInsert {
		t.Fatalf("no-op session upsert advanced revision: before=%d after=%d", afterInsert, got)
	}
	info.Title = "after"
	if err := st.UpsertSession(ctx, info); err != nil {
		t.Fatal(err)
	}
	if got := st.Revision(); got <= afterInsert {
		t.Fatalf("session metadata update did not advance revision: before=%d after=%d", afterInsert, got)
	}
}

func TestCorrectEventUsageWithSessionProgressRollsBackTogether(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	initial := model.TokenUsage{Input: 80, CachedInput: 10, Output: 20, ReasoningOutput: 2, Total: 100}
	corrected := model.TokenUsage{Input: 80, CachedInput: 12, Output: 20, ReasoningOutput: 3, Total: 100}
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "atomic-correction", Timestamp: time.Now(), SessionID: "session", Usage: initial,
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "fixture.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSessionProgress(ctx, "session", 0, initial); err != nil {
		t.Fatal(err)
	}
	changed, err := st.CorrectEventUsageWithSessionProgress(
		ctx, "atomic-correction", "", 0, corrected.Sub(initial), corrected,
	)
	if err == nil || changed {
		t.Fatalf("empty session id was accepted: changed=%v err=%v", changed, err)
	}
	summary, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Usage != initial || summary.GrandTotal != initial.Total {
		t.Fatalf("empty session id changed event classification: %+v", summary)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_session_progress BEFORE INSERT ON session_cursors
		BEGIN SELECT RAISE(ABORT, 'forced session progress failure'); END`); err != nil {
		t.Fatal(err)
	}

	changed, err = st.CorrectEventUsageWithSessionProgress(
		ctx, "atomic-correction", "session", 0, corrected.Sub(initial), corrected,
	)
	if err == nil || changed {
		t.Fatalf("atomic correction did not fail as a unit: changed=%v err=%v", changed, err)
	}
	summary, err = st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	progress, segment, ok, err := st.GetSessionProgress(ctx, "session")
	if err != nil || !ok || segment != 0 {
		t.Fatalf("read rolled-back session progress: ok=%v segment=%d err=%v", ok, segment, err)
	}
	if summary.Usage != initial || summary.GrandTotal != initial.Total || progress != initial {
		t.Fatalf("atomic correction partially committed: summary=%+v progress=%+v", summary, progress)
	}

	if _, err := st.db.Exec(`DROP TRIGGER fail_session_progress`); err != nil {
		t.Fatal(err)
	}
	changed, err = st.CorrectEventUsageWithSessionProgress(
		ctx, "atomic-correction", "session", 0, corrected.Sub(initial), corrected,
	)
	if err != nil || !changed {
		t.Fatalf("atomic correction retry failed: changed=%v err=%v", changed, err)
	}
	summary, err = st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	progress, segment, ok, err = st.GetSessionProgress(ctx, "session")
	if err != nil || !ok || segment != 0 {
		t.Fatalf("read committed session progress: ok=%v segment=%d err=%v", ok, segment, err)
	}
	if summary.Usage != corrected || summary.GrandTotal != corrected.Total || progress != corrected {
		t.Fatalf("atomic correction did not commit together: summary=%+v progress=%+v", summary, progress)
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
	if version != "7" {
		t.Fatalf("schema version = %q, want 7", version)
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

func TestReadStateThreadsLabelsUntitledSubagentsFromTheirParentTask(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state_5.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		rollout_path TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		thread_source TEXT NOT NULL DEFAULT '',
		agent_role TEXT
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	rows := []struct {
		id, title, source, threadSource, role string
	}{
		{"parent", "Parent task\nLong first-message context", "vscode", "user", ""},
		{"manager", "", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"gsd-debug-session-manager"}}}`, "subagent", "gsd-debug-session-manager"},
		{"debugger", "", `{"subagent":{"thread_spawn":{"parent_thread_id":"manager","agent_role":"gsd-debugger"}}}`, "subagent", "gsd-debugger"},
		{"orphan", "", `{"subagent":{"thread_spawn":{"parent_thread_id":"missing","agent_role":"worker"}}}`, "subagent", "worker"},
		{"named", "Explicit child title", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"worker"}}}`, "subagent", "worker"},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO threads(id,rollout_path,title,source,thread_source,agent_role)
			VALUES(?,?,?,?,?,?)`, row.id, row.id+".jsonl", row.title, row.source, row.threadSource, row.role); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	items, err := ReadStateThreads(ctx, path, `C:\Users\demo\.codex`)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]model.SessionInfo, len(items))
	for _, item := range items {
		byID[item.SessionID] = item
	}
	for id, want := range map[string]string{
		"parent":   "Parent task\nLong first-message context",
		"manager":  "gsd-debug-session-manager · Parent task",
		"debugger": "gsd-debugger · Parent task",
		"orphan":   "Subagent · worker",
		"named":    "Explicit child title",
	} {
		if got := byID[id].Title; got != want {
			t.Errorf("%s title = %q, want %q", id, got, want)
		}
	}
}
