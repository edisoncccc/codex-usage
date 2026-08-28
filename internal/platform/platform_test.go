package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestValidatePurgeStateDirRequiresExactMarker(t *testing.T) {
	dir := t.TempDir()
	if err := ValidatePurgeStateDir(dir); err == nil {
		t.Fatal("expected missing marker rejection")
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-usage-state"), []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePurgeStateDir(dir); err == nil {
		t.Fatal("expected invalid marker rejection")
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-usage-state"), []byte("codex-usage-state-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePurgeStateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePurgeStateDirRejectsBroadDirectories(t *testing.T) {
	root := filepath.Clean(filepath.VolumeName(t.TempDir()) + string(os.PathSeparator))
	if err := ValidatePurgeStateDir(root); err == nil {
		t.Fatal("expected filesystem root rejection")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := ValidatePurgeStateDir(home); err == nil {
			t.Fatal("expected user home rejection")
		}
	}
}

func TestServiceAndRemovalModesAreStable(t *testing.T) {
	if ServiceModePersistent != "persistent" {
		t.Fatalf("persistent service mode=%q", ServiceModePersistent)
	}
	if ServiceModeDetachedFallback != "detached_fallback" {
		t.Fatalf("detached fallback service mode=%q", ServiceModeDetachedFallback)
	}
	if RemovalModeRemoved != "removed" {
		t.Fatalf("removed mode=%q", RemovalModeRemoved)
	}
	if RemovalModeScheduled != "scheduled" {
		t.Fatalf("scheduled mode=%q", RemovalModeScheduled)
	}
}

func TestRenameNoReplacePreservesUnknownTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("unknown-target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(source, target); err == nil {
		t.Fatal("expected no-replace rename to reject an existing target")
	}
	for path, want := range map[string]string{source: "source", target: "unknown-target"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("rename changed %s: %q err=%v", path, data, err)
		}
	}
}

func TestLinuxSyncParentAcceptsDurableTempEntry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux directory fsync")
	}
	path := filepath.Join(t.TempDir(), "durable-entry")
	if err := os.WriteFile(path, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncParent(path); err != nil {
		t.Fatalf("sync parent directory: %v", err)
	}
}

func TestPreviousExecutableCleanupRejectsCrossBoundaryPath(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "foreign-program")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := PreviousService{
		StateDir:   filepath.Join(root, "previous-state"),
		InstallDir: filepath.Join(root, "previous-install"),
		Executable: foreign,
	}
	if err := removePreviousExecutable(previous); err == nil {
		t.Fatal("expected cross-boundary previous executable cleanup rejection")
	}
	data, err := os.ReadFile(foreign)
	if err != nil || string(data) != "foreign" {
		t.Fatalf("foreign executable changed: %q err=%v", data, err)
	}
}

func TestRemovePreviousServiceRejectsForeignStateBeforeMutation(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "codex-meter")
	installDir := filepath.Join(stateDir, "bin")
	foreignDir := filepath.Join(root, "foreign-program")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_METER_HOME", stateDir)

	executableName := "codex-meter"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	foreignExecutable := filepath.Join(foreignDir, executableName)
	pidPath := filepath.Join(stateDir, "codex-meter.pid")
	launcherPath := filepath.Join(stateDir, "codex-meter-start.vbs")
	files := map[string]string{
		foreignExecutable: "foreign-executable",
		pidPath:           "424242\n",
		launcherPath:      "foreign-launcher",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS == "linux" {
		configHome := filepath.Join(root, "config")
		t.Setenv("XDG_CONFIG_HOME", configHome)
		unitPath := filepath.Join(configHome, "systemd", "user", "codex-meter.service")
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(unitPath, []byte("foreign-unit"), 0o600); err != nil {
			t.Fatal(err)
		}
		files[unitPath] = "foreign-unit"
	}

	mutationCalls := 0
	originalRemovePersistence := removePreviousPersistence
	removePreviousPersistence = func(PreviousService) error {
		mutationCalls++
		return nil
	}
	t.Cleanup(func() { removePreviousPersistence = originalRemovePersistence })

	err := RemovePreviousService(PreviousService{
		StateDir:     stateDir,
		Executable:   foreignExecutable,
		InstallDir:   installDir,
		PIDPath:      pidPath,
		LauncherPath: launcherPath,
		StartupEntry: "CodexMeter",
		ServiceName:  "codex-meter.service",
	})
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected stable untrusted preflight rejection, got %v", err)
	}
	if mutationCalls != 0 {
		t.Fatalf("platform persistence mutation ran %d times before preflight rejection", mutationCalls)
	}
	for path, want := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != want {
			t.Fatalf("preflight changed %s: %q err=%v", path, data, readErr)
		}
	}
}

func TestRemovePreviousServiceRejectsForeignLinuxUnitBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd preflight")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "codex-meter")
	configHome := filepath.Join(root, "config")
	t.Setenv("CODEX_METER_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	previous, err := expectedPreviousService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.InstallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(configHome, "systemd", "user", previous.ServiceName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		previous.Executable: "legacy-executable",
		previous.PIDPath:    "424242\n",
		unitPath: `[Unit]
Description=foreign
[Service]
ExecStart="/foreign/codex-meter" daemon
`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mutationCalls := 0
	originalRemovePersistence := removePreviousPersistence
	removePreviousPersistence = func(PreviousService) error {
		mutationCalls++
		return nil
	}
	t.Cleanup(func() { removePreviousPersistence = originalRemovePersistence })

	err = RemovePreviousService(previous)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected unknown systemd target rejection, got %v", err)
	}
	if mutationCalls != 0 {
		t.Fatalf("systemd mutation hook ran %d times before rejection", mutationCalls)
	}
	for path, want := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != want {
			t.Fatalf("preflight changed %s: %q err=%v", path, data, readErr)
		}
	}
}

func TestRemovePreviousServiceValidatesAllLegacyMetadataBeforeMutation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "codex-meter")
	t.Setenv("CODEX_METER_HOME", stateDir)
	previous, err := expectedPreviousService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.InstallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		previous.Executable:   "legacy-executable",
		previous.PIDPath:      "424242\n",
		previous.LauncherPath: "legacy-launcher",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*PreviousService)
	}{
		{name: "state directory", mutate: func(value *PreviousService) { value.StateDir = filepath.Join(filepath.Dir(stateDir), "foreign-state") }},
		{name: "install directory", mutate: func(value *PreviousService) {
			value.InstallDir = filepath.Join(filepath.Dir(stateDir), "foreign-install")
		}},
		{name: "PID metadata", mutate: func(value *PreviousService) { value.PIDPath = filepath.Join(filepath.Dir(stateDir), "foreign.pid") }},
		{name: "launcher metadata", mutate: func(value *PreviousService) {
			value.LauncherPath = filepath.Join(filepath.Dir(stateDir), "foreign.vbs")
		}},
		{name: "startup identity", mutate: func(value *PreviousService) { value.StartupEntry = "ForeignStartup" }},
		{name: "service identity", mutate: func(value *PreviousService) { value.ServiceName = "foreign.service" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := previous
			test.mutate(&candidate)
			inspectCalls := 0
			mutationCalls := 0
			originalInspect := inspectPreviousPersistence
			originalRemove := removePreviousPersistence
			inspectPreviousPersistence = func(PreviousService) error {
				inspectCalls++
				return nil
			}
			removePreviousPersistence = func(PreviousService) error {
				mutationCalls++
				return nil
			}
			t.Cleanup(func() {
				inspectPreviousPersistence = originalInspect
				removePreviousPersistence = originalRemove
			})

			err := RemovePreviousService(candidate)
			if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
				t.Fatalf("expected %s rejection, got %v", test.name, err)
			}
			if inspectCalls != 0 || mutationCalls != 0 {
				t.Fatalf("platform hook ran before %s rejection: inspect=%d mutation=%d", test.name, inspectCalls, mutationCalls)
			}
			for path, want := range files {
				data, readErr := os.ReadFile(path)
				if readErr != nil || string(data) != want {
					t.Fatalf("preflight changed %s: %q err=%v", path, data, readErr)
				}
			}
		})
	}
}

