package otel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/store"
)

func TestOTelCumulativeDeltaResetAndRetry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	receiver := NewReceiver(st)
	receiver.Now = func() time.Time { return now }

	first := otlpPayload("start-a", now, 2, map[string]float64{
		"input": 100, "cached_input": 20, "output": 20, "reasoning_output": 5, "total": 120,
	})
	result, err := receiver.Ingest(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 1 {
		t.Fatalf("first ingest: %+v", result)
	}
	retry, err := receiver.Ingest(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Events != 0 || retry.Duplicates == 0 {
		t.Fatalf("retry was not ignored: %+v", retry)
	}
	now = now.Add(time.Minute)
	second := otlpPayload("start-a", now, 2, map[string]float64{
		"input": 150, "cached_input": 30, "output": 30, "reasoning_output": 8, "total": 180,
	})
	if _, err := receiver.Ingest(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	delayed := otlpPayload("start-a", now, 2, map[string]float64{
		"input": 125, "cached_input": 25, "output": 25, "reasoning_output": 6, "total": 150,
	})
	if result, err := receiver.Ingest(context.Background(), delayed); err != nil {
		t.Fatal(err)
	} else if result.Events != 0 {
		t.Fatalf("delayed cumulative batch was counted: %+v", result)
	}
	now = now.Add(time.Minute)
	reset := otlpPayload("start-b", now, 2, map[string]float64{
		"input": 10, "cached_input": 2, "output": 5, "reasoning_output": 1, "total": 15,
	})
	if _, err := receiver.Ingest(context.Background(), reset); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 195 || summary.Usage.Input != 160 ||
		summary.Usage.CachedInput != 32 || summary.Usage.Output != 35 ||
		summary.Usage.ReasoningOutput != 9 {
		t.Fatalf("cumulative/reset mismatch: %+v", summary.Usage)
	}
}

func TestOTelDeltaTemporalityCountsSeparateBatches(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	defer st.Close()
	receiver := NewReceiver(st)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	receiver.Now = func() time.Time { return now }
	if _, err := receiver.Ingest(context.Background(), otlpPayload("s", now, 1, map[string]float64{"total": 10})); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := receiver.Ingest(context.Background(), otlpPayload("s", now, 1, map[string]float64{"total": 10})); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 20 {
		t.Fatalf("delta temporality want 20, got %d", summary.Usage.Total)
	}
}

func TestOTelCoverageReplacesConcurrentSessionJSONL(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	defer st.Close()
	at := time.Now().UTC().Truncate(time.Second)
	_, err := st.InsertEvent(context.Background(), model.UsageEvent{
		ID: "session-event", Timestamp: at, ObservedAt: at, SessionID: "s",
		ProjectPath: "/project/attribution",
		Usage:       model.TokenUsage{Total: 100}, Provenance: model.ProvenanceSessionJSONL,
		Confidence: model.ConfidenceExact,
	}, "fixture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	receiver := NewReceiver(st)
	receiver.Now = func() time.Time { return at }
	if _, err := receiver.Ingest(context.Background(), otlpPayload("s", at, 1, map[string]float64{"total": 100})); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 100 {
		t.Fatalf("OTel + JSONL were added together: %+v", summary)
	}
	filtered, _ := st.Summary(context.Background(), model.Filter{Project: "/project/attribution"})
	if filtered.Usage.Total != 100 {
		t.Fatalf("project attribution was lost under OTel coverage: %+v", filtered)
	}
}

func TestOTLPHTTPJSONAcceptsProtoStringNumbers(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	defer st.Close()
	receiver := NewReceiver(st)
	body := `{"resourceMetrics":[{"resource":{"attributes":[{"key":"model","value":{"stringValue":"gpt-5.4"}}]},"scopeMetrics":[{"metrics":[{"name":"turn.token_usage","histogram":{"aggregationTemporality":2,"dataPoints":[{"attributes":[{"key":"token_type","value":{"stringValue":"total"}}],"startTimeUnixNano":"1785398400000000000","timeUnixNano":"1785398460000000000","count":"1","sum":123}]}}]}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 123 {
		t.Fatalf("want 123, got %+v", summary)
	}
}

func TestOTelSubsetOnlyBatchDoesNotHideSessionHistory(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	defer st.Close()
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if _, err := st.InsertEvent(context.Background(), model.UsageEvent{
		ID: "session", Timestamp: at, SessionID: "s",
		Usage:      model.TokenUsage{Input: 80, CachedInput: 20, Output: 20, Total: 100},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "session.jsonl"); err != nil {
		t.Fatal(err)
	}
	receiver := NewReceiver(st)
	receiver.Now = func() time.Time { return at }
	result, err := receiver.Ingest(context.Background(),
		otlpPayload("s", at, 2, map[string]float64{"cached_input": 20}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 0 {
		t.Fatalf("subset-only batch created an event: %+v", result)
	}
	summary, err := st.Summary(context.Background(), model.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Usage.Total != 100 || summary.Usage.CachedInput != 20 {
		t.Fatalf("session history was hidden or subset doubled: %+v", summary.Usage)
	}
}

func TestReceiverOfflineRecoveryDoesNotRecountSessionGap(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	defer st.Close()
	start := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	firstReceiver := NewReceiver(st)
	firstReceiver.Now = func() time.Time { return start }
	if _, err := firstReceiver.Ingest(context.Background(),
		otlpPayload("shared-process-start", start, 2, map[string]float64{"total": 100})); err != nil {
		t.Fatal(err)
	}
	gapTime := start.Add(5 * time.Minute)
	if _, err := st.InsertEvent(context.Background(), model.UsageEvent{
		ID: "gap-session", Timestamp: gapTime, ObservedAt: gapTime,
		SessionID: "gap", Usage: model.TokenUsage{Total: 100},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "gap.jsonl"); err != nil {
		t.Fatal(err)
	}
	recoveredAt := start.Add(10 * time.Minute)
	recovered := NewReceiver(st)
	recovered.Now = func() time.Time { return recoveredAt }
	if _, err := recovered.Ingest(context.Background(),
		otlpPayload("shared-process-start", recoveredAt, 2, map[string]float64{"total": 200})); err != nil {
		t.Fatal(err)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.Usage.Total != 200 {
		t.Fatalf("offline recovery double counted session gap: %+v", summary)
	}
}

func otlpPayload(start string, at time.Time, temporality int, values map[string]float64) exportRequest {
	points := make([]histogramPoint, 0, len(values))
	for tokenType, value := range values {
		points = append(points, histogramPoint{
			Attributes:        []attribute{{Key: "token_type", Value: anyValue{StringValue: tokenType}}},
			StartTimeUnixNano: start,
			TimeUnixNano:      strconv.FormatInt(at.UnixNano(), 10),
			Count:             flexNumber{raw: "1", value: 1, set: true},
			Sum:               flexNumber{raw: strconv.FormatFloat(value, 'f', -1, 64), value: value, set: true},
		})
	}
	return exportRequest{ResourceMetrics: []resourceMetrics{{
		Resource: resource{Attributes: []attribute{
			{Key: "model", Value: anyValue{StringValue: "gpt-5.4"}},
			{Key: "originator", Value: anyValue{StringValue: "codex_cli_rs"}},
			{Key: "session_id", Value: anyValue{StringValue: "otel-session"}},
		}},
		ScopeMetrics: []scopeMetrics{{Metrics: []metric{{
			Name:      "turn.token_usage",
			Histogram: &histogram{AggregationTemporality: json.Number(strconv.Itoa(temporality)), DataPoints: points},
		}}}},
	}}}
}
