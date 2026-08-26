package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
)

func TestHeartbeatRepeatsLatestSnapshotWhileWorkBlocks(t *testing.T) {
	progress := usage.ScanProgress{
		HomesTotal:       1,
		HomesDiscovered:  1,
		FilesDiscovered:  3,
		FilesProcessed:   1,
		RecordsProcessed: 8,
		EventsInserted:   5,
		Warnings:         2,
	}
	runner := &blockingScanRunner{
		progress: progress,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		result: usage.ScanResult{
			Homes: 1, Files: 3, Records: 8, EventsInserted: 5, Warnings: 2,
		},
	}
	var stdout lockedBuffer
	var stderr bytes.Buffer
	cli := CLI{
		Stdout:            &stdout,
		Stderr:            &stderr,
		HeartbeatInterval: 10 * time.Millisecond,
		openScanState: func() (*scanRuntime, error) {
			return &scanRuntime{scanner: runner, homes: []string{"synthetic-home"}}, nil
		},
	}
	done := make(chan int, 1)
	go func() { done <- cli.Run([]string{"scan", "--json"}) }()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("scanner did not start")
	}
	waitForProgressCopies(t, &stdout, progress, 2)
	close(runner.release)
	select {
	case exitCode := <-done:
		if exitCode != 0 {
			t.Fatalf("exit=%d stderr=%q output=%q", exitCode, stderr.String(), stdout.String())
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not finish after release")
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON stderr must stay empty: %q", stderr.String())
	}
}

func TestScanJSONEmitsProgressAndSingleTerminalResult(t *testing.T) {
	target, st := newSyntheticScanRuntime(t)
	var stdout, stderr bytes.Buffer
	cli := CLI{
		Stdout:            &stdout,
		Stderr:            &stderr,
		HeartbeatInterval: time.Hour,
		openScanState: func() (*scanRuntime, error) {
			return target, nil
		},
	}

	if exitCode := cli.Run([]string{"scan", "--rebuild", "--json"}); exitCode != 0 {
		t.Fatalf("exit=%d stderr=%q output=%q", exitCode, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON stderr must stay empty: %q", stderr.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	progressEvents, terminalEvents := 0, 0
	for _, event := range events {
		assertMachineEventEnvelope(t, event)
		if event["phase"] != "scan" {
			t.Fatalf("unexpected phase: %#v", event)
		}
		switch event["event"] {
		case "progress":
			progressEvents++
			assertCompleteProgressFields(t, event["progress"])
		case "result":
			terminalEvents++
			if event["code"] != "scan_complete" {
				t.Fatalf("terminal code=%v", event["code"])
			}
			resultObject, ok := event["result"].(map[string]any)
			if !ok {
				t.Fatalf("result is not an object: %#v", event["result"])
			}
			encoded, err := json.Marshal(resultObject["scan"])
			if err != nil {
				t.Fatal(err)
			}
			var result usage.ScanResult
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatalf("decode scan result: %v", err)
			}
			if result.Homes != 1 || result.Files != 1 || result.Records != 2 ||
				result.EventsInserted != 1 || result.Corrections != 0 ||
				result.Duplicates != 0 || result.Warnings != 0 || result.Unattributed != 0 ||
				len(result.StateDatabases) != 0 || result.ElapsedMillis < 0 {
				t.Fatalf("unexpected fixed synthetic scan result: %+v", result)
			}
		case "error":
			t.Fatalf("unexpected error event: %#v", event)
		default:
			t.Fatalf("unexpected event: %#v", event)
		}
	}
	if progressEvents < 1 || terminalEvents != 1 {
		t.Fatalf("progress=%d terminal=%d output=%q", progressEvents, terminalEvents, stdout.String())
	}
	status, err := st.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.EventCount != 1 || status.SessionCount != 1 || status.WarningCount != 0 {
		t.Fatalf("database did not match JSON result: %+v", status)
	}
}

func TestScanJSONFailureUsesStableCode(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		runner   scanRunner
		wantExit int
		wantCode string
	}{
		{
			name: "scanner failure",
			args: []string{"scan", "--json"},
			runner: scanRunnerFunc(func(_ context.Context, _ []string, _ bool, observer usage.ProgressObserver) (usage.ScanResult, error) {
				observer(usage.ScanProgress{HomesTotal: 1, HomesDiscovered: 1, Warnings: 1})
				return usage.ScanResult{}, errors.New("synthetic scan exploded")
			}),
			wantExit: 1,
			wantCode: "scan_failed",
		},
		{
			name:     "unknown flag",
			args:     []string{"scan", "--json", "--unknown"},
			runner:   panicScanRunner{},
			wantExit: 2,
			wantCode: "invalid_arguments",
		},
		{
			name:     "extra argument",
			args:     []string{"scan", "--json", "extra"},
			runner:   panicScanRunner{},
			wantExit: 2,
			wantCode: "invalid_arguments",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cli := CLI{
				Stdout:            &stdout,
				Stderr:            &stderr,
				HeartbeatInterval: time.Hour,
				openScanState: func() (*scanRuntime, error) {
					return &scanRuntime{scanner: test.runner, homes: []string{"synthetic-home"}}, nil
				},
			}
			if exitCode := cli.Run(test.args); exitCode != test.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%q output=%q", exitCode, test.wantExit, stderr.String(), stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("JSON stderr must stay empty: %q", stderr.String())
			}
			events := decodeMachineEvents(t, stdout.String())
			terminal := 0
			for _, event := range events {
				assertMachineEventEnvelope(t, event)
				if event["event"] == "error" || event["event"] == "result" {
					terminal++
					if event["event"] != "error" || event["code"] != test.wantCode {
						t.Fatalf("unexpected terminal event: %#v", event)
					}
				}
			}
			if terminal != 1 {
				t.Fatalf("terminal=%d output=%q", terminal, stdout.String())
			}
		})
	}
}