func TestRemovePreviousServiceRejectsNonRegularMetadataBeforeMutation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "codex-meter")
	t.Setenv("CODEX_METER_HOME", stateDir)
	previous, err := expectedPreviousService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.InstallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.Executable, []byte("legacy-executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(previous.PIDPath, 0o700); err != nil {
		t.Fatal(err)
	}

	mutationCalls := 0
	originalInspect := inspectPreviousPersistence
	originalRemove := removePreviousPersistence
	inspectPreviousPersistence = func(PreviousService) error {
		mutationCalls++
		return nil
	}
	removePreviousPersistence = func(PreviousService) error {
		mutationCalls++
		return nil
	}
	t.Cleanup(func() {
		inspectPreviousPersistence = originalInspect
		removePreviousPersistence = originalRemove
	})

	err = RemovePreviousService(previous)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected non-regular metadata rejection, got %v", err)
	}
	if mutationCalls != 0 {
		t.Fatalf("platform hook ran %d times before non-regular metadata rejection", mutationCalls)
	}
	if _, statErr := os.Stat(previous.PIDPath); statErr != nil {
		t.Fatalf("non-regular metadata changed: %v", statErr)
	}
	data, readErr := os.ReadFile(previous.Executable)
	if readErr != nil || string(data) != "legacy-executable" {
		t.Fatalf("legacy executable changed: %q err=%v", data, readErr)
	}
}

func TestRemovePreviousServiceRemovesValidatedLegacyFilesAfterPersistence(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "codex-meter")
	t.Setenv("CODEX_METER_HOME", stateDir)
	previous, err := expectedPreviousService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.InstallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		previous.Executable:   "legacy-executable",
		previous.PIDPath:      "424242\n",
		previous.LauncherPath: "legacy-launcher",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(stateDir, "unrelated")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := make([]string, 0, 2)
	originalInspect := inspectPreviousPersistence
	originalRemove := removePreviousPersistence
	inspectPreviousPersistence = func(PreviousService) error {
		calls = append(calls, "inspect")
		return nil
	}
	removePreviousPersistence = func(PreviousService) error {
		calls = append(calls, "remove-persistence")
		return nil
	}
	t.Cleanup(func() {
		inspectPreviousPersistence = originalInspect
		removePreviousPersistence = originalRemove
	})

	if err := RemovePreviousService(previous); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "inspect,remove-persistence" {
		t.Fatalf("unexpected persistence order: %v", calls)
	}
	for _, path := range []string{previous.Executable, previous.PIDPath, previous.LauncherPath} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("validated legacy file still exists: %s err=%v", path, statErr)
		}
	}
	data, readErr := os.ReadFile(unrelated)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("unrelated file changed: %q err=%v", data, readErr)
	}
}

func TestLinuxInstallServiceRejectsForeignUnitBeforeHooks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit preflight")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	foreign := []byte("[Service]\nExecStart=/foreign/codex-usage daemon\n")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	hookCalls := 0
	restore := replaceLinuxServiceOperations(linuxServiceOperations{
		LookPath:              func(string) (string, error) { hookCalls++; return "", errors.New("unexpected") },
		RunSystemctl:          func(...string) ([]byte, error) { hookCalls++; return nil, nil },
		RunLoginctl:           func(...string) ([]byte, error) { hookCalls++; return nil, nil },
		StartDetached:         func(string, ...string) error { hookCalls++; return nil },
		ReadProcessExecutable: func(int) (string, error) { hookCalls++; return "", nil },
		OpenProcessHandle:     func(int) (int, error) { hookCalls++; return 0, nil },
		SignalProcessHandle:   func(int, os.Signal) error { hookCalls++; return nil },
		ProcessHandleExited:   func(int) (bool, error) { hookCalls++; return true, nil },
		CloseProcessHandle:    func(int) error { hookCalls++; return nil },
		Sleep:                 func(time.Duration) { hookCalls++ },
	})
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected foreign unit rejection, got %v", err)
	}
	if hookCalls != 0 {
		t.Fatalf("service hook ran %d times before unit rejection", hookCalls)
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != string(foreign) {
		t.Fatalf("foreign unit changed: %q err=%v", got, err)
	}
}

func TestLinuxInstallServiceRejectsSymlinkUnitWithoutFollowing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit preflight")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(unitPath))), "foreign-unit")
	if err := os.WriteFile(sentinel, []byte("foreign-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, unitPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	hookCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { hookCalls++; return "", errors.New("unexpected") }
	ops.RunSystemctl = func(...string) ([]byte, error) { hookCalls++; return nil, nil }
	ops.StartDetached = func(string, ...string) error { hookCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected symlink unit rejection, got %v", err)
	}
	if hookCalls != 0 {
		t.Fatalf("service hook ran %d times before symlink rejection", hookCalls)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "foreign-sentinel" {
		t.Fatalf("symlink target changed: %q err=%v", got, err)
	}
}

func TestLinuxInstallServiceRejectsSymlinkUnitAncestorBeforeHooks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit directory preflight")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	executable := filepath.Join(stateDir, "bin", "codex-usage")
	foreignConfig := filepath.Join(root, "foreign-config")
	linkedConfig := filepath.Join(root, "linked-config")
	t.Setenv("CODEX_USAGE_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", linkedConfig)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(foreignConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignConfig, linkedConfig); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	hookCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { hookCalls++; return "", errors.New("unexpected") }
	ops.RunSystemctl = func(...string) ([]byte, error) { hookCalls++; return nil, nil }
	ops.StartDetached = func(string, ...string) error { hookCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected linked unit ancestor rejection, got %v", err)
	}
	if hookCalls != 0 {
		t.Fatalf("service hook ran %d times before linked ancestor rejection", hookCalls)
	}
	entries, err := os.ReadDir(foreignConfig)
	if err != nil || len(entries) != 0 {
		t.Fatalf("linked foreign directory changed: entries=%v err=%v", entries, err)
	}
}

func TestLinuxInstallServiceRejectsExistingManagerStateBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd manager state preflight")
	}
	tests := []struct {
		name   string
		output []byte
	}{
		{
			name:   "vendor fragment",
			output: linuxSystemdSnapshotOutput("/usr/lib/systemd/user/codex-usage.service", "loaded", "enabled", "inactive", "no"),
		},
		{
			name:   "dangling wants link",
			output: linuxSystemdSnapshotOutput("", "not-found", "enabled", "inactive", "no"),
		},
		{
			name:   "active transient",
			output: linuxSystemdSnapshotOutput("/run/user/1000/systemd/transient/codex-usage.service", "loaded", "transient", "active", "yes"),
		},
		{
			name:   "malformed snapshot",
			output: []byte("FragmentPath=\nLoadState=not-found\nUnitFileState=\nActiveState=inactive\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executable, stateDir, unitPath := linuxServiceFixture(t)
			queryCalls, mutationCalls, startCalls, removeCalls, syncCalls := 0, 0, 0, 0, 0
			originalSyncParent := syncServiceParent
			syncServiceParent = func(string) error { syncCalls++; return nil }
			t.Cleanup(func() { syncServiceParent = originalSyncParent })
			ops := inertLinuxServiceOperations()
			ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
			ops.RunSystemctl = func(args ...string) ([]byte, error) {
				if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
					queryCalls++
					return tt.output, nil
				}
				mutationCalls++
				return nil, errors.New("unexpected systemctl mutation")
			}
			ops.StartDetached = func(string, ...string) error { startCalls++; return errors.New("unexpected detached start") }
			ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
			restore := replaceLinuxServiceOperations(ops)
			t.Cleanup(restore)

			_, err := InstallService(executable, stateDir)
			if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
				t.Fatalf("expected existing systemd state rejection, got %v", err)
			}
			if queryCalls != 1 || mutationCalls != 0 || startCalls != 0 || removeCalls != 0 || syncCalls != 0 {
				t.Fatalf("preflight side effects query=%d mutation=%d start=%d remove=%d sync=%d",
					queryCalls, mutationCalls, startCalls, removeCalls, syncCalls)
			}
			if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unit was written before manager state rejection: %v", statErr)
			}
		})
	}
}

