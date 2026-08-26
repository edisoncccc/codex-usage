package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
)

func TestUninstallJSONRequiresConfirmation(t *testing.T) {
	fixture := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
	before := fixture.snapshot(t)
	exitCode, stdout, stderr := fixture.run(strings.NewReader("unexpected"), "uninstall", "--json")
	if exitCode == 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["event"] != "error" || terminal["code"] != "confirmation_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
	receipt := uninstallReceiptMap(t, terminal["result"])
	if receipt["state_path"] != fixture.paths.StateDir || receipt["data_preserved"] != true {
		t.Fatalf("receipt=%#v", receipt)
	}
	if len(fixture.calls) != 0 {
		t.Fatalf("unconfirmed uninstall side effects=%v", fixture.calls)
	}
	if after := fixture.snapshot(t); !bytes.Equal(after, before) {
		t.Fatalf("unconfirmed uninstall changed files:\nbefore=%q\nafter=%q", before, after)
	}

	t.Run("human mode asks exactly once", func(t *testing.T) {
		human := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
		reader := &singleConfirmationReader{value: []byte("yes\n")}
		exitCode, stdout, stderr := human.run(reader, "--lang", "en", "uninstall")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if reader.reads != 1 || strings.Count(stdout, "Proceed") != 1 {
			t.Fatalf("reads=%d stdout=%q", reader.reads, stdout)
		}
	})
}

func TestUninstallKeepsDatabaseAndConfig(t *testing.T) {
	fixture := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
	wantConfig, err := os.ReadFile(fixture.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDatabase, err := os.ReadFile(fixture.paths.Database)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := fixture.run(strings.NewReader(""), "uninstall", "--yes", "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, fixture.calls)
	}
	if got := strings.Join(fixture.calls, ","); got != "validate,uninstall_service,remove_program,remove_record" {
		t.Fatalf("call order=%q", got)
	}
	receipt := uninstallReceiptMap(t, installTerminalEvent(t, stdout)["result"])
	if receipt["program_removed"] != true || receipt["removal_scheduled"] != false ||
		receipt["data_preserved"] != true || receipt["purged"] != false {
		t.Fatalf("receipt=%#v", receipt)
	}
	if got, err := os.ReadFile(fixture.paths.ConfigPath); err != nil || !bytes.Equal(got, wantConfig) {
		t.Fatalf("config changed: data=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(fixture.paths.Database); err != nil || !bytes.Equal(got, wantDatabase) {
		t.Fatalf("database changed: data=%q err=%v", got, err)
	}
	if _, err := os.Lstat(fixture.paths.InstallRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install record remained: %v", err)
	}
}

func TestUninstallPurgeWithoutYesOnlyReportsAbsoluteTarget(t *testing.T) {
	fixture := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
	exitCode, stdout, stderr := fixture.run(strings.NewReader("unexpected"), "uninstall", "--purge", "--json")
	if exitCode == 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["code"] != "confirmation_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
	receipt := uninstallReceiptMap(t, terminal["result"])
	statePath, ok := receipt["state_path"].(string)
	if !ok || !filepath.IsAbs(statePath) || filepath.Clean(statePath) != fixture.paths.StateDir {
		t.Fatalf("state_path=%#v want=%q", receipt["state_path"], fixture.paths.StateDir)
	}
	if receipt["purged"] != false || receipt["data_preserved"] != false {
		t.Fatalf("receipt=%#v", receipt)
	}
	if len(fixture.calls) != 0 {
		t.Fatalf("unconfirmed purge side effects=%v", fixture.calls)
	}
}

func TestUninstallPurgeYesUsesValidatedStateDirectory(t *testing.T) {
	t.Run("valid marker validates before every removal", func(t *testing.T) {
		fixture := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
		exitCode, stdout, stderr := fixture.run(strings.NewReader(""), "uninstall", "--purge", "--yes", "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, fixture.calls)
		}
		if got := strings.Join(fixture.calls, ","); got != "validate,uninstall_service,remove_program,remove_record" {
			t.Fatalf("call order=%q", got)
		}
		receipt := uninstallReceiptMap(t, installTerminalEvent(t, stdout)["result"])
		if receipt["data_preserved"] != false || receipt["purged"] != true {
			t.Fatalf("receipt=%#v", receipt)
		}
	})

	t.Run("missing marker stops before removal hooks", func(t *testing.T) {
		fixture := newUninstallTestFixture(t, false, platform.RemovalModeRemoved)
		exitCode, stdout, stderr := fixture.run(strings.NewReader(""), "uninstall", "--purge", "--yes", "--json")
		if exitCode == 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, fixture.calls)
		}
		terminal := installTerminalEvent(t, stdout)
		if terminal["event"] != "error" || terminal["code"] != "existing_install_untrusted" {
			t.Fatalf("terminal=%#v", terminal)
		}
		if got := strings.Join(fixture.calls, ","); got != "validate" {
			t.Fatalf("validation failure called removal hooks: %q", got)
		}
	})

	t.Run("any validation failure stops before removal hooks", func(t *testing.T) {
		fixture := newUninstallTestFixture(t, true, platform.RemovalModeRemoved)
		fixture.validateErr = errors.New("synthetic validation failure")
		exitCode, stdout, stderr := fixture.run(strings.NewReader(""), "uninstall", "--yes", "--json")
		if exitCode == 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, fixture.calls)
		}
		if got := strings.Join(fixture.calls, ","); got != "validate" {
			t.Fatalf("validation failure called removal hooks: %q", got)
		}
	})
}

