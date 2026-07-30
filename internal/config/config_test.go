package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallOTelPreservesCommentsAndExistingSection(t *testing.T) {
	home := t.TempDir()
	backup := filepath.Join(t.TempDir(), "backups")
	path := CodexConfigPath(home)
	original := "# 用户注释\r\nmodel = \"gpt-5\"\r\n\r\n[otel]\r\n# 保留这一行\r\nlog_user_prompt = false\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InstallOTel(home, "http://127.0.0.1:43189/v1/metrics", backup)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Conflict || result.Backup == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, wanted := range []string{
		"# 用户注释", `model = "gpt-5"`, "# 保留这一行",
		managedBegin, `protocol = "json"`, managedEnd,
	} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("updated config lost %q:\n%s", wanted, text)
		}
	}
	backupData, err := os.ReadFile(result.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupData) != original {
		t.Fatalf("backup mismatch\nwant %q\ngot  %q", original, string(backupData))
	}
	changed, err := UninstallOTel(home)
	if err != nil || !changed {
		t.Fatalf("uninstall changed=%v err=%v", changed, err)
	}
	restored, _ := os.ReadFile(path)
	if strings.Contains(string(restored), "codex-meter managed") ||
		!strings.Contains(string(restored), "# 保留这一行") {
		t.Fatalf("managed stanza removal damaged config:\n%s", restored)
	}
}

func TestInstallOTelDoesNotOverwriteExporter(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	original := `[otel]
metrics_exporter = { otlp-http = { endpoint = "http://collector:4318", protocol = "binary" } }
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InstallOTel(home, "http://127.0.0.1:43189/v1/metrics", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Conflict || result.Changed {
		t.Fatalf("unexpected result: %+v", result)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatalf("existing exporter was modified")
	}
}

func TestInstallOTelInvalidTOMLDoesNotModify(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	original := "[otel\nbroken = true\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallOTel(home, "http://127.0.0.1:43189/v1/metrics", t.TempDir()); err == nil {
		t.Fatal("expected semantic TOML error")
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatal("invalid input was modified")
	}
}

func TestRollbackRestoresExactOriginal(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	original := "# exact\nmodel = \"gpt-5\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InstallOTel(home, "http://127.0.0.1:43189/v1/metrics", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := RollbackOTel(home, result.Backup); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatalf("rollback mismatch: %q", after)
	}
}

func TestInstallIntoOTelHeaderWithoutTrailingNewline(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	if err := os.WriteFile(path, []byte("[otel]"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InstallOTel(home, "http://127.0.0.1:43189/v1/metrics", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("expected change: %+v", result)
	}
	updated, _ := os.ReadFile(path)
	if !strings.Contains(string(updated), "[otel]\n"+managedBegin) {
		t.Fatalf("missing newline after section header: %q", updated)
	}
}

func TestResolvePathsRejectsNonDedicatedOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_METER_HOME", dir)
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("expected non-dedicated state directory rejection")
	}
}

func TestResolvePathsAcceptsEmptyOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_METER_HOME", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.StateDir != dir {
		t.Fatalf("state dir mismatch: %q", paths.StateDir)
	}
}