func TestLinuxInstallServiceManagerStateQueryFailureHasNoSideEffects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd manager state query error")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	queryErr := errors.New("user bus permission denied")
	queryCalls, mutationCalls, startCalls, removeCalls, syncCalls := 0, 0, 0, 0, 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(string) error { syncCalls++; return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			queryCalls++
			return []byte("query failed"), queryErr
		}
		mutationCalls++
		return nil, errors.New("unexpected systemctl mutation")
	}
	ops.StartDetached = func(string, ...string) error { startCalls++; return errors.New("unexpected detached start") }
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, queryErr) || !strings.Contains(err.Error(), "permission_required") {
		t.Fatalf("manager state query error=%v want %v", err, queryErr)
	}
	if queryCalls != 1 || mutationCalls != 0 || startCalls != 0 || removeCalls != 0 || syncCalls != 0 {
		t.Fatalf("query failure side effects query=%d mutation=%d start=%d remove=%d sync=%d",
			queryCalls, mutationCalls, startCalls, removeCalls, syncCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit was written after manager state query failure: %v", statErr)
	}
}

func TestLinuxInstallServiceUserBusUnavailableUsesDetachedFallbackWithoutUnitMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux unavailable user bus fallback")
	}
	for _, existingUnit := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_unit_%t", existingUnit), func(t *testing.T) {
			executable, stateDir, unitPath := linuxServiceFixture(t)
			if existingUnit {
				writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
				writeLinuxPIDFixture(t, stateDir, 424242)
			}
			before, beforeErr := os.ReadFile(unitPath)
			if !existingUnit && !errors.Is(beforeErr, os.ErrNotExist) {
				t.Fatalf("unexpected unit precondition: %v", beforeErr)
			}
			queryErr := errors.New("exit status 1")
			queryCalls, mutationCalls, startCalls, removeCalls, syncCalls := 0, 0, 0, 0, 0
			originalSyncParent := syncServiceParent
			syncServiceParent = func(string) error { syncCalls++; return nil }
			t.Cleanup(func() { syncServiceParent = originalSyncParent })
			ops := inertLinuxServiceOperations()
			ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
			ops.RunSystemctl = func(args ...string) ([]byte, error) {
				if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
					queryCalls++
					return []byte("Failed to connect to bus: No medium found\n"), queryErr
				}
				mutationCalls++
				return nil, errors.New("unexpected systemctl mutation")
			}
			ops.StartDetached = func(got string, args ...string) error {
				startCalls++
				if got != executable || !reflect.DeepEqual(args, []string{"daemon"}) {
					t.Fatalf("unexpected detached start: %s %v", got, args)
				}
				return nil
			}
			ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
			ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
			restore := replaceLinuxServiceOperations(ops)
			t.Cleanup(restore)

			result, err := InstallService(executable, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Installed || !result.Started || result.Mode != ServiceModeDetachedFallback {
				t.Fatalf("unexpected fallback result: %+v", result)
			}
			if !strings.Contains(result.Warning, "Failed to connect to bus: No medium found") ||
				!strings.Contains(result.Warning, "未写入或修改 systemd unit") {
				t.Fatalf("fallback warning does not preserve the real reason: %q", result.Warning)
			}
			wantStarts := 1
			if existingUnit {
				wantStarts = 0
			}
			if queryCalls != 1 || mutationCalls != 0 || startCalls != wantStarts || removeCalls != 0 || syncCalls != 0 {
				t.Fatalf("fallback side effects query=%d mutation=%d start=%d remove=%d sync=%d",
					queryCalls, mutationCalls, startCalls, removeCalls, syncCalls)
			}
			if existingUnit {
				after, readErr := os.ReadFile(unitPath)
				if readErr != nil || !bytes.Equal(after, before) {
					t.Fatalf("owned unit changed during fallback: before=%q after=%q err=%v", before, after, readErr)
				}
			} else if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unit was written during bus fallback: %v", statErr)
			}
		})
	}
}

