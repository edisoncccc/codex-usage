package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventEmitterWritesOneJSONObjectPerLine(t *testing.T) {
	var output bytes.Buffer
	now := time.Date(2026, time.August, 26, 1, 2, 3, 456000000, time.FixedZone("test", 8*60*60))
	emitter := newEventEmitter(&output, true, func() time.Time { return now })

	if err := emitter.Progress("scan", "running", "scan_started", "", map[string]int{"files": 1}); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Progress("scan", "running", "scan_progress", "", map[string]int{"files": 2}); err != nil {
		t.Fatal(err)
	}

	events := decodeMachineEvents(t, output.String())
	if len(events) != 2 {
		t.Fatalf("events=%d output=%q", len(events), output.String())
	}
	for index, event := range events {
		assertMachineEventEnvelope(t, event)
		if event["event"] != "progress" || event["phase"] != "scan" || event["status"] != "running" {
			t.Fatalf("event %d has unstable envelope: %#v", index, event)
		}
	}
}

func TestStructuredRunnerEmitsExactlyOneTerminalEvent(t *testing.T) {
	now := time.Date(2026, time.August, 26, 5, 6, 7, 0, time.UTC)
	tests := []struct {
		name      string
		handler   structuredHandler
		wantExit  int
		wantEvent string
		wantCode  string
	}{
		{
			name: "success",
			handler: func(_ []string, emitter *eventEmitter) (commandResult, error) {
				if err := emitter.Progress("test", "running", "working", "", map[string]int{"steps": 1}); err != nil {
					return commandResult{}, err
				}
				return commandResult{Code: "test_complete", Data: map[string]bool{"ok": true}}, nil
			},
			wantExit:  0,
			wantEvent: "result",
			wantCode:  "test_complete",
		},
		{
			name: "coded error",
			handler: func(_ []string, emitter *eventEmitter) (commandResult, error) {
				if err := emitter.Progress("test", "running", "working", "", nil); err != nil {
					return commandResult{}, err
				}
				return commandResult{}, &codedError{
					Code:     "test_failed",
					ExitCode: 7,
					Err:      errors.New("expected failure"),
					Details:  map[string]string{"reason": "fixture"},
				}
			},
			wantExit:  7,
			wantEvent: "error",
			wantCode:  "test_failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cli := CLI{Stdout: &stdout, Stderr: &stderr, Now: func() time.Time { return now }}
			if exitCode := cli.runStructured("test", []string{"--json"}, test.handler); exitCode != test.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%q", exitCode, test.wantExit, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("structured stderr must stay empty, got %q", stderr.String())
			}

			events := decodeMachineEvents(t, stdout.String())
			terminal := 0
			for _, event := range events {
				assertMachineEventEnvelope(t, event)
				switch event["event"] {
				case "result", "error":
					terminal++
					if event["event"] != test.wantEvent || event["code"] != test.wantCode {
						t.Fatalf("terminal event=%#v", event)
					}
				case "progress":
				default:
					t.Fatalf("unsupported event kind: %#v", event)
				}
			}
			if terminal != 1 {
				t.Fatalf("terminal events=%d output=%q", terminal, stdout.String())
			}
		})
	}
}

func TestStructuredRunnerFallsBackFromUnencodableTerminalPayload(t *testing.T) {
	tests := []struct {
		name    string
		handler structuredHandler
	}{
		{
			name: "result",
			handler: func(_ []string, _ *eventEmitter) (commandResult, error) {
				return commandResult{Code: "bad_result", Data: func() {}}, nil
			},
		},
		{
			name: "error details",
			handler: func(_ []string, _ *eventEmitter) (commandResult, error) {
				return commandResult{}, &codedError{
					Code:     "bad_details",
					ExitCode: 7,
					Err:      errors.New("expected handler failure"),
					Details:  func() {},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := (CLI{Stdout: &stdout, Stderr: &stderr}).runStructured(
				"test", []string{"--json"}, test.handler,
			)
			if exitCode != 1 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
			}
			events := decodeMachineEvents(t, stdout.String())
			if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "internal_error" {
				t.Fatalf("fallback events=%#v", events)
			}
			if _, exists := events[0]["result"]; exists {
				t.Fatalf("fallback must not retain the original payload: %#v", events[0])
			}
		})
	}
}

