package app

import (
	"bytes"
	"strings"
	"testing"
)

func runCLIForLocaleTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(args)
	return code, stdout.String(), stderr.String()
}

func TestGlobalLanguageFlagSelectsEnglishHelp(t *testing.T) {
	t.Setenv("CODEX_USAGE_LANG", "zh-CN")
	code, stdout, stderr := runCLIForLocaleTest(t, "--lang", "en", "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "local Codex token accounting for this machine") || strings.Contains(stdout, "用法:") {
		t.Fatalf("unexpected English help:\n%s", stdout)
	}
}

func TestLanguageEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv("CODEX_USAGE_LANG", "en")
	code, stdout, _ := runCLIForLocaleTest(t, "--help")
	if code != 0 || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("environment did not select English: code=%d output=%q", code, stdout)
	}
	code, stdout, _ = runCLIForLocaleTest(t, "--lang=zh-CN", "--help")
	if code != 0 || !strings.Contains(stdout, "用法:") {
		t.Fatalf("flag did not override environment: code=%d output=%q", code, stdout)
	}
}

func TestInvalidGlobalLanguageReturnsUsageExitCode(t *testing.T) {
	code, _, stderr := runCLIForLocaleTest(t, "--lang", "fr", "install")
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "en or zh-CN") {
		t.Fatalf("unexpected error: %q", stderr)
	}
}