func TestLinuxInstallServiceTrulyAbsentStateInstallsAfterSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux absent systemd state installation")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	calls := make([]string, 0, 5)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error {
		if path != unitPath {
			t.Fatalf("unexpected sync path: %s", path)
		}
		calls = append(calls, "sync-created-unit")
		return nil
	}
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { calls = append(calls, "look:systemctl"); return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		switch command {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload", "--user enable --now codex-usage.service":
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %s", command)
			return nil, nil
		}
	}
	ops.RunLoginctl = func(...string) ([]byte, error) { return []byte("yes\n"), nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	result, err := InstallService(executable, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Started || result.Mode != ServiceModePersistent {
		t.Fatalf("unexpected service result: %+v", result)
	}
	wantCalls := []string{
		"look:systemctl",
		linuxSystemdSnapshotCommand,
		"sync-created-unit",
		"--user daemon-reload",
		"--user enable --now codex-usage.service",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("install calls=%v want %v", calls, wantCalls)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != currentSystemdUnitContents(executable, stateDir) {
		t.Fatalf("installed unit=%q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceWithoutSystemctlUsesDetachedFallbackWithoutUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux no-systemctl detached fallback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	startCalls, syncCalls := 0, 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(string) error { syncCalls++; return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("systemctl not installed") }
	ops.RunSystemctl = func(...string) ([]byte, error) { t.Fatal("systemctl called after LookPath failure"); return nil, nil }
	ops.StartDetached = func(got string, args ...string) error {
		startCalls++
		if got != executable || !reflect.DeepEqual(args, []string{"daemon"}) {
			t.Fatalf("unexpected detached start: %s %v", got, args)
		}
		return nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	result, err := InstallService(executable, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Started || result.Mode != ServiceModeDetachedFallback ||
		!strings.Contains(result.Warning, "未写入 systemd unit") {
		t.Fatalf("unexpected detached fallback result: %+v", result)
	}
	if startCalls != 1 || syncCalls != 0 {
		t.Fatalf("detached fallback calls start=%d sync=%d", startCalls, syncCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit was written without a systemd state snapshot: %v", statErr)
	}
}

func TestLinuxInstallServiceReusesExactUnitAfterPreflight(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit preflight")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	wantUnit := currentSystemdUnitContents(executable, stateDir)
	if err := os.WriteFile(unitPath, []byte(wantUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := make([]string, 0, 4)
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(name string) (string, error) { calls = append(calls, "look:"+name); return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if command == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		return nil, nil
	}
	ops.RunLoginctl = func(...string) ([]byte, error) { return []byte("yes\n"), nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	result, err := InstallService(executable, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Started || result.Mode != ServiceModePersistent {
		t.Fatalf("unexpected service result: %+v", result)
	}
	wantCalls := "look:systemctl," + linuxSystemdSnapshotCommand + ",--user daemon-reload,--user enable --now codex-usage.service"
	if strings.Join(calls, ",") != wantCalls {
		t.Fatalf("service calls=%q want %q", strings.Join(calls, ","), wantCalls)
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != wantUnit {
		t.Fatalf("exact unit changed: %q err=%v", got, err)
	}
}

func TestLinuxInstallServiceHardFailureRemovesUnitCreatedByCall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service installation rollback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	reloadErr := errors.New("daemon-reload failed")
	fallbackErr := errors.New("detached start failed")
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		if command == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		if command != "--user daemon-reload" {
			t.Fatalf("unexpected systemctl call: %v", args)
		}
		return []byte("reload failure"), reloadErr
	}
	ops.StartDetached = func(string, ...string) error { return fallbackErr }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, reloadErr) {
		t.Fatalf("hard install error=%v want %v", err, reloadErr)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit created by failed call remained: %v", statErr)
	}
}

func TestLinuxInstallServiceHardFailurePreservesPreexistingExactUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service installation rollback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	wantUnit := currentSystemdUnitContents(executable, stateDir)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(wantUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	reloadErr := errors.New("daemon-reload failed")
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		return nil, reloadErr
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); !errors.Is(err, reloadErr) {
		t.Fatalf("hard install error=%v want %v", err, reloadErr)
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != wantUnit {
		t.Fatalf("preexisting exact unit changed: %q err=%v", got, err)
	}
}

func TestLinuxInstallServiceEnableFailureRemovesUnitCreatedByCall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service installation rollback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	enableErr := errors.New("enable failed")
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		if command == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		if command == "--user daemon-reload" {
			return nil, nil
		}
		return []byte("enable failure"), enableErr
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, enableErr) {
		t.Fatalf("enable error=%v want %v", err, enableErr)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit created before failed enable remained: %v", statErr)
	}
}

func TestLinuxInstallServiceEnablePartialFailureRollsBackSystemdStateAndNewUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux partial systemd enable rollback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable returned failure after partial mutation")
	fallbackErr := errors.New("detached start failed")
	enabled, running := false, false
	calls := make([]string, 0, 7)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error {
		if path != unitPath {
			t.Fatalf("unexpected sync path: %s", path)
		}
		if _, err := os.Lstat(unitPath); err == nil {
			calls = append(calls, "sync-created-unit")
		} else if errors.Is(err, os.ErrNotExist) {
			calls = append(calls, "sync-removed-unit")
		} else {
			t.Fatalf("inspect unit during sync: %v", err)
		}
		return nil
	}
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		switch command {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			return nil, nil
		case "--user enable --now codex-usage.service":
			enabled, running = true, true
			return []byte("partial enable"), primaryErr
		case "--user disable --now codex-usage.service":
			if _, err := os.Lstat(unitPath); err != nil {
				t.Fatalf("disable ran after unit removal: %v", err)
			}
			enabled, running = false, false
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %s", command)
			return nil, nil
		}
	}
	ops.StartDetached = func(string, ...string) error { return fallbackErr }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("enable error=%v want %v", err, primaryErr)
	}
	if enabled || running {
		t.Fatalf("partial systemd state remained: enabled=%v running=%v", enabled, running)
	}
	wantCalls := []string{
		linuxSystemdSnapshotCommand,
		"sync-created-unit",
		"--user daemon-reload",
		"--user enable --now codex-usage.service",
		"--user disable --now codex-usage.service",
		"sync-removed-unit",
		"--user daemon-reload",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("rollback calls=%v want %v", calls, wantCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit created before partial enable failure remained: %v", statErr)
	}
}

func TestLinuxInstallServiceEnableRollbackDisableFailureIsReported(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux partial systemd disable error")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable failed")
	disableErr := errors.New("disable --now failed")
	disableCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			return nil, nil
		case "--user enable --now codex-usage.service":
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			return []byte("disable failure"), disableErr
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) || !errors.Is(err, disableErr) {
		t.Fatalf("combined error=%v primary=%v disable=%v", err, primaryErr, disableErr)
	}
	if disableCalls != 1 {
		t.Fatalf("disable --now calls=%d want 1", disableCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit remained after disable rollback error: %v", statErr)
	}
}

func TestLinuxInstallServiceEnableRollbackAggregatesAllCleanupErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd cleanup error aggregation")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable failed")
	disableErr := errors.New("disable failed")
	removeErr := errors.New("remove unit failed")
	syncErr := errors.New("sync unit parent failed")
	reloadErr := errors.New("rollback reload failed")
	syncCalls := 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error {
		if path != unitPath {
			t.Fatalf("unexpected sync path: %s", path)
		}
		syncCalls++
		if syncCalls == 2 {
			return syncErr
		}
		return nil
	}
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	disableCalls, removeCalls, reloadCalls := 0, 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			reloadCalls++
			if reloadCalls == 2 {
				return []byte("rollback reload failure"), reloadErr
			}
			return nil, nil
		case "--user enable --now codex-usage.service":
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			return []byte("disable failure"), disableErr
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.RemoveUnit = func(path string) error {
		removeCalls++
		if path != unitPath {
			t.Fatalf("unexpected remove path: %s", path)
		}
		return removeErr
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	for name, wantErr := range map[string]error{
		"primary": primaryErr,
		"disable": disableErr,
		"remove":  removeErr,
		"sync":    syncErr,
		"reload":  reloadErr,
	} {
		if !errors.Is(err, wantErr) {
			t.Errorf("%s error is not preserved in %v", name, err)
		}
	}
	if disableCalls != 1 || removeCalls != 1 || syncCalls != 2 || reloadCalls != 2 {
		t.Fatalf("cleanup calls disable=%d remove=%d sync=%d reload=%d", disableCalls, removeCalls, syncCalls, reloadCalls)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback detail is not recognizable: %v", err)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != currentSystemdUnitContents(executable, stateDir) {
		t.Fatalf("unit should remain after injected remove failure: %q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceEnableFailureDoesNotDisableOrDeletePreexistingExactUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux preexisting systemd unit rollback ownership")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	wantUnit := currentSystemdUnitContents(executable, stateDir)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(wantUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryErr := errors.New("enable failed after touching preexisting service")
	enabled, running := false, false
	disableCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		case "--user daemon-reload":
			return nil, nil
		case "--user enable --now codex-usage.service":
			enabled, running = true, true
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			enabled, running = false, false
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("enable error=%v want %v", err, primaryErr)
	}
	if disableCalls != 0 || !enabled || !running {
		t.Fatalf("preexisting service rollback changed state: disables=%d enabled=%v running=%v", disableCalls, enabled, running)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != wantUnit {
		t.Fatalf("preexisting exact unit changed: %q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceEnableFailureDoesNotDisableOrDeleteForeignReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux foreign systemd unit rollback ownership")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable failed after foreign replacement")
	foreignUnit := "[Service]\nExecStart=/foreign/codex-usage daemon\n"
	disableCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			return nil, nil
		case "--user enable --now codex-usage.service":
			if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("foreign replacement rollback error=%v", err)
	}
	if disableCalls != 0 {
		t.Fatalf("foreign replacement was disabled: calls=%d", disableCalls)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != foreignUnit {
		t.Fatalf("foreign replacement changed: %q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceRollbackMissingUnitStillDisablesAndReloads(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux missing unit rollback continuation")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable failed after removing unit")
	disableCalls, reloadCalls := 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			reloadCalls++
			return nil, nil
		case "--user enable --now codex-usage.service":
			if err := os.Remove(unitPath); err != nil {
				t.Fatal(err)
			}
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("enable error=%v want %v", err, primaryErr)
	}
	if disableCalls != 1 || reloadCalls != 2 {
		t.Fatalf("missing unit rollback calls disable=%d reload=%d", disableCalls, reloadCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit unexpectedly exists after rollback: %v", statErr)
	}
}

func TestLinuxInstallServiceRollbackRechecksUnitAfterDisableBeforeRemove(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux post-disable unit ownership recheck")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("enable failed")
	foreignUnit := "[Service]\nExecStart=/foreign/replacement daemon\n"
	disableCalls, removeCalls, reloadCalls := 0, 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return absentLinuxSystemdSnapshotOutput(), nil
		case "--user daemon-reload":
			reloadCalls++
			return nil, nil
		case "--user enable --now codex-usage.service":
			return nil, primaryErr
		case "--user disable --now codex-usage.service":
			disableCalls++
			if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("post-disable replacement error=%v", err)
	}
	if disableCalls != 1 || removeCalls != 0 || reloadCalls != 1 {
		t.Fatalf("post-disable replacement calls disable=%d remove=%d reload=%d", disableCalls, removeCalls, reloadCalls)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != foreignUnit {
		t.Fatalf("post-disable foreign replacement changed: %q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceRollbackDoesNotDeleteForeignUnitReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service installation rollback ownership")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	reloadErr := errors.New("daemon-reload failed after replacement")
	foreignUnit := "[Service]\nExecStart=/foreign/codex-usage daemon\n"
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil, reloadErr
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, reloadErr) || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("foreign unit rollback error=%v", err)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != foreignUnit {
		t.Fatalf("foreign unit replacement changed: %q err=%v", got, readErr)
	}
}

func TestLinuxInstallServiceActivationSyncFailureRollsBackNewUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit parent sync rollback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	syncErr := errors.New("activation parent sync failed")
	syncCalls := 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error {
		if path != unitPath {
			t.Fatalf("unexpected sync path: %s", path)
		}
		syncCalls++
		if syncCalls == 1 {
			return syncErr
		}
		return nil
	}
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		return nil, errors.New("unexpected systemctl mutation")
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("unexpected detached start") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, syncErr) {
		t.Fatalf("activation sync error=%v want %v", err, syncErr)
	}
	if syncCalls != 2 {
		t.Fatalf("activation and rollback sync calls=%d want 2", syncCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit remained after activation sync failure: %v", statErr)
	}
}

func TestLinuxInstallServiceRollbackSyncFailurePreservesPrimaryAndDetail(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd unit rollback sync error")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	primaryErr := errors.New("daemon-reload failed")
	cleanupErr := errors.New("rollback parent sync failed")
	syncCalls := 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error {
		if path != unitPath {
			t.Fatalf("unexpected sync path: %s", path)
		}
		syncCalls++
		if syncCalls == 2 {
			return cleanupErr
		}
		return nil
	}
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		return nil, primaryErr
	}
	ops.StartDetached = func(string, ...string) error { return errors.New("detached start failed") }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, primaryErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("combined install error=%v primary=%v cleanup=%v", err, primaryErr, cleanupErr)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback detail is not recognizable: %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("activation and rollback sync calls=%d want 2", syncCalls)
	}
	if _, statErr := os.Lstat(unitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unit remained after rollback sync failure: %v", statErr)
	}
}

func TestLinuxUninstallServiceRejectsUntrustedManagerBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall manager ownership preflight")
	}
	queryErr := errors.New("user bus query denied")
	tests := []struct {
		name      string
		output    func(string) []byte
		queryErr  error
		wantCode  string
		wantCause error
	}{
		{
			name: "vendor fragment",
			output: func(string) []byte {
				return linuxSystemdSnapshotOutput("/usr/lib/systemd/user/codex-usage.service", "loaded", "enabled", "active", "no")
			},
			wantCode: "existing_install_untrusted",
		},
		{
			name: "foreign fragment",
			output: func(string) []byte {
				return linuxSystemdSnapshotOutput("/tmp/foreign/codex-usage.service", "loaded", "enabled", "inactive", "no")
			},
			wantCode: "existing_install_untrusted",
		},
		{
			name: "transient manager unit",
			output: func(unitPath string) []byte {
				return linuxSystemdSnapshotOutput(unitPath, "loaded", "transient", "active", "yes")
			},
			wantCode: "existing_install_untrusted",
		},
		{
			name: "unloaded manager unit",
			output: func(unitPath string) []byte {
				return linuxSystemdSnapshotOutput(unitPath, "not-found", "enabled", "inactive", "no")
			},
			wantCode: "existing_install_untrusted",
		},
		{
			name:      "query failure",
			output:    func(string) []byte { return []byte("query denied") },
			queryErr:  queryErr,
			wantCode:  "permission_required",
			wantCause: queryErr,
		},
		{
			name:     "malformed snapshot",
			output:   func(string) []byte { return []byte("FragmentPath=/tmp/foreign\nLoadState=loaded\n") },
			wantCode: "existing_install_untrusted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executable, stateDir, unitPath := linuxServiceFixture(t)
			writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
			queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls := 0, 0, 0, 0, 0
			originalSyncParent := syncServiceParent
			syncServiceParent = func(string) error { syncCalls++; return nil }
			t.Cleanup(func() { syncServiceParent = originalSyncParent })
			ops := inertLinuxServiceOperations()
			ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
			ops.RunSystemctl = func(args ...string) ([]byte, error) {
				if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
					queryCalls++
					return tt.output(unitPath), tt.queryErr
				}
				mutationCalls++
				return nil, nil
			}
			ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
			ops.ReadProcessExecutable = func(int) (string, error) { t.Fatal("PID identity read before manager rejection"); return "", nil }
			ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
			restore := replaceLinuxServiceOperations(ops)
			t.Cleanup(restore)

			err := UninstallService(executable, stateDir)
			if err == nil || !strings.Contains(err.Error(), tt.wantCode) {
				t.Fatalf("manager preflight error=%v want code %q", err, tt.wantCode)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("manager preflight error=%v want cause %v", err, tt.wantCause)
			}
			if queryCalls != 1 || mutationCalls != 0 || removeCalls != 0 || syncCalls != 0 || signalCalls != 0 {
				t.Fatalf("unsafe preflight calls query=%d mutation=%d remove=%d sync=%d signal=%d",
					queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls)
			}
			if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != currentSystemdUnitContents(executable, stateDir) {
				t.Fatalf("owned unit changed before manager rejection: %q err=%v", got, readErr)
			}
		})
	}
}