func TestStructuredRunnerRecoversFromWriteFailureBeforeAnyBytes(t *testing.T) {
	w := &failOnceWriter{remainingFailures: 1}
	var stderr bytes.Buffer
	exitCode := (CLI{Stdout: w, Stderr: &stderr}).runStructured(
		"test",
		[]string{"--json"},
		func(_ []string, _ *eventEmitter) (commandResult, error) {
			return commandResult{Code: "result", Data: map[string]bool{"ok": true}}, nil
		},
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	events := decodeMachineEvents(t, w.output.String())
	if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "internal_error" {
		t.Fatalf("fallback events=%#v", events)
	}
}

func TestStructuredRunnerDoesNotAppendAfterPartialProgressWrite(t *testing.T) {
	w := &partialErrorWriter{limit: 12, failNext: true}
	var stderr bytes.Buffer
	var captured *eventEmitter
	exitCode := (CLI{Stdout: w, Stderr: &stderr}).runStructured(
		"scan",
		[]string{"--json"},
		func(_ []string, emitter *eventEmitter) (commandResult, error) {
			captured = emitter
			err := emitter.Progress("scan", "running", "scan_progress", "", map[string]int{"files": 1})
			if err == nil {
				return commandResult{}, errors.New("expected partial writer failure")
			}
			return commandResult{}, err
		},
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	if got := w.output.String(); got != w.failedPrefix {
		t.Fatalf("stream received bytes after corrupt prefix: got=%q prefix=%q", got, w.failedPrefix)
	}
	if strings.Contains(w.output.String(), "\n") {
		t.Fatalf("corrupt stream must not claim a terminal line: %q", w.output.String())
	}
	before := w.output.String()
	if err := captured.Progress("scan", "running", "late", "", nil); err == nil {
		t.Fatal("expected poisoned emitter to reject later progress")
	}
	if got := w.output.String(); got != before {
		t.Fatalf("poisoned emitter appended bytes: before=%q after=%q", before, got)
	}
}

func TestStructuredRunnerCommitsTerminalAfterFullWriteError(t *testing.T) {
	w := &fullWriteErrorWriter{failNext: true}
	var stderr bytes.Buffer
	var captured *eventEmitter
	exitCode := (CLI{Stdout: w, Stderr: &stderr}).runStructured(
		"test",
		[]string{"--json"},
		func(_ []string, emitter *eventEmitter) (commandResult, error) {
			captured = emitter
			return commandResult{Code: "complete", Data: map[string]bool{"ok": true}}, nil
		},
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	events := decodeMachineEvents(t, w.output.String())
	if len(events) != 1 || events[0]["event"] != "result" || events[0]["code"] != "complete" {
		t.Fatalf("events=%#v", events)
	}
	if !captured.terminal {
		t.Fatal("complete terminal line was written but terminal state was not committed")
	}
	before := w.output.String()
	if err := captured.Progress("test", "running", "late", "", nil); err == nil {
		t.Fatal("expected progress after complete terminal write to fail")
	}
	if err := captured.finish("error", "test", "failed", "late", "", nil); err == nil {
		t.Fatal("expected second terminal after complete terminal write to fail")
	}
	if got := w.output.String(); got != before {
		t.Fatalf("terminal guard appended bytes: before=%q after=%q", before, got)
	}
}

func TestEventEmitterConcurrentWritesDoNotInterleave(t *testing.T) {
	var output bytes.Buffer
	emitter := newEventEmitter(&output, true, time.Now)
	const writers = 32
	var wait sync.WaitGroup
	wait.Add(writers)
	for index := 0; index < writers; index++ {
		go func(index int) {
			defer wait.Done()
			if err := emitter.Progress("scan", "running", "progress", "", map[string]int{"index": index}); err != nil {
				t.Errorf("Progress(%d): %v", index, err)
			}
		}(index)
	}
	wait.Wait()
	events := decodeMachineEvents(t, output.String())
	if len(events) != writers {
		t.Fatalf("events=%d want=%d", len(events), writers)
	}
}

func TestEventEmitterRejectsWritesAfterTerminal(t *testing.T) {
	var output bytes.Buffer
	emitter := newEventEmitter(&output, true, time.Now)
	if err := emitter.finish("result", "test", "success", "done", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Progress("test", "running", "late", "", nil); err == nil {
		t.Fatal("expected progress after terminal to fail")
	}
	if err := emitter.finish("error", "test", "failed", "late", "", nil); err == nil {
		t.Fatal("expected a second terminal event to fail")
	}
	if events := decodeMachineEvents(t, output.String()); len(events) != 1 {
		t.Fatalf("events=%d output=%q", len(events), output.String())
	}
}

func TestEventEmitterFailedWriteDoesNotCommitTerminal(t *testing.T) {
	emitter := newEventEmitter(permanentErrorWriter{}, true, time.Now)
	if err := emitter.finish("result", "test", "success", "done", "", nil); err == nil {
		t.Fatal("expected permanent writer failure")
	}
	if emitter.terminal {
		t.Fatal("failed write committed terminal state")
	}
	var recovered bytes.Buffer
	emitter.writer = &recovered
	if err := emitter.finish("error", "test", "failed", "internal_error", "", nil); err != nil {
		t.Fatalf("retry after writer recovery: %v", err)
	}
	if events := decodeMachineEvents(t, recovered.String()); len(events) != 1 {
		t.Fatalf("events=%d output=%q", len(events), recovered.String())
	}
}

func TestEventEmitterHandlesShortWrites(t *testing.T) {
	w := &shortWriter{limit: 3}
	emitter := newEventEmitter(w, true, time.Now)
	if err := emitter.finish("result", "test", "success", "done", "", map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	events := decodeMachineEvents(t, w.output.String())
	if len(events) != 1 || events[0]["event"] != "result" {
		t.Fatalf("events=%#v", events)
	}
}

func TestMachineFieldsDoNotDependOnLocale(t *testing.T) {
	restoreBuildMetadata(t, "2.3.6", strings.Repeat("a", 40), "false", "2026-08-26T00:00:00Z")
	now := time.Date(2026, time.August, 26, 0, 0, 1, 0, time.UTC)

	outputs := make([]map[string]any, 0, 2)
	for _, language := range []string{"zh-CN", "en"} {
		var stdout, stderr bytes.Buffer
		exitCode := (CLI{
			Stdout: &stdout,
			Stderr: &stderr,
			Now:    func() time.Time { return now },
		}).Run([]string{"--lang", language, "version", "--json"})
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("language=%s exit=%d stderr=%q", language, exitCode, stderr.String())
		}
		events := decodeMachineEvents(t, stdout.String())
		if len(events) != 1 {
			t.Fatalf("language=%s events=%d output=%q", language, len(events), stdout.String())
		}
		outputs = append(outputs, events[0])
	}

	left, _ := json.Marshal(outputs[0])
	right, _ := json.Marshal(outputs[1])
	if !bytes.Equal(left, right) {
		t.Fatalf("machine output changed with locale:\nzh=%s\nen=%s", left, right)
	}
}

func decodeMachineEvents(t *testing.T, output string) []map[string]any {
	t.Helper()
	trimmed := strings.TrimSuffix(output, "\n")
	if trimmed == "" {
		t.Fatal("expected JSON Lines output")
	}
	lines := strings.Split(trimmed, "\n")
	events := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		if strings.TrimSpace(line) != line || line == "" {
			t.Fatalf("line %d is not one compact JSON object: %q", index, line)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", index, err, line)
		}
		events = append(events, event)
	}
	return events
}

func assertMachineEventEnvelope(t *testing.T, event map[string]any) {
	t.Helper()
	for _, field := range []string{"schema_version", "event", "phase", "status", "timestamp"} {
		if value, ok := event[field].(string); !ok || value == "" {
			t.Fatalf("field %s missing or not a non-empty string: %#v", field, event)
		}
	}
	if event["schema_version"] != "1" {
		t.Fatalf("schema_version=%v", event["schema_version"])
	}
	timestamp := event["timestamp"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", timestamp, err)
	}
	_, offset := parsed.Zone()
	if offset != 0 || !strings.HasSuffix(timestamp, "Z") {
		t.Fatalf("timestamp %q is not UTC", timestamp)
	}
}

type failOnceWriter struct {
	remainingFailures int
	output            bytes.Buffer
}

func (w *failOnceWriter) Write(value []byte) (int, error) {
	if w.remainingFailures > 0 {
		w.remainingFailures--
		return 0, errors.New("temporary writer failure")
	}
	return w.output.Write(value)
}

type permanentErrorWriter struct{}

func (permanentErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("permanent writer failure")
}

type shortWriter struct {
	limit  int
	output bytes.Buffer
}

type partialErrorWriter struct {
	limit        int
	failNext     bool
	failedPrefix string
	output       bytes.Buffer
}

type fullWriteErrorWriter struct {
	failNext bool
	output   bytes.Buffer
}

func (w *fullWriteErrorWriter) Write(value []byte) (int, error) {
	count, err := w.output.Write(value)
	if err != nil {
		return count, err
	}
	if w.failNext {
		w.failNext = false
		return count, errors.New("full writer failure")
	}
	return count, nil
}

func (w *partialErrorWriter) Write(value []byte) (int, error) {
	if w.failNext {
		w.failNext = false
		count := w.limit
		if count > len(value) {
			count = len(value)
		}
		w.failedPrefix = string(value[:count])
		_, _ = w.output.Write(value[:count])
		return count, errors.New("partial writer failure")
	}
	return w.output.Write(value)
}

func (w *shortWriter) Write(value []byte) (int, error) {
	if len(value) > w.limit {
		value = value[:w.limit]
	}
	if len(value) == 0 {
		return 0, io.ErrNoProgress
	}
	return w.output.Write(value)
}
