package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/meter"
	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/otel"
	"github.com/zJay26/codex-usage/internal/store"
)

func TestDashboardAPIAndExport(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "meter.sqlite"))
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
	scanner := &meter.Scanner{Store: st}
	srv := &Server{
		Store: st, Scanner: scanner, Receiver: otel.NewReceiver(st),
		Homes:   func() ([]string, error) { return []string{filepath.Join(root, ".codex")}, nil },
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
		"/api/v1/sessions?limit=10",
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

	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/rescan", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://evil.example")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation was not blocked: %d", response.StatusCode)
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