func TestLinuxUninstallServiceRejectsUntrustedPIDBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall PID ownership preflight")
	}
	tests := []struct {
		name          string
		prepare       func(*testing.T, string, string)
		afterSnapshot func(*testing.T, string)
		observedExe   func(string) string
	}{
		{
			name: "symlink metadata",
			prepare: func(t *testing.T, stateDir, pidPath string) {
				sentinel := filepath.Join(filepath.Dir(stateDir), "foreign.pid")
				if err := os.WriteFile(sentinel, []byte("424242\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, pidPath); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
		{
			name: "nonregular metadata",
			prepare: func(t *testing.T, _, pidPath string) {
				if err := os.Mkdir(pidPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid metadata",
			prepare: func(t *testing.T, _, pidPath string) {
				if err := os.WriteFile(pidPath, []byte("not-a-pid\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "foreign executable",
			prepare: func(t *testing.T, _, pidPath string) {
				if err := os.WriteFile(pidPath, []byte("424242\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			observedExe: func(stateDir string) string { return filepath.Join(filepath.Dir(stateDir), "foreign", "codex-usage") },
		},
		{
			name: "linked ancestor",
			prepare: func(t *testing.T, _, pidPath string) {
				if err := os.WriteFile(pidPath, []byte("424242\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			afterSnapshot: func(t *testing.T, stateDir string) {
				realState := stateDir + "-owned"
				if err := os.Rename(stateDir, realState); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realState, stateDir); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executable, stateDir, unitPath := linuxServiceFixture(t)
			writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
			pidPath := filepath.Join(stateDir, "codex-usage.pid")
			tt.prepare(t, stateDir, pidPath)
			queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls := 0, 0, 0, 0, 0
			originalSyncParent := syncServiceParent
			syncServiceParent = func(string) error { syncCalls++; return nil }
			t.Cleanup(func() { syncServiceParent = originalSyncParent })
			ops := inertLinuxServiceOperations()
			ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
			ops.RunSystemctl = func(args ...string) ([]byte, error) {
				if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
					queryCalls++
					if tt.afterSnapshot != nil {
						tt.afterSnapshot(t, stateDir)
					}
					return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
				}
				mutationCalls++
				return nil, nil
			}
			ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
			ops.ReadProcessExecutable = func(int) (string, error) {
				if tt.observedExe != nil {
					return tt.observedExe(stateDir), nil
				}
				return executable, nil
			}
			ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
			restore := replaceLinuxServiceOperations(ops)
			t.Cleanup(restore)

			err := UninstallService(executable, stateDir)
			if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
				t.Fatalf("PID preflight error=%v want existing_install_untrusted", err)
			}
			if queryCalls != 1 || mutationCalls != 0 || removeCalls != 0 || syncCalls != 0 || signalCalls != 0 {
				t.Fatalf("unsafe PID preflight calls query=%d mutation=%d remove=%d sync=%d signal=%d",
					queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls)
			}
			if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != currentSystemdUnitContents(executable, stateDir) {
				t.Fatalf("owned unit changed before PID rejection: %q err=%v", got, readErr)
			}
			if _, statErr := os.Lstat(pidPath); statErr != nil {
				t.Fatalf("PID metadata changed before rejection: %v", statErr)
			}
		})
	}
}

func TestLinuxUninstallServiceAcceptsExactManagerSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall exact manager ownership")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	queryCalls, removeCalls := 0, 0
	commands := make([]string, 0, 3)
	syncPaths := make([]string, 0, 1)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error { syncPaths = append(syncPaths, path); return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		if command == linuxSystemdSnapshotCommand {
			queryCalls++
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		commands = append(commands, command)
		return nil, nil
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if queryCalls != 1 || removeCalls != 1 || !reflect.DeepEqual(syncPaths, []string{unitPath}) {
		t.Fatalf("exact uninstall calls query=%d remove=%d sync=%v", queryCalls, removeCalls, syncPaths)
	}
	wantCommands := []string{"--user stop codex-usage.service", "--user disable codex-usage.service", "--user daemon-reload"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("systemctl commands=%v want %v", commands, wantCommands)
	}
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned unit remains after uninstall: %v", err)
	}
}

func TestLinuxUninstallServiceManagerOwnedPIDUsesSystemctlWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager-owned PID uninstall")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 4)
	stopped, signalCalls := false, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		switch command {
		case linuxSystemdSnapshotCommand:
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		case "--user stop codex-usage.service":
			stopped = true
			return nil, nil
		case "--user disable codex-usage.service", "--user daemon-reload":
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl command: %s", command)
			return nil, nil
		}
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		if stopped {
			return "", os.ErrNotExist
		}
		return executable, nil
	}
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{
		linuxSystemdSnapshotCommand,
		"--user stop codex-usage.service",
		"--user disable codex-usage.service",
		"--user daemon-reload",
	}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 0 {
		t.Fatalf("manager-owned uninstall commands=%v signal=%d want commands=%v signal=0", commands, signalCalls, wantCommands)
	}
	for _, path := range []string{unitPath, pidPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned metadata remains after uninstall: %s: %v", path, err)
		}
	}
}

func TestLinuxUninstallServiceManagerOwnedPIDSignalsResidualAfterSystemctlStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager-owned residual PID uninstall")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 4)
	signaled, signalCalls, processProbes := false, 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		switch command {
		case linuxSystemdSnapshotCommand:
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		case "--user stop codex-usage.service", "--user disable codex-usage.service", "--user daemon-reload":
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl command: %s", command)
			return nil, nil
		}
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		if signaled {
			return "", os.ErrNotExist
		}
		return executable, nil
	}
	ops.SignalProcessHandle = func(pid int, signal os.Signal) error {
		signalCalls++
		if pid != 424242 || signal != syscall.SIGTERM {
			t.Fatalf("unexpected signal target: pid=%d signal=%v", pid, signal)
		}
		signaled = true
		return nil
	}
	ops.ProcessHandleExited = func(int) (bool, error) {
		processProbes++
		return signaled, nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{
		linuxSystemdSnapshotCommand,
		"--user stop codex-usage.service",
		"--user disable codex-usage.service",
		"--user daemon-reload",
	}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 1 || processProbes == 0 {
		t.Fatalf("residual uninstall commands=%v signal=%d probes=%d want commands=%v signal=1 bounded wait",
			commands, signalCalls, processProbes, wantCommands)
	}
	for _, path := range []string{unitPath, pidPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned metadata remains after residual uninstall: %s: %v", path, err)
		}
	}
}

func TestLinuxUninstallServiceRejectsLoadedManagerWhenLocalUnitIsMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall missing local unit manager ownership")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls := 0, 0, 0, 0, 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(string) error { syncCalls++; return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			queryCalls++
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		mutationCalls++
		return nil, nil
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	ops.ReadProcessExecutable = func(int) (string, error) {
		t.Fatal("PID identity read after missing metadata")
		return "", nil
	}
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := UninstallService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("missing local unit manager error=%v want existing_install_untrusted", err)
	}
	if queryCalls != 1 || mutationCalls != 0 || removeCalls != 0 || syncCalls != 0 || signalCalls != 0 {
		t.Fatalf("unsafe missing-unit calls query=%d mutation=%d remove=%d sync=%d signal=%d",
			queryCalls, mutationCalls, removeCalls, syncCalls, signalCalls)
	}
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing local unit unexpectedly created or changed: %v", err)
	}
	if _, err := os.Lstat(executable); err != nil {
		t.Fatalf("executable changed before manager ownership rejection: %v", err)
	}
}

func TestLinuxUninstallServiceUnitMissingStopsValidatedPIDWhenManagerIsAbsent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux detached uninstall with absent manager state")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	lookCalls, queryCalls, mutationCalls, signalCalls := 0, 0, 0, 0
	syncPaths := make([]string, 0, 1)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error { syncPaths = append(syncPaths, path); return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { lookCalls++; return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			queryCalls++
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		mutationCalls++
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if lookCalls != 1 || queryCalls != 1 || mutationCalls != 0 || signalCalls != 1 || !reflect.DeepEqual(syncPaths, []string{pidPath}) {
		t.Fatalf("detached uninstall calls look=%d query=%d mutation=%d signal=%d sync=%v",
			lookCalls, queryCalls, mutationCalls, signalCalls, syncPaths)
	}
	if _, err := os.Lstat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned PID metadata remains after detached uninstall: %v", err)
	}
}

func TestLinuxUninstallServiceWithoutSystemctlUsesValidatedPIDFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall without systemctl")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	lookCalls, systemctlCalls, removeCalls, signalCalls := 0, 0, 0, 0
	syncPaths := make([]string, 0, 2)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error { syncPaths = append(syncPaths, path); return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { lookCalls++; return "", errors.New("systemctl unavailable") }
	ops.RunSystemctl = func(...string) ([]byte, error) { systemctlCalls++; return nil, errors.New("unexpected systemctl") }
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if lookCalls != 1 || systemctlCalls != 0 || removeCalls != 1 || signalCalls != 1 {
		t.Fatalf("fallback calls look=%d systemctl=%d remove=%d signal=%d", lookCalls, systemctlCalls, removeCalls, signalCalls)
	}
	if !reflect.DeepEqual(syncPaths, []string{unitPath, pidPath}) {
		t.Fatalf("fallback sync paths=%v want [%s %s]", syncPaths, unitPath, pidPath)
	}
	for _, path := range []string{unitPath, pidPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned metadata remains after fallback uninstall: %s: %v", path, err)
		}
	}
}

func TestLinuxUninstallServiceUserBusUnavailableUsesValidatedPIDFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux unavailable user bus uninstall fallback")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	queryCalls, mutationCalls, removeCalls, signalCalls := 0, 0, 0, 0
	syncPaths := make([]string, 0, 2)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error { syncPaths = append(syncPaths, path); return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			queryCalls++
			return []byte("Failed to connect to bus: No such file or directory\n"), errors.New("exit status 1")
		}
		mutationCalls++
		return nil, errors.New("unexpected systemctl mutation")
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if queryCalls != 1 || mutationCalls != 0 || removeCalls != 1 || signalCalls != 1 {
		t.Fatalf("bus fallback calls query=%d mutation=%d remove=%d signal=%d",
			queryCalls, mutationCalls, removeCalls, signalCalls)
	}
	if !reflect.DeepEqual(syncPaths, []string{unitPath, pidPath}) {
		t.Fatalf("bus fallback sync paths=%v want [%s %s]", syncPaths, unitPath, pidPath)
	}
	for _, path := range []string{unitPath, pidPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned metadata remains after bus fallback: %s: %v", path, err)
		}
	}
}

