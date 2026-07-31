package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
)

func TestStateFallbackDoesNotGrowAcrossOTelCoverage(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "meter.sqlite"))
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
