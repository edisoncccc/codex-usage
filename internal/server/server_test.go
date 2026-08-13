package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/pricing"
	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
)

func TestDashboardAPIAndExport(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := st.InsertEvent(context.Background(), model.UsageEvent{
		ID: "fixture", Timestamp: at, ObservedAt: at, SessionID: "session-1",
		Model: "gpt-5.4", Source: "codex_desktop", AgentType: "main",
		ProjectPath: `C:\work\private-project`, ThreadTitle: "本机测试线程",
		Usage: model.TokenUsage{
			Input: 80, CachedInput: 20, Output: 20, ReasoningOutput: 5, Total: 100,
		},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "fixture.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertEvent(context.Background(), model.UsageEvent{
		ID: "unpriced-fixture", Timestamp: at.Add(time.Minute), ObservedAt: at.Add(time.Minute), SessionID: "session-2",
		Model: "codex-auto-review", Source: "codex_desktop", AgentType: "main",
		Usage:      model.TokenUsage{Input: 10, Output: 10, Total: 20},
		Provenance: model.ProvenanceSessionJSONL, Confidence: model.ConfidenceExact,
	}, "unpriced-fixture.jsonl"); err != nil {
		t.Fatal(err)
	}
	scanner := &usage.Scanner{Store: st}
	savedOverrides := map[string]pricing.Override{}
	srv := &Server{
		Store: st, Scanner: scanner,
		Homes: func() ([]string, error) { return []string{filepath.Join(root, ".codex")}, nil },
		LoadPricingOverrides: func() (map[string]pricing.Override, error) {
			return savedOverrides, nil
		},
		SavePricingOverrides: func(value map[string]pricing.Override) error {
			savedOverrides = value
			return nil
		},
		Address: "127.0.0.1", Port: 43189, Version: "test",
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "Codex Usage") ||
		response.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("dashboard response invalid: status=%d headers=%v body=%s", response.StatusCode, response.Header, body)
	}
	if strings.Contains(string(body), "cdn.") {
		t.Fatal("dashboard unexpectedly references a CDN")
	}

	for _, path := range []string{
		"/api/v1/status",
		"/api/v1/summary?since=7d",
		"/api/v1/timeseries?since=7d&bucket=hour",
		"/api/v1/breakdown?dimension=model",
		"/api/v1/dimensions",
		"/api/v1/sessions?limit=10",
		"/api/v1/sessions?limit=10&include_estimate=0",
		"/api/v1/session-estimates?limit=10",
		"/api/v1/cost-estimate?bucket=day",
		"/api/v1/pricing",
		"/api/v1/export?format=json",
		"/api/v1/export?format=csv",
	} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.StatusCode, payload)
		}
		if len(payload) == 0 {
			t.Fatalf("%s returned an empty body", path)
		}
	}
	response, err = http.Get(httpServer.URL + "/api/v1/not-a-route")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API route status=%d", response.StatusCode)
	}
	response, err = http.Post(httpServer.URL+"/v1/metrics", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("removed metrics receiver status=%d want 404", response.StatusCode)
	}
	response, err = http.Get(httpServer.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	var statusPayload struct {
		Status store.Status `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&statusPayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if statusPayload.Status.AccountingMode != "jsonl_only" {
		t.Fatalf("accounting mode=%q", statusPayload.Status.AccountingMode)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/cost-estimate?bucket=day")
	if err != nil {
		t.Fatal(err)
	}
	var initialCost pricing.Report
	if err := json.NewDecoder(response.Body).Decode(&initialCost); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if initialCost.Summary.PricedTokens != 100 || initialCost.Summary.UnpricedTokens != 20 || initialCost.Summary.USD != "0.000455000" {
		t.Fatalf("unexpected initial cost estimate: %#v", initialCost.Summary)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/sessions?limit=10&q=private-project")
	if err != nil {
		t.Fatal(err)
	}
	var sessionPayload struct {
		Items []struct {
			SessionID string           `json:"session_id"`
			Estimate  pricing.Estimate `json:"estimate"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&sessionPayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(sessionPayload.Items) != 1 || sessionPayload.Items[0].SessionID != "session-1" ||
		sessionPayload.Items[0].Estimate.PricedTokens != 100 || sessionPayload.Items[0].Estimate.USD != "0.000455000" {
		t.Fatalf("unexpected searched session estimate: %#v", sessionPayload.Items)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/sessions?limit=10&q=private-project&include_estimate=0")
	if err != nil {
		t.Fatal(err)
	}
	var baseSessionPayload struct {
		Items []struct {
			SessionID string          `json:"session_id"`
			Estimate  json.RawMessage `json:"estimate"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&baseSessionPayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(baseSessionPayload.Items) != 1 || baseSessionPayload.Items[0].SessionID != "session-1" || baseSessionPayload.Items[0].Estimate != nil {
		t.Fatalf("base sessions unexpectedly included pricing: %#v", baseSessionPayload.Items)
	}
	if timing := response.Header.Get("Server-Timing"); !strings.Contains(timing, `sessions;`) || !strings.Contains(timing, `desc="hit"`) {
		t.Fatalf("base sessions timing did not report a cache hit: %q", timing)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/session-estimates?limit=10&q=private-project")
	if err != nil {
		t.Fatal(err)
	}
	var estimatePayload struct {
		Items []sessionEstimateResponseItem `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&estimatePayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(estimatePayload.Items) != 1 || estimatePayload.Items[0].SessionID != "session-1" || estimatePayload.Items[0].Estimate.PricedTokens != 100 {
		t.Fatalf("unexpected deferred session estimate: %#v", estimatePayload.Items)
	}
	if timing := response.Header.Get("Server-Timing"); !strings.Contains(timing, `pricing;`) || !strings.Contains(timing, `desc="hit"`) {
		t.Fatalf("deferred estimate timing did not report a cache hit: %q", timing)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/session-estimates?limit=10&session_id=session-2")
	if err != nil {
		t.Fatal(err)
	}
	var unpricedSessionPayload struct {
		Items []sessionEstimateResponseItem `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&unpricedSessionPayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(unpricedSessionPayload.Items) != 1 || unpricedSessionPayload.Items[0].Estimate.UnpricedTokens != 20 {
		t.Fatalf("unexpected unpriced session estimate: %#v", unpricedSessionPayload.Items)
	}

	request, _ := http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/pricing/overrides", strings.NewReader(
		`{"overrides":{"codex-auto-review":{"alias_of":"gpt-5.6-luna"}}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", httpServer.URL)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || savedOverrides["codex-auto-review"].AliasOf != "gpt-5.6-luna" {
		t.Fatalf("pricing override was not saved: status=%d body=%s saved=%#v", response.StatusCode, payload, savedOverrides)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/cost-estimate?bucket=day")
	if err != nil {
		t.Fatal(err)
	}
	var overriddenCost pricing.Report
	if err := json.NewDecoder(response.Body).Decode(&overriddenCost); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if overriddenCost.Summary.PricedTokens != 120 || overriddenCost.Summary.UnpricedTokens != 0 || overriddenCost.Summary.CoverageRatio != 1 {
		t.Fatalf("override was not applied without restart: %#v", overriddenCost.Summary)
	}

	response, err = http.Get(httpServer.URL + "/api/v1/session-estimates?limit=10&session_id=session-2")
	if err != nil {
		t.Fatal(err)
	}
	var overriddenSessionPayload struct {
		Items []sessionEstimateResponseItem `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&overriddenSessionPayload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(overriddenSessionPayload.Items) != 1 || overriddenSessionPayload.Items[0].Estimate.PricedTokens != 20 || overriddenSessionPayload.Items[0].Estimate.UnpricedTokens != 0 {
		t.Fatalf("pricing override did not invalidate session estimate cache: %#v", overriddenSessionPayload.Items)
	}

	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/rescan", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://evil.example")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation was not blocked: %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/pricing/overrides", strings.NewReader(`{"overrides":{}}`))
	request.Header.Set("Origin", "https://evil.example")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin pricing update was not blocked: %d", response.StatusCode)
	}
}

func TestSafeOriginRequiresSameLoopbackOrigin(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		origin       string
		secFetchSite string
		allowed      bool
	}{
		{name: "same IPv4 origin", target: "http://127.0.0.1:43189/api/v1/rescan", origin: "http://127.0.0.1:43189", allowed: true},
		{name: "same localhost origin", target: "http://localhost:43189/api/v1/rescan", origin: "http://localhost:43189", allowed: true},
		{name: "different loopback port", target: "http://127.0.0.1:43189/api/v1/rescan", origin: "http://127.0.0.1:43200"},
		{name: "HTTPS origin for HTTP service", target: "http://127.0.0.1:43189/api/v1/rescan", origin: "https://127.0.0.1:43189"},
		{name: "non-loopback origin", target: "http://127.0.0.1:43189/api/v1/rescan", origin: "http://example.test:43189"},
		{name: "origin with path", target: "http://127.0.0.1:43189/api/v1/rescan", origin: "http://127.0.0.1:43189/path"},
		{name: "non-browser client", target: "http://127.0.0.1:43189/api/v1/rescan", allowed: true},
		{name: "cross-site request without origin", target: "http://127.0.0.1:43189/api/v1/rescan", secFetchSite: "cross-site"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.secFetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.secFetchSite)
			}
			if got := safeOrigin(request); got != test.allowed {
				t.Fatalf("safeOrigin()=%v, want %v", got, test.allowed)
			}
		})
	}
}

func TestRescanRequiresExplicitRebuildApproval(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".codex")
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval.jsonl")
	meta := `{"timestamp":"2026-08-09T01:00:00Z","type":"session_meta","payload":{"id":"approval-session","cwd":"C:\\work"}}` + "\n"
	tokens := `{"timestamp":"2026-08-09T01:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"output_tokens":20,"total_tokens":100},"last_token_usage":{"input_tokens":80,"output_tokens":20,"total_tokens":100}}}}` + "\n"
	if err := os.WriteFile(path, []byte(meta+tokens), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scanner := &usage.Scanner{Store: st}
	if _, err := scanner.Scan(context.Background(), []string{home}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: st, Scanner: scanner, Homes: func() ([]string, error) { return []string{home}, nil }}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	response, err := http.Post(httpServer.URL+"/api/v1/rescan", "application/json", strings.NewReader(`{"rebuild":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var conflict map[string]any
	if err := json.NewDecoder(response.Body).Decode(&conflict); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || conflict["rebuild_required"] != true {
		t.Fatalf("rescan did not request approval: status=%d payload=%#v", response.StatusCode, conflict)
	}
	summary, _ := st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 100 {
		t.Fatalf("history changed before approval: %+v", summary)
	}

	response, err = http.Post(httpServer.URL+"/api/v1/rescan", "application/json", strings.NewReader(`{"rebuild":true}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approved rebuild failed: status=%d", response.StatusCode)
	}
	summary, _ = st.Summary(context.Background(), model.Filter{})
	if summary.GrandTotal != 0 {
		t.Fatalf("approved rebuild did not use current JSONL: %+v", summary)
	}
}

func TestPricingOverrideValidationAndBodyLimit(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{
		Store:                st,
		SavePricingOverrides: func(map[string]pricing.Override) error { return nil },
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	for _, body := range []string{
		`{"overrides":{"internal":{"input_usd_per_million":"-1","cached_input_usd_per_million":"0.1","cache_write_input_usd_per_million":"1","output_usd_per_million":"1"}}}`,
		`{"overrides":{"x":{"alias_of":"y"}}}`,
		`{"wrong":{}}`,
	} {
		request, _ := http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/pricing/overrides", strings.NewReader(body))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid pricing body was accepted: status=%d body=%s", response.StatusCode, body)
		}
	}
	oversized := `{"overrides":{},"padding":"` + strings.Repeat("x", 70<<10) + `"}`
	request, _ := http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/pricing/overrides", strings.NewReader(oversized))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized pricing body status=%d", response.StatusCode)
	}
}

func TestParseSince(t *testing.T) {
	for _, value := range []string{"today", "7d", "2w", "12h", "2026-07-30", "2026-07-30T01:02:03Z"} {
		if _, err := ParseSince(value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	if _, err := ParseSince("banana"); err == nil {
		t.Fatal("expected invalid since error")
	}
}

func TestParseFilterDateUsesOneLocalNaturalDay(t *testing.T) {
	filter, err := parseFilter(url.Values{"date": {"2026-07-30"}})
	if err != nil {
		t.Fatal(err)
	}
	if filter.SinceDate != "2026-07-30" || filter.UntilDate != "2026-07-31" {
		t.Fatalf("unexpected local date bounds: since=%q until=%q", filter.SinceDate, filter.UntilDate)
	}
	if filter.Since.In(time.Local).Format("2006-01-02") != "2026-07-30" ||
		filter.Until.In(time.Local).Format("2006-01-02") != "2026-07-31" {
		t.Fatalf("unexpected local time interval: since=%v until=%v", filter.Since, filter.Until)
	}

	filter, err = parseFilter(url.Values{
		"date":  {"2026-07-30"},
		"since": {"2026-07-30T12:00:00+08:00"},
		"until": {"2026-07-30T13:00:00+08:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filter.SinceDate != "" || filter.UntilDate != "" || filter.Until.Sub(filter.Since) != time.Hour {
		t.Fatalf("date did not preserve the narrower absolute interval: %#v", filter)
	}

	if _, err := parseFilter(url.Values{"date": {"2026-07-40"}}); err == nil {
		t.Fatal("expected invalid date error")
	}
}

func TestCSVTextNeutralizesSpreadsheetFormulas(t *testing.T) {
	for _, value := range []string{"=1+1", "+cmd", "-2+3", "@SUM(A1:A2)", "\tformula"} {
		if got := csvText(value); got != "'"+value {
			t.Fatalf("csvText(%q)=%q", value, got)
		}
	}
	if got := csvText(`C:\work\project`); got != `C:\work\project` {
		t.Fatalf("ordinary path changed: %q", got)
	}
}