func TestLinuxUninstallServiceAbsentManagerStateUsesDetachedFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall with absent manager state")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 2)
	removeCalls, signalCalls := 0, 0
	syncPaths := make([]string, 0, 2)
	originalSyncParent := syncServiceParent
	syncServiceParent = func(path string) error { syncPaths = append(syncPaths, path); return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if command == linuxSystemdSnapshotCommand {
			return absentLinuxSystemdSnapshotOutput(), nil
		}
		if command != "--user daemon-reload" {
			t.Fatalf("unexpected systemctl mutation for absent manager state: %s", command)
		}
		return nil, nil
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{linuxSystemdSnapshotCommand, "--user daemon-reload"}
	if !reflect.DeepEqual(commands, wantCommands) || removeCalls != 1 || signalCalls != 1 {
		t.Fatalf("absent manager calls commands=%v remove=%d signal=%d", commands, removeCalls, signalCalls)
	}
	if !reflect.DeepEqual(syncPaths, []string{unitPath, pidPath}) {
		t.Fatalf("absent manager sync paths=%v want [%s %s]", syncPaths, unitPath, pidPath)
	}
}

func TestLinuxUninstallServiceInactiveOwnedUnitStopsDetachedPIDAfterSystemctlStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inactive unit detached PID uninstall")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 4)
	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if command == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "inactive", "no"), nil
		}
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(pid int, signal os.Signal) error {
		signalCalls++
		if pid != 424242 || signal != syscall.SIGTERM {
			t.Fatalf("unexpected signal target: pid=%d signal=%v", pid, signal)
		}
		return nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := UninstallService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{
		linuxSystemdSnapshotCommand,
		"--user stop codex-usage.service",
		"--user disable codex-usage.service",
		"--user daemon-reload",
	}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 1 {
		t.Fatalf("uninstall commands=%v signal=%d want commands=%v signal=1", commands, signalCalls, wantCommands)
	}
	if _, err := os.Lstat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached PID metadata remains after uninstall: %v", err)
	}
}

