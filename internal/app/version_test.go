package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVersionJSONHasStableBuildIdentity(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	restoreBuildMetadata(t, "2.3.6", commit, "false", "2026-08-26T00:00:00Z")

	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, time.August, 26, 0, 0, 1, 0, time.UTC) },
	}).Run([]string{"version", "--json"})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}

	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 {
		t.Fatalf("events=%d output=%q", len(events), stdout.String())
	}
	event := events[0]
	assertMachineEventEnvelope(t, event)
	if event["event"] != "result" || event["phase"] != "version" || event["status"] != "success" || event["code"] != "version" {
		t.Fatalf("unexpected terminal envelope: %#v", event)
	}
	result, ok := event["result"].(map[string]any)
	if !ok {
		t.Fatalf("result=%#v", event["result"])
	}
	want := map[string]any{
		"version":    "2.3.6",
		"commit":     commit,
		"dirty":      false,
		"build_date": "2026-08-26T00:00:00Z",
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	}
	for key, value := range want {
		if result[key] != value {
			t.Fatalf("result[%q]=%#v want=%#v; result=%#v", key, result[key], value, result)
		}
	}
}

func TestCurrentBuildIdentityRejectsInvalidDirtyValue(t *testing.T) {
	restoreBuildMetadata(t, "2.3.6", "dev", "not-a-bool", "unknown")
	_, err := currentBuildIdentity()
	if err == nil {
		t.Fatal("expected invalid BuildDirty to fail")
	}
	var typed *codedError
	if !errors.As(err, &typed) || typed.Code != "build_metadata_invalid" || typed.ExitCode != 1 {
		t.Fatalf("error=%T %v, want build_metadata_invalid codedError with exit 1", err, err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, time.August, 26, 0, 0, 1, 0, time.UTC) },
	}).Run([]string{"version", "--json"})
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "build_metadata_invalid" {
		t.Fatalf("unexpected error output: %#v", events)
	}
}

func TestBuildScriptsKeepCommitAndDirtySeparate(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	tests := []struct {
		path           string
		skipTestsToken string
		dirtySuffix    string
	}{
		{path: filepath.Join(root, "scripts", "build.ps1"), skipTestsToken: "[switch]$SkipTests", dirtySuffix: "$Commit-dirty"},
		{path: filepath.Join(root, "scripts", "build.sh"), skipTestsToken: "SKIP_TESTS", dirtySuffix: "${commit}-dirty"},
	}

	for _, test := range tests {
		t.Run(filepath.Base(test.path), func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			script := string(content)
			if !strings.Contains(script, "rev-parse HEAD") || strings.Contains(script, "rev-parse --short HEAD") {
				t.Fatalf("script must record the full commit SHA:\n%s", script)
			}
			if !strings.Contains(script, "BuildDirty=") {
				t.Fatalf("script does not pass an independent BuildDirty ldflag:\n%s", script)
			}
			if strings.Contains(script, test.dirtySuffix) {
				t.Fatalf("script still appends dirty state to commit:\n%s", script)
			}
			if !strings.Contains(script, test.skipTestsToken) {
				t.Fatalf("script is missing the explicit test-skip switch:\n%s", script)
			}
			if filepath.Ext(test.path) == ".sh" {
				for _, token := range []string{
					"rev-parse --show-toplevel",
					"pwd -P",
					`[[ "$git_root" == "$project_root" ]]`,
					"git_status=",
				} {
					if !strings.Contains(script, token) {
						t.Fatalf("shell build metadata is not fail-closed; missing %q:\n%s", token, script)
					}
				}
			}
		})
	}
}

func TestBuildScriptsRejectAncestorRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell behavior is exercised on the Linux CI runner")
	}
	root := t.TempDir()
	runTestCommand(t, root, "git", "init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, root, "git", "add", "tracked")
	runTestCommand(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "fixture")
	ancestorCommit := strings.TrimSpace(runTestCommand(t, root, "git", "rev-parse", "HEAD"))

	project := filepath.Join(root, "nested", "project")
	log := runCopiedBuildScript(t, project, nil)
	if !strings.Contains(log, "BuildDirty=true") || !strings.Contains(log, "Commit=dev") {
		t.Fatalf("ancestor repository metadata was trusted:\n%s", log)
	}
	if strings.Contains(log, "Commit="+ancestorCommit) {
		t.Fatalf("ancestor commit leaked into build metadata:\n%s", log)
	}
}

func TestBuildScriptsFailClosedWhenStatusFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell behavior is exercised on the Linux CI runner")
	}
	project := t.TempDir()
	fakeBin := filepath.Join(project, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeGit := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_GIT_LOG"
case "$*" in
  *"rev-parse --show-toplevel"*) printf '%s\n' "$FAKE_PROJECT_ROOT" ;;
  *"rev-parse HEAD"*) printf '0123456789abcdef0123456789abcdef01234567\n' ;;
  *"status --porcelain"*) exit 42 ;;
  *) exit 1 ;;
