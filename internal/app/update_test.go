package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateCheckReturnsReleaseChannelDisabledWithoutNetwork(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	wantSentinel := []byte("unchanged")
	if err := os.WriteFile(sentinel, wantSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_USAGE_HOME", root)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, time.August, 27, 1, 2, 3, 0, time.UTC) },
	}).Run([]string{"update", "--check", "--json"})
	if exitCode == 0 || stderr.String() != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 {
		t.Fatalf("events=%#v; update must emit only one terminal error", events)
	}
	terminal := events[0]
	assertMachineEventEnvelope(t, terminal)
	if terminal["event"] != "error" || terminal["code"] != "release_channel_disabled" {
		t.Fatalf("terminal=%#v", terminal)
	}
	assertDisabledUpdateReceipt(t, terminal["result"])
	after, err := os.ReadFile(sentinel)
	if err != nil || !bytes.Equal(after, wantSentinel) {
		t.Fatalf("update modified sentinel: data=%q err=%v", after, err)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = (CLI{Stdout: &stdout, Stderr: &stderr}).Run([]string{"--lang", "en", "update", "--check"})
	if exitCode == 0 || stdout.String() != "" {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "source-only") || !strings.Contains(stderr.String(), "INSTALL.md") {
		t.Fatalf("human update explanation=%q", stderr.String())
	}
}

func TestUpdateYesReturnsReleaseChannelDisabledWithoutModification(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	wantSentinel := []byte("unchanged")
	if err := os.WriteFile(sentinel, wantSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_USAGE_HOME", root)

	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, time.August, 27, 1, 2, 3, 0, time.UTC) },
	}).Run([]string{"update", "--yes", "--json"})
	if exitCode == 0 || stderr.String() != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "release_channel_disabled" {
		t.Fatalf("events=%#v", events)
	}
	assertDisabledUpdateReceipt(t, events[0]["result"])
	after, err := os.ReadFile(sentinel)
	if err != nil || !bytes.Equal(after, wantSentinel) {
		t.Fatalf("update modified sentinel: data=%q err=%v", after, err)
	}
}

func assertDisabledUpdateReceipt(t *testing.T, value any) {
	t.Helper()
	receipt, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("update receipt=%#v", value)
	}
	if receipt["channel_enabled"] != false || receipt["checked"] != false || receipt["modified"] != false {
		t.Fatalf("update receipt=%#v", receipt)
	}
	if receipt["policy_path"] != "install-policy.json" {
		t.Fatalf("policy_path=%#v", receipt["policy_path"])
	}
}