func TestLinuxStopServiceInactiveOwnedUnitStopsDetachedPIDAfterSystemctlStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux inactive unit detached PID stop")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 2)
	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if command == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "inactive", "no"), nil
		}
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{linuxSystemdSnapshotCommand, "--user stop codex-usage.service"}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 1 {
		t.Fatalf("stop commands=%v signal=%d want commands=%v signal=1", commands, signalCalls, wantCommands)
	}
}

func TestLinuxStopServiceManagerOwnedPIDUsesSystemctlWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager-owned PID stop")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 2)
	stopped, signalCalls := false, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if command == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		if command == "--user stop codex-usage.service" {
			stopped = true
			return nil, nil
		}
		t.Fatalf("unexpected systemctl command: %s", command)
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		if stopped {
			return "", os.ErrNotExist
		}
		return executable, nil
	}
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{linuxSystemdSnapshotCommand, "--user stop codex-usage.service"}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 0 {
		t.Fatalf("manager-owned stop commands=%v signal=%d want commands=%v signal=0", commands, signalCalls, wantCommands)
	}
}

func TestLinuxStopServiceManagerOwnedPIDSignalsResidualAfterSystemctlStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager-owned residual PID stop")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)
	commands := make([]string, 0, 2)
	signaled, signalCalls, processProbes := false, 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if command == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		}
		if command != "--user stop codex-usage.service" {
			t.Fatalf("unexpected systemctl command: %s", command)
		}
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(pid int, signal os.Signal) error {
		signalCalls++
		if pid != 424242 || signal != syscall.SIGTERM {
			t.Fatalf("unexpected signal target: pid=%d signal=%v", pid, signal)
		}
		signaled = true
		return nil
	}
	ops.ProcessHandleExited = func(int) (bool, error) {
		processProbes++
		return signaled, nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{linuxSystemdSnapshotCommand, "--user stop codex-usage.service"}
	if !reflect.DeepEqual(commands, wantCommands) || signalCalls != 1 || processProbes == 0 {
		t.Fatalf("residual stop commands=%v signal=%d probes=%d want commands=%v signal=1 bounded wait",
			commands, signalCalls, processProbes, wantCommands)
	}
}

func TestLinuxStopServiceRejectsPostStopPIDReuseWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux post-stop PID reuse")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)
	stopped, signalCalls := false, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		case "--user stop codex-usage.service":
			stopped = true
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl command: %v", args)
			return nil, nil
		}
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		if stopped {
			return filepath.Join(filepath.Dir(stateDir), "foreign", "codex-usage"), nil
		}
		return executable, nil
	}
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("post-stop PID reuse error=%v want existing_install_untrusted", err)
	}
	if signalCalls != 0 {
		t.Fatalf("post-stop foreign PID was signaled %d times", signalCalls)
	}
}