func TestWindowsUninstallReceiptReportsScheduledRemoval(t *testing.T) {
	fixture := newUninstallTestFixture(t, true, platform.RemovalModeScheduled)
	exitCode, stdout, stderr := fixture.run(strings.NewReader(""), "uninstall", "--yes", "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, fixture.calls)
	}
	receipt := uninstallReceiptMap(t, installTerminalEvent(t, stdout)["result"])
	if receipt["program_removed"] != false || receipt["removal_scheduled"] != true || receipt["purged"] != false {
		t.Fatalf("scheduled removal was overstated: %#v", receipt)
	}
	if _, err := os.Lstat(fixture.paths.InstalledEXE); err != nil {
		t.Fatalf("scheduled fake unexpectedly removed program: %v", err)
	}
	if _, err := os.Lstat(fixture.paths.InstallRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful scheduled uninstall kept install record: %v", err)
	}
}

type uninstallTestFixture struct {
	paths       config.Paths
	deps        uninstallCommandDeps
	mode        platform.RemovalMode
	calls       []string
	validateErr error
}

func newUninstallTestFixture(t *testing.T, marker bool, mode platform.RemovalMode) *uninstallTestFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	installDir := filepath.Join(stateDir, "bin")
	executableName := "codex-usage"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	paths := config.Paths{
		StateDir:      stateDir,
		ConfigPath:    filepath.Join(stateDir, "config.json"),
		InstallRecord: filepath.Join(stateDir, "install.json"),
		Database:      filepath.Join(stateDir, "usage.sqlite"),
		BackupDir:     filepath.Join(stateDir, "backups"),
		InstallDir:    installDir,
		InstalledEXE:  filepath.Join(installDir, executableName),
	}
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigPath, []byte("config-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Database, []byte("database-bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstalledEXE, []byte("program-bytes\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if marker {
		if err := os.WriteFile(filepath.Join(stateDir, ".codex-usage-state"), []byte("codex-usage-state-v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := install.FileSHA256(paths.InstalledEXE)
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Save(paths.InstallRecord, install.Record{
		SchemaVersion:    install.RecordSchemaVersion,
		Product:          install.ProductName,
		Version:          "2.3.5",
		Commit:           strings.Repeat("a", 40),
		Dirty:            true,
		BuildDate:        "2026-08-27T00:00:00Z",
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		ExecutablePath:   paths.InstalledEXE,
		ExecutableSHA256: digest,
		Source:           install.SourceBuild,
		InstalledAt:      "2026-08-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	fixture := &uninstallTestFixture{paths: paths, mode: mode}
	fixture.deps = uninstallCommandDeps{
		ResolvePaths: func() (config.Paths, error) { return fixture.paths, nil },
		ValidateRemoval: func(executable, stateDir, recordPath string, purge bool) error {
			fixture.calls = append(fixture.calls, "validate")
			if fixture.validateErr != nil {
				return fixture.validateErr
			}
			return validateUninstallRemoval(executable, stateDir, recordPath, purge)
		},
		UninstallService: func(string, string) error {
			fixture.calls = append(fixture.calls, "uninstall_service")
			return nil
		},
		RemoveInstalledExecutable: func(executable, _ string, _ bool) error {
			fixture.calls = append(fixture.calls, "remove_program")
			if fixture.mode == platform.RemovalModeRemoved {
				return os.Remove(executable)
			}
			return nil
		},
		RemoveInstallRecord: func(path string) error {
			fixture.calls = append(fixture.calls, "remove_record")
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		},
		RemovalMode: func() platform.RemovalMode { return fixture.mode },
	}
	return fixture
}

func (f *uninstallTestFixture) run(stdin io.Reader, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout:        &stdout,
		Stderr:        &stderr,
		Stdin:         stdin,
		Now:           func() time.Time { return time.Date(2026, time.August, 27, 1, 2, 3, 0, time.UTC) },
		uninstallDeps: &f.deps,
	}).Run(args)
	return exitCode, stdout.String(), stderr.String()
}

func (f *uninstallTestFixture) snapshot(t *testing.T) []byte {
	t.Helper()
	var snapshot bytes.Buffer
	err := filepath.Walk(f.paths.StateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(f.paths.StateDir, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&snapshot, "%s:%s\n", relative, info.Mode())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&snapshot, "%x\n", data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Bytes()
}

func uninstallReceiptMap(t *testing.T, value any) map[string]any {
	t.Helper()
	receipt, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("uninstall receipt=%#v", value)
	}
	for _, field := range []string{
		"install_path", "state_path", "database_path", "program_removed",
		"removal_scheduled", "data_preserved", "purged",
	} {
		if _, ok := receipt[field]; !ok {
			t.Fatalf("receipt missing %s: %#v", field, receipt)
		}
	}
	return receipt
}

type singleConfirmationReader struct {
	value []byte
	reads int
}

func (r *singleConfirmationReader) Read(buffer []byte) (int, error) {
	r.reads++
	if r.reads > 1 {
		return 0, errors.New("confirmation input was read more than once")
	}
	count := copy(buffer, r.value)
	return count, nil
}
