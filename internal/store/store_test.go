package store

import (
	"context"
	"database/sql"
	"fmt"
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
	ctx := context.Background()
	b.Run("stream", func(b *testing.B) {
		for range b.N {
			count := 0
			if err := st.WalkPricingEvents(ctx, model.Filter{}, func(model.UsageEvent) error {
				count++
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			if count == 0 {
				b.Fatal("no pricing events")
			}
		}
	})
	b.Run("paged", func(b *testing.B) {
		for range b.N {
			count := 0
			for offset := 0; ; offset += 5000 {
				items, err := st.PricingEvents(ctx, EventQuery{Limit: 5000, Offset: offset})
				if err != nil {
					b.Fatal(err)
				}
				count += len(items)
				if len(items) < 5000 {
					break
				}
			}
			if count == 0 {
				b.Fatal("no pricing events")
			}
		}
	})
}

func TestWarningsAreGroupedByKindAndPath(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, detail := range []string{"第一次", "第二次", "第三次"} {
		if err := st.AddWarning(ctx, "state_fallback_suppressed_otel", "same.jsonl", detail); err != nil {
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
	if status.WarningCount != 0 {
		t.Fatalf("informational de-dup warning counted as actionable: %+v", status)
	}
	if err := st.AddWarning(ctx, "cumulative_reset", "same.jsonl", "需要复核"); err != nil {
		t.Fatal(err)
	}
	status, _ = st.Status(ctx)
	if status.WarningCount != 1 {
		t.Fatalf("actionable warning count=%d want 1", status.WarningCount)
	}
}

func TestWarningV1MigrationCollapsesHistoricalNoise(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
		INSERT INTO meta VALUES('schema_version','1');
		CREATE TABLE warnings(
			id INTEGER PRIMARY KEY AUTOINCREMENT,created_at INTEGER NOT NULL,
			kind TEXT NOT NULL,path TEXT NOT NULL DEFAULT '',detail TEXT NOT NULL,
			fingerprint TEXT NOT NULL UNIQUE);
		INSERT INTO warnings(created_at,kind,path,detail,fingerprint) VALUES
			(10,'state_fallback_suppressed_otel','same.jsonl','旧差额','a'),
			(20,'state_fallback_suppressed_otel','same.jsonl','新差额','b');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	warnings, err := st.Warnings(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Occurrences != 2 || warnings[0].Detail != "新差额" || warnings[0].FirstSeen.Unix() != 10 {
		t.Fatalf("v1 warning migration mismatch: %+v", warnings)
	}
}

func TestStateFallbackDoesNotGrowAcrossOTelCoverage(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	session := model.SessionInfo{
		SessionID: "session-a", RolloutPath: "fixture.jsonl", CodexHome: "/codex",
		TokensUsed: 150, UpdatedAt: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC),
	}
	if _, err := st.InsertEvent(ctx, model.UsageEvent{
		ID: "json-a", Timestamp: session.UpdatedAt, SessionID: session.SessionID,
		Usage: model.TokenUsage{Total: 100}, Provenance: model.ProvenanceSessionJSONL,
		Confidence: model.ConfidenceExact,
	}, session.RolloutPath); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyStateFallback(ctx, session); err != nil {
		t.Fatal(err)
	}
	before, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Unattributed.Total != 50 {
		t.Fatalf("initial fallback=%d want 50", before.Unattributed.Total)
	}

	coverageStart := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	if err := st.TouchCoverageInterval(ctx, "run", coverageStart, coverageStart.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	session.TokensUsed = 250
	session.UpdatedAt = coverageStart.Add(time.Minute)
	if err := st.ApplyStateFallback(ctx, session); err != nil {
		t.Fatal(err)
	}
	after, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Unattributed.Total != 50 {
		t.Fatalf("OTel-overlapping fallback grew to %d", after.Unattributed.Total)
	}

	newSession := model.SessionInfo{
		SessionID: "session-new", RolloutPath: "new.jsonl", CodexHome: "/codex",
		TokensUsed: 80, UpdatedAt: coverageStart.Add(time.Minute),
	}
	if err := st.ApplyStateFallback(ctx, newSession); err != nil {
		t.Fatal(err)
	}
	final, err := st.Summary(ctx, model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if final.Unattributed.Total != 50 {
		t.Fatalf("new OTel-overlapping fallback was counted: %d", final.Unattributed.Total)
	}
	warnings, err := st.Warnings(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range warnings {
		found = found || warning.Kind == "state_fallback_suppressed_otel"
	}
	if !found {
		t.Fatal("suppressed fallback was not made visible")
	}
}

func TestPricingEventsPreserveCanonicalTotalsAndJSONLAttribution(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	if err := st.TouchCoverageInterval(ctx, "run", at.Add(-time.Minute), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, event := range []model.UsageEvent{
		{
			ID: "json", Timestamp: at, ObservedAt: at, SessionID: "session", Model: "gpt-5.6-luna", ProjectPath: "/work/project",
			Usage: model.TokenUsage{Input: 80, Output: 20, Total: 100}, Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
		},
		{
			ID: "otel", Timestamp: at, ObservedAt: at, Model: "gpt-5.6-luna",
			Usage: model.TokenUsage{Input: 120, Output: 30, Total: 150}, Provenance: model.ProvenanceOTel, Confidence: model.ConfidenceExact,
		},
	} {
		if _, err := st.InsertEvent(ctx, event, event.ID); err != nil {
			t.Fatal(err)
		}
	}
	machineView, err := st.PricingEvents(ctx, EventQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(machineView) != 1 || machineView[0].ID != "otel" {
		t.Fatalf("canonical pricing view double counted sources: %#v", machineView)
	}
	projectView, err := st.PricingEvents(ctx, EventQuery{Filter: model.Filter{Project: "/work/project"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectView) != 1 || projectView[0].ID != "json" {
		t.Fatalf("project pricing view lost JSONL attribution: %#v", projectView)
	}
}

func TestTimeseriesUsesLocalNaturalDaysAndExclusiveUntil(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("test", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for index, at := range []time.Time{
		time.Date(2026, 7, 30, 15, 59, 0, 0, time.UTC),
		time.Date(2026, 7, 30, 16, 1, 0, 0, time.UTC),
	} {
		if _, err := st.InsertEvent(ctx, model.UsageEvent{
			ID: fmt.Sprintf("event-%d", index), Timestamp: at, ObservedAt: at,
			Usage: model.TokenUsage{Input: 10, Total: 10}, Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
		}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Date(2026, 7, 31, 0, 0, 0, 0, time.Local)
	points, err := st.Timeseries(ctx, model.Filter{Until: until}, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Usage.Total != 10 || points[0].Time.In(time.Local).Day() != 30 {
		t.Fatalf("exclusive local-day boundary failed: %#v", points)
	}
}