func TestLinuxStopServiceRejectsForeignPIDBeforeSystemctlStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux foreign detached PID stop preflight")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)
	mutationCalls, signalCalls := 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == linuxSystemdSnapshotCommand {
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "inactive", "no"), nil
		}
		mutationCalls++
		return nil, nil
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		return filepath.Join(filepath.Dir(stateDir), "foreign", "codex-usage"), nil
	}
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("foreign PID error=%v want existing_install_untrusted", err)
	}
	if mutationCalls != 0 || signalCalls != 0 {
		t.Fatalf("foreign PID caused mutations=%d signals=%d", mutationCalls, signalCalls)
	}
}

func TestLinuxUninstallServiceRechecksUnitAfterStopBeforeDisableOrRemove(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux uninstall replacement recheck")
	}
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	foreignUnit := "[Service]\nExecStart=/foreign/codex-usage daemon\n"
	stopCalls, disableCalls, removeCalls, syncCalls := 0, 0, 0, 0
	originalSyncParent := syncServiceParent
	syncServiceParent = func(string) error { syncCalls++; return nil }
	t.Cleanup(func() { syncServiceParent = originalSyncParent })
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case linuxSystemdSnapshotCommand:
			return linuxSystemdSnapshotOutput(unitPath, "loaded", "enabled", "active", "no"), nil
		case "--user stop codex-usage.service":
			stopCalls++
			if err := os.WriteFile(unitPath, []byte(foreignUnit), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, nil
		case "--user disable codex-usage.service":
			disableCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl call: %v", args)
			return nil, nil
		}
	}
	ops.RemoveUnit = func(path string) error { removeCalls++; return os.Remove(path) }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := UninstallService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("replacement error=%v want existing_install_untrusted", err)
	}
	if stopCalls != 1 || disableCalls != 0 || removeCalls != 0 || syncCalls != 0 {
		t.Fatalf("replacement calls stop=%d disable=%d remove=%d sync=%d", stopCalls, disableCalls, removeCalls, syncCalls)
	}
	if got, readErr := os.ReadFile(unitPath); readErr != nil || string(got) != foreignUnit {
		t.Fatalf("foreign unit replacement changed: %q err=%v", got, readErr)
	}
}

func TestLinuxStopServiceRejectsPIDExecutableMismatchWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PID identity")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)
	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.ReadProcessExecutable = func(int) (string, error) { return filepath.Join(t.TempDir(), "codex-usage"), nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected PID executable mismatch rejection, got %v", err)
	}
	if signalCalls != 0 {
		t.Fatalf("signal hook ran %d times for mismatched executable", signalCalls)
	}
}

func TestLinuxStopServicePropagatesSignalFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PID signal failure")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)
	wantErr := errors.New("signal failed")
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { return wantErr }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); !errors.Is(err, wantErr) {
		t.Fatalf("signal error=%v want %v", err, wantErr)
	}
}

func TestLinuxStopServiceFailsWhenProcessRemainsAlive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PID termination wait")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)
	sleeps := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { return nil }
	ops.ProcessHandleExited = func(int) (bool, error) { return false, nil }
	ops.Sleep = func(time.Duration) { sleeps++ }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected process exit timeout, got %v", err)
	}
	if sleeps == 0 {
		t.Fatal("bounded wait did not poll process exit")
	}
}

func TestLinuxStopServiceRejectsSymlinkPIDWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux safe PID metadata")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	sentinel := filepath.Join(filepath.Dir(stateDir), "foreign.pid")
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	if err := os.WriteFile(sentinel, []byte("424242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, pidPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected symlink PID rejection, got %v", err)
	}
	if signalCalls != 0 {
		t.Fatalf("signal hook ran %d times for symlink PID", signalCalls)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "424242\n" {
		t.Fatalf("PID symlink target changed: %q err=%v", got, err)
	}
}

func TestLinuxStopServiceSignalsExactPIDAndWaitsForExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux exact PID stop")
	}
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)
	signals := 0
	probes := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.SignalProcessHandle = func(pid int, signal os.Signal) error {
		signals++
		if pid != 424242 || fmt.Sprint(signal) != "terminated" {
			t.Fatalf("unexpected signal target: pid=%d signal=%v", pid, signal)
		}
		return nil
	}
	ops.ProcessHandleExited = func(int) (bool, error) {
		probes++
		return probes != 1, nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if signals != 1 || probes != 2 {
		t.Fatalf("stop sequence signals=%d probes=%d", signals, probes)
	}
}

func TestLinuxSuspendPreviousServiceRejectsPIDExecutableMismatchWithoutSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux previous PID identity")
	}
	root := t.TempDir()
	t.Setenv("CODEX_METER_HOME", filepath.Join(root, "codex-meter"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	previous, err := expectedPreviousService()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.InstallDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.Executable, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.PIDPath, []byte("424242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	ops.ReadProcessExecutable = func(int) (string, error) { return filepath.Join(root, "foreign", "codex-meter"), nil }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err = SuspendPreviousService(previous)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected previous PID executable mismatch rejection, got %v", err)
	}
	if signalCalls != 0 {
		t.Fatalf("signal hook ran %d times for mismatched previous executable", signalCalls)
	}
}

func linuxServiceFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	executable := filepath.Join(stateDir, "bin", "codex-usage")
	configHome := filepath.Join(root, "config")
	t.Setenv("CODEX_USAGE_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable, stateDir, filepath.Join(configHome, "systemd", "user", "codex-usage.service")
}

func writeLinuxPIDFixture(t *testing.T, stateDir string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "codex-usage.pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExactLinuxUnitFixture(t *testing.T, unitPath, executable, stateDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(currentSystemdUnitContents(executable, stateDir)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func inertLinuxServiceOperations() linuxServiceOperations {
	processHandleProbes := 0
	return linuxServiceOperations{
		LookPath:              func(string) (string, error) { return "", errors.New("not found") },
		RunSystemctl:          func(...string) ([]byte, error) { return nil, errors.New("unexpected systemctl") },
		RunLoginctl:           func(...string) ([]byte, error) { return []byte("yes\n"), nil },
		StartDetached:         func(string, ...string) error { return nil },
		RemoveUnit:            os.Remove,
		ReadProcessExecutable: func(int) (string, error) { return "", errors.New("unexpected process read") },
		OpenProcessHandle:     func(pid int) (int, error) { return pid, nil },
		SignalProcessHandle:   func(int, os.Signal) error { return errors.New("unexpected signal") },
		ProcessHandleExited: func(int) (bool, error) {
			processHandleProbes++
			return processHandleProbes > 1, nil
		},
		CloseProcessHandle: func(int) error { return nil },
		Sleep:              func(time.Duration) {},
	}
}

func replaceLinuxServiceOperations(operations linuxServiceOperations) func() {
	original := activeLinuxServiceOperations
	activeLinuxServiceOperations = operations
	return func() { activeLinuxServiceOperations = original }
}

const linuxSystemdSnapshotCommand = "--user show codex-usage.service --property=FragmentPath --property=LoadState --property=UnitFileState --property=ActiveState --property=Transient --all --no-pager"

func linuxSystemdSnapshotOutput(fragmentPath, loadState, unitFileState, activeState, transient string) []byte {
	return []byte(fmt.Sprintf(
		"FragmentPath=%s\nLoadState=%s\nUnitFileState=%s\nActiveState=%s\nTransient=%s\n",
		fragmentPath, loadState, unitFileState, activeState, transient,
	))
}

func absentLinuxSystemdSnapshotOutput() []byte {
	return linuxSystemdSnapshotOutput("", "not-found", "", "inactive", "no")
}