esac
`
	writeExecutable(t, filepath.Join(fakeBin, "git"), fakeGit)
	path := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	gitLogPath := filepath.Join(project, "git.log")
	log := runCopiedBuildScript(t, project, []string{
		"PATH=" + path,
		"FAKE_PROJECT_ROOT=" + project,
		"FAKE_GIT_LOG=" + gitLogPath,
	})
	if !strings.Contains(log, "BuildDirty=true") || !strings.Contains(log, "Commit=dev") {
		t.Fatalf("failed git status produced trusted metadata:\n%s", log)
	}
	gitLog, err := os.ReadFile(gitLogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"rev-parse --show-toplevel", "rev-parse HEAD", "status --porcelain"} {
		if !strings.Contains(string(gitLog), call) {
			t.Fatalf("fake git did not observe %q:\n%s", call, gitLog)
		}
	}
}

func TestVersionJSONRejectsUnknownFlags(t *testing.T) {
	restoreBuildMetadata(t, "2.3.6", "dev", "true", "unknown")
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Date(2026, time.August, 26, 0, 0, 1, 0, time.UTC) },
	}).Run([]string{"version", "--json", "--unknown"})
	if exitCode != 2 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "invalid_arguments" {
		t.Fatalf("unexpected error output: %#v", events)
	}
}

func TestVersionJSONRejectsMalformedLanguageArguments(t *testing.T) {
	restoreBuildMetadata(t, "2.3.6", "dev", "true", "unknown")
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing value", args: []string{"version", "--json", "--lang"}},
		{name: "unsupported value", args: []string{"version", "--json", "--lang", "fr"}},
		{name: "next flag is not a value", args: []string{"version", "--lang", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(test.args)
			if exitCode != 2 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
			}
			events := decodeMachineEvents(t, stdout.String())
			if len(events) != 1 || events[0]["event"] != "error" || events[0]["phase"] != "version" || events[0]["code"] != "invalid_arguments" {
				t.Fatalf("events=%#v", events)
			}
		})
	}
}

func TestVersionJSONDoesNotClaimCommandAfterLeadingFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{Stdout: &stdout, Stderr: &stderr}).Run([]string{"--json", "version", "--lang"})
	if exitCode != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "--lang") {
		t.Fatalf("leading flag was misclassified as version JSON: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestGlobalVersionRemainsHumanReadable(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	restoreBuildMetadata(t, "2.3.6", commit, "true", "unknown")
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{Stdout: &stdout, Stderr: &stderr}).Run([]string{"--version"})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	output := stdout.String()
	if strings.Count(output, "\n") != 1 || strings.HasPrefix(strings.TrimSpace(output), "{") {
		t.Fatalf("global version is not one human-readable line: %q", output)
	}
	for _, value := range []string{"codex-usage 2.3.6", commit, "dirty=true", "unknown", runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(output, value) {
			t.Fatalf("version output %q does not contain %q", output, value)
		}
	}
	if strings.Contains(output, commit+"-dirty") {
		t.Fatalf("dirty state was appended to commit: %q", output)
	}
}

func restoreBuildMetadata(t *testing.T, version, commit, dirty, buildDate string) {
	t.Helper()
	previousVersion, previousCommit := Version, Commit
	previousDirty, previousBuildDate := BuildDirty, BuildDate
	Version, Commit, BuildDirty, BuildDate = version, commit, dirty, buildDate
	t.Cleanup(func() {
		Version, Commit, BuildDirty, BuildDate = previousVersion, previousCommit, previousDirty, previousBuildDate
	})
}

func runCopiedBuildScript(t *testing.T, project string, extraEnvironment []string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(project, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(project, "scripts", "build.sh")
	writeExecutable(t, script, string(source))
	fakeGo := filepath.Join(project, "fake-go")
	writeExecutable(t, fakeGo, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_GO_LOG"
if [[ "${1:-}" == "build" ]]; then
  output=""
  while (($#)); do
    if [[ "$1" == "-o" ]]; then
      shift
      output="$1"
      break
    fi
    shift
  done
  : > "$output"
fi
`)
	logPath := filepath.Join(project, "go.log")
	command := exec.Command("bash", script)
	command.Env = append(os.Environ(),
		"GO="+fakeGo,
		"SKIP_TESTS=1",
		"FAKE_GO_LOG="+logPath,
	)
	command.Env = append(command.Env, extraEnvironment...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(log)
}

func runTestCommand(t *testing.T, directory, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
