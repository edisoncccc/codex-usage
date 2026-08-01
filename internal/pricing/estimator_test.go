package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
)

func TestEvaluateEventSeparatesOverlappingTokenCategories(t *testing.T) {
	event := model.UsageEvent{
		Model: "gpt-5.6-sol", Confidence: model.ConfidenceExact,
		Usage: model.TokenUsage{Input: 1000, CachedInput: 200, CacheWriteInput: 100, Output: 100, ReasoningOutput: 50, Total: 1100},
	}
	evaluated, err := evaluateEvent(event, nil)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := aggregate{}
	if err := aggregate.add(evaluated); err != nil {
		t.Fatal(err)
	}
	estimate := aggregate.estimate()
	if estimate.USD != "0.007225000" {
		t.Fatalf("unexpected estimate %s", estimate.USD)
	}
	if estimate.PricedTokens != 1100 || estimate.UnpricedTokens != 0 || estimate.CoverageRatio != 1 {
		t.Fatalf("unexpected coverage: %#v", estimate)
	}
	if estimate.RegularInputUSD != "0.003500000" || estimate.CachedInputUSD != "0.000100000" || estimate.CacheWriteInputUSD != "0.000625000" || estimate.OutputUSD != "0.003000000" {
		t.Fatalf("unexpected category estimates: %#v", estimate)
	}
}

func TestEvaluateEventAppliesLongContextMultiplierOnlyToExactEvents(t *testing.T) {
	exact := model.UsageEvent{
		Model: "gpt-5.6-sol", Confidence: model.ConfidenceExact,
		Usage: model.TokenUsage{Input: 300000, Output: 1000, Total: 301000},
	}
	evaluated, err := evaluateEvent(exact, nil)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := aggregate{}
	if err := aggregate.add(evaluated); err != nil {
		t.Fatal(err)
	}
	if got := aggregate.estimate().USD; got != "3.045000000" {
		t.Fatalf("unexpected long-context estimate %s", got)
	}

	nonExact := exact
	nonExact.Confidence = model.ConfidenceGapFallback
	evaluated, err = evaluateEvent(nonExact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if evaluated.pricedTokens != 0 || evaluated.unpricedTokens != 301000 || evaluated.reasons[0].Kind != "long_context_uncertain" {
		t.Fatalf("unexpected non-exact handling: %#v", evaluated)
	}
}

func TestEvaluateEventReportsSpecificUnpricedReasons(t *testing.T) {
	tests := []struct {
		name   string
		event  model.UsageEvent
		reason string
		priced int64
		missed int64
	}{
		{
			name:   "unknown model",
			event:  model.UsageEvent{Model: "codex-auto-review", Confidence: model.ConfidenceExact, Usage: model.TokenUsage{Input: 80, Output: 20, Total: 100}},
			reason: "unknown_model", missed: 100,
		},
		{
			name:   "total only fallback",
			event:  model.UsageEvent{Model: "gpt-5.6-sol", Confidence: model.ConfidenceAggregateOnly, Usage: model.TokenUsage{Total: 100}},
			reason: "missing_token_categories", missed: 100,
		},
		{
			name:   "contradictory fields",
			event:  model.UsageEvent{Model: "gpt-5.6-sol", Confidence: model.ConfidenceExact, Usage: model.TokenUsage{Input: 10, CachedInput: 11, Output: 2, Total: 12}},
			reason: "invalid_token_categories", missed: 12,
		},
		{
			name:   "unpublished cache write rate",
			event:  model.UsageEvent{Model: "gpt-5.5", Confidence: model.ConfidenceExact, Usage: model.TokenUsage{Input: 10, CacheWriteInput: 4, Output: 2, Total: 12}},
			reason: "cache_write_rate_missing", priced: 8, missed: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluated, err := evaluateEvent(test.event, nil)
			if err != nil {
				t.Fatal(err)
			}
			if evaluated.pricedTokens != test.priced || evaluated.unpricedTokens != test.missed || len(evaluated.reasons) != 1 || evaluated.reasons[0].Kind != test.reason {
				t.Fatalf("unexpected result: %#v", evaluated)
			}
		})
	}
}

func TestBuilderBucketsByLocalNaturalDayAndSortsModels(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("test", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })

	builder, err := NewBuilder(nil)
	if err != nil {
		t.Fatal(err)
	}
	events := []model.UsageEvent{
		{Timestamp: time.Date(2026, 7, 30, 16, 30, 0, 0, time.UTC), Model: "gpt-5.6-luna", Confidence: model.ConfidenceExact, Usage: model.TokenUsage{Input: 100, Output: 20, Total: 120}},
		{Timestamp: time.Date(2026, 7, 31, 16, 30, 0, 0, time.UTC), Model: "gpt-5.6-sol", Confidence: model.ConfidenceExact, Usage: model.TokenUsage{Input: 200, Output: 20, Total: 220}},
	}
	for _, event := range events {
		if err := builder.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	report := builder.Report()
	if len(report.Points) != 2 || report.Points[0].Date != "2026-07-31" || report.Points[1].Date != "2026-08-01" {
		t.Fatalf("unexpected local day buckets: %#v", report.Points)
	}
	if len(report.Models) != 2 || report.Models[0].Key != "gpt-5.6-sol" {
		t.Fatalf("unexpected model order: %#v", report.Models)
	}
}

func TestTokenCostRejectsOverflow(t *testing.T) {
	if _, err := tokenCost(1<<62, 5000, 1, 1); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestLargeFixedPointAccumulationRemainsExact(t *testing.T) {
	builder, err := NewBuilder(nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		if err := builder.Add(model.UsageEvent{
			Model: "gpt-5.4-mini", Confidence: model.ConfidenceExact,
			Usage: model.TokenUsage{Input: 1_000_000_000, CachedInput: 500_000_000, Output: 100_000_000, Total: 1_100_000_000},
		}); err != nil {
			t.Fatal(err)
		}
	}
	estimate := builder.Report().Summary
	if estimate.USD != "862500.000000000" || estimate.PricedTokens != 1_100_000_000_000 || estimate.CoverageRatio != 1 {
		t.Fatalf("large accumulation lost precision: %#v", estimate)
	}
}

func TestTokenCategorySumOverflowIsRejected(t *testing.T) {
	_, err := evaluateEvent(model.UsageEvent{
		Model: "gpt-5.6-sol", Confidence: model.ConfidenceExact,
		Usage: model.TokenUsage{Input: math.MaxInt64, Output: 1},
	}, nil)
	if err == nil {
		t.Fatal("expected token category overflow error")
	}
}

func TestFillDailyIncludesZeroDaysAndHonorsExclusiveUntil(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("test", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocal })

	report := Report{Points: []ReportPoint{{
		Date: "2026-07-30", Time: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC),
		Usage: model.TokenUsage{Total: 10}, Estimate: Estimate{USD: "1.000000000"},
	}}}
	filled, err := FillDaily(
		report,
		time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 1, 16, 0, 0, 0, time.UTC),
		time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(filled.Points) != 3 || filled.Points[0].Date != "2026-07-30" || filled.Points[1].Date != "2026-07-31" || filled.Points[2].Date != "2026-08-01" {
		t.Fatalf("unexpected filled points: %#v", filled.Points)
	}
	if filled.Points[1].Usage.Total != 0 || filled.Points[1].Estimate.USD != "0.000000000" {
		t.Fatalf("zero day is not empty: %#v", filled.Points[1])
	}
}