func TestScanHumanOutputRemainsLocalized(t *testing.T) {
	result := usage.ScanResult{
		Homes: 2, Files: 3, Records: 5, EventsInserted: 7,
		Duplicates: 2, Warnings: 4, ElapsedMillis: 1200,
	}
	progress := usage.ScanProgress{
		HomesTotal: 2, HomesDiscovered: 2, FilesDiscovered: 3,
		FilesProcessed: 3, RecordsProcessed: 5, EventsInserted: 7, Warnings: 4,
	}
	tests := []struct {
		language string
		want     []string
		reject   string
	}{
		{language: "zh-CN", want: []string{"扫描进度", "发现 3 个文件", "处理 5 条记录", "新增 7 个事件", "4 条提示", "扫描完成"}, reject: "Scan complete"},
		{language: "en", want: []string{"Scan progress", "3 files discovered", "5 records processed", "7 events added", "4 notices", "Scan complete"}, reject: "扫描完成"},
	}
	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runner := scanRunnerFunc(func(_ context.Context, _ []string, _ bool, observer usage.ProgressObserver) (usage.ScanResult, error) {
				observer(progress)
				return result, nil
			})
			cli := CLI{
				Stdout:            &stdout,
				Stderr:            &stderr,
				HeartbeatInterval: time.Hour,
				openScanState: func() (*scanRuntime, error) {
					return &scanRuntime{scanner: runner, homes: []string{"a", "b"}}, nil
				},
			}
			if exitCode := cli.Run([]string{"--lang", test.language, "scan"}); exitCode != 0 {
				t.Fatalf("exit=%d stderr=%q output=%q", exitCode, stderr.String(), stdout.String())
			}
			if stderr.Len() != 0 || strings.Contains(stdout.String(), "{") {
				t.Fatalf("human output must remain plain: stderr=%q output=%q", stderr.String(), stdout.String())
			}
			for _, fragment := range test.want {
				if !strings.Contains(stdout.String(), fragment) {
					t.Fatalf("missing %q in %q", fragment, stdout.String())
				}
			}
			if strings.Contains(stdout.String(), test.reject) {
				t.Fatalf("output leaked another locale: %q", stdout.String())
			}
		})
	}
}

type blockingScanRunner struct {
	progress usage.ScanProgress
	started  chan struct{}
	release  chan struct{}
	result   usage.ScanResult
	err      error
}

func (s *blockingScanRunner) ScanWithProgress(
	ctx context.Context,
	_ []string,
	_ bool,
	observer usage.ProgressObserver,
) (usage.ScanResult, error) {
	observer(s.progress)
	close(s.started)
	select {
	case <-s.release:
		return s.result, s.err
	case <-ctx.Done():
		return usage.ScanResult{}, ctx.Err()
	}
}

type scanRunnerFunc func(context.Context, []string, bool, usage.ProgressObserver) (usage.ScanResult, error)

func (fn scanRunnerFunc) ScanWithProgress(
	ctx context.Context,
	homes []string,
	rebuild bool,
	observer usage.ProgressObserver,
) (usage.ScanResult, error) {
	return fn(ctx, homes, rebuild, observer)
}

type panicScanRunner struct{}

func (panicScanRunner) ScanWithProgress(context.Context, []string, bool, usage.ProgressObserver) (usage.ScanResult, error) {
	panic("scanner must not run for invalid arguments")
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func waitForProgressCopies(t *testing.T, output *lockedBuffer, want usage.ScanProgress, copies int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, err := parseCompleteMachineEvents(output.String())
		if err == nil {
			matched := 0
			for _, event := range events {
				if event["event"] != "progress" {
					continue
				}
				encoded, _ := json.Marshal(event["progress"])
				var progress usage.ScanProgress
				if json.Unmarshal(encoded, &progress) == nil && progress == want {
					matched++
				}
			}
			if matched >= copies {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("latest snapshot was not repeated %d times: %q", copies, output.String())
}

func parseCompleteMachineEvents(output string) ([]map[string]any, error) {
	if output == "" || !strings.HasSuffix(output, "\n") {
		return nil, errors.New("incomplete JSON Lines output")
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func assertCompleteProgressFields(t *testing.T, value any) {
	t.Helper()
	progress, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("progress is not an object: %#v", value)
	}
	for _, field := range []string{
		"homes_total", "homes_discovered", "files_discovered", "files_processed",
		"records_processed", "events_inserted", "warnings",
	} {
		if _, ok := progress[field].(float64); !ok {
			t.Fatalf("progress field %s missing or non-numeric: %#v", field, progress)
		}
	}
}

func newSyntheticScanRuntime(t *testing.T) (*scanRuntime, *store.Store) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "codex-home")
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"timestamp":"2026-08-26T01:00:00Z","type":"session_meta","payload":{"id":"scan-json-session","cwd":"/synthetic/project"}}`,
		`{"timestamp":"2026-08-26T01:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":100},"last_token_usage":{"input_tokens":80,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":100}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "rollout-synthetic.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	target := &scanRuntime{
		scanner: &usage.Scanner{Store: st},
		homes:   []string{home},
		close:   func() error { return nil },
	}
	return target, st
}
