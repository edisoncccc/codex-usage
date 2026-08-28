//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsHelperEnvironment = "CODEX_USAGE_WINDOWS_PROCESS_HELPER"

func TestWindowsProcessHelper(t *testing.T) {
	if os.Getenv(windowsHelperEnvironment) != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestStopWindowsExecutableFindsProcessWithoutPIDFile(t *testing.T) {
	target, command := startWindowsProcessHelper(t)
	waitForWindowsProcessPath(t, command.Process.Pid, target)

	if err := stopWindowsExecutable(target); err != nil {
		t.Fatal(err)
	}
	waitForWindowsProcessExit(t, command)
}

func TestStopWindowsExecutableDoesNotStopSameNameAtAnotherPath(t *testing.T) {
	target, targetCommand := startWindowsProcessHelper(t)
	otherTarget, otherCommand := startWindowsProcessHelper(t)
	waitForWindowsProcessPath(t, targetCommand.Process.Pid, target)
	waitForWindowsProcessPath(t, otherCommand.Process.Pid, otherTarget)

	if err := stopWindowsExecutable(target); err != nil {
		t.Fatal(err)
	}
	waitForWindowsProcessExit(t, targetCommand)
	if path, err := windowsProcessExecutable(uint32(otherCommand.Process.Pid)); err != nil ||
		!strings.EqualFold(filepath.Clean(path), filepath.Clean(otherTarget)) {
		t.Fatalf("same-name process at another path was stopped: path=%q err=%v", path, err)
	}
	if err := stopWindowsExecutable(otherTarget); err != nil {
		t.Fatal(err)
	}
	waitForWindowsProcessExit(t, otherCommand)
}

func TestUninstallServicePreservesMetadataWhenStopFails(t *testing.T) {
	stateDir := t.TempDir()
	target := filepath.Join(stateDir, "bin", "codex-usage.exe")
	command := startWindowsProcessHelperAt(t, target)
	waitForWindowsProcessPath(t, command.Process.Pid, target)
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	launcherPath := filepath.Join(stateDir, "codex-usage-start.vbs")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", command.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcherPath, []byte(windowsServiceLauncherContents(target, stateDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	deleteCalls := 0
	restoreServiceHooks := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return windowsServiceRunCommand(launcherPath), true, nil },
		func(string) error { return nil },
		func() error { deleteCalls++; return nil },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restoreServiceHooks)

	originalTerminate := terminateWindowsProcessByPID
	terminateWindowsProcessByPID = func(uint32) error { return windows.ERROR_ACCESS_DENIED }
	err := UninstallService(target, stateDir)
	terminateWindowsProcessByPID = originalTerminate
	if err == nil || !strings.Contains(err.Error(), "permission_required") {
		t.Fatalf("expected stable permission error, got %v", err)
	}
	if strings.Contains(err.Error(), "管理员") || strings.Contains(strings.ToLower(err.Error()), "admin") {
		t.Fatalf("permission error recommended elevation: %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("registry entry was removed after stop failure: calls=%d", deleteCalls)
	}
	for _, path := range []string{pidPath, launcherPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("metadata %s was removed after stop failure: %v", path, statErr)
		}
	}
	if err := stopWindowsExecutable(target); err != nil {
		t.Fatal(err)
	}
	waitForWindowsProcessExit(t, command)
}

func TestValidateWindowsServiceInstallTargetRejectsForeignPath(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "codex-usage")
	canonical := filepath.Join(stateDir, "bin", "codex-usage.exe")
	if err := validateWindowsServiceInstallTarget(canonical, stateDir); err != nil {
		t.Fatalf("canonical override target rejected: %v", err)
	}
	foreign := filepath.Join(root, "foreign", "codex-usage.exe")
	if err := validateWindowsServiceInstallTarget(foreign, stateDir); err == nil {
		t.Fatal("expected foreign service target rejection")
	}
}

func TestWindowsPermissionErrorDoesNotRecommendElevation(t *testing.T) {
	err := windowsProcessError("停止", 42, windows.ERROR_ACCESS_DENIED)
	if !strings.Contains(err.Error(), "permission_required") {
		t.Fatalf("missing stable permission code: %v", err)
	}
	if strings.Contains(err.Error(), "管理员") || strings.Contains(strings.ToLower(err.Error()), "admin") {
		t.Fatalf("permission error recommended elevation: %v", err)
	}
}

func TestRemovePreviousServiceRejectsUnknownRunTargetBeforeMutation(t *testing.T) {
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
		previous.LauncherPath: previousWindowsLauncher(previous),
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	deleteCalls := 0
	terminateCalls := 0
	originalReadRun := readPreviousWindowsRunValue
	originalDeleteRun := deletePreviousWindowsRunValue
	originalTerminate := terminateWindowsProcessByPID
	readPreviousWindowsRunValue = func(string) (string, bool, error) {
		return `wscript.exe //B //Nologo "C:\foreign\launcher.vbs"`, true, nil
	}
	deletePreviousWindowsRunValue = func(string) error {
		deleteCalls++
		return nil
	}
	terminateWindowsProcessByPID = func(uint32) error {
		terminateCalls++
		return nil
	}
	t.Cleanup(func() {
		readPreviousWindowsRunValue = originalReadRun
		deletePreviousWindowsRunValue = originalDeleteRun
		terminateWindowsProcessByPID = originalTerminate
	})

	err = RemovePreviousService(previous)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected unknown run target rejection, got %v", err)
	}
	if deleteCalls != 0 || terminateCalls != 0 {
		t.Fatalf("mutation hooks ran before rejection: registry=%d process=%d", deleteCalls, terminateCalls)
	}
	for path, want := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != want {
			t.Fatalf("preflight changed %s: %q err=%v", path, data, readErr)
		}
	}
}

func TestSuspendPreviousServiceRejectsUnknownRunTargetBeforeStoppingProcess(t *testing.T) {
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
		previous.LauncherPath: previousWindowsLauncher(previous),
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stopCalls := 0
	originalReadRun := readPreviousWindowsRunValue
	originalStop := stopPreviousWindowsServiceProcess
	readPreviousWindowsRunValue = func(string) (string, bool, error) {
		return `wscript.exe //B //Nologo "C:\foreign\legacy.vbs"`, true, nil
	}
	stopPreviousWindowsServiceProcess = func(string) error {
		stopCalls++
		return nil
	}
	t.Cleanup(func() {
		readPreviousWindowsRunValue = originalReadRun
		stopPreviousWindowsServiceProcess = originalStop
	})

	err = SuspendPreviousService(previous)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected full persistence preflight rejection, got %v", err)
	}
	if stopCalls != 0 {
		t.Fatalf("process stop ran %d times before persistence rejection", stopCalls)
	}
}

func TestInstallServiceRejectsForeignLauncherBeforeMutation(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	if err := os.WriteFile(launcher, []byte("foreign-launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	readCalls, writeCalls, deleteCalls, startCalls := 0, 0, 0, 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { readCalls++; return "", false, nil },
		func(string) error { writeCalls++; return nil },
		func() error { deleteCalls++; return nil },
		func(string, ...string) error { startCalls++; return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected foreign launcher rejection, got %v", err)
	}
	if readCalls != 0 || writeCalls != 0 || deleteCalls != 0 || startCalls != 0 {
		t.Fatalf("hooks ran before launcher rejection: read=%d write=%d delete=%d start=%d", readCalls, writeCalls, deleteCalls, startCalls)
	}
	if got, err := os.ReadFile(launcher); err != nil || string(got) != "foreign-launcher" {
		t.Fatalf("foreign launcher changed: %q err=%v", got, err)
	}
}

func TestInstallServiceRejectsForeignRunEntryBeforeCreatingLauncher(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	writeCalls, deleteCalls, startCalls := 0, 0, 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return `wscript.exe //B //Nologo "C:\foreign\service.vbs"`, true, nil },
		func(string) error { writeCalls++; return nil },
		func() error { deleteCalls++; return nil },
		func(string, ...string) error { startCalls++; return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected foreign HKCU target rejection, got %v", err)
	}
	if writeCalls != 0 || deleteCalls != 0 || startCalls != 0 {
		t.Fatalf("mutation hooks ran before HKCU rejection: write=%d delete=%d start=%d", writeCalls, deleteCalls, startCalls)
	}
	if _, err := os.Lstat(launcher); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launcher created before HKCU rejection: %v", err)
	}
}

func TestInstallServiceRejectsSymlinkLauncherWithoutFollowing(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	sentinel := filepath.Join(filepath.Dir(stateDir), "foreign-launcher")
	if err := os.WriteFile(sentinel, []byte("foreign-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, launcher); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mutationCalls := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { mutationCalls++; return "", false, nil },
		func(string) error { mutationCalls++; return nil },
		func() error { mutationCalls++; return nil },
		func(string, ...string) error { mutationCalls++; return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected symlink launcher rejection, got %v", err)
	}
	if mutationCalls != 0 {
		t.Fatalf("hook ran %d times before symlink rejection", mutationCalls)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "foreign-sentinel" {
		t.Fatalf("symlink target changed: %q err=%v", got, err)
	}
}

func TestInstallServiceCreatesOwnedLauncherAndRunEntryAfterPreflight(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	writeValue := ""
	startCalls := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return "", false, nil },
		func(value string) error { writeValue = value; return nil },
		func() error { return errors.New("unexpected delete") },
		func(got string, args ...string) error {
			startCalls++
			if !strings.EqualFold(got, executable) || strings.Join(args, " ") != "daemon" {
				t.Fatalf("unexpected start target: %s %v", got, args)
			}
			return nil
		},
	)
	t.Cleanup(restore)

	result, err := InstallService(executable, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Started || result.Mode != ServiceModePersistent || startCalls != 1 {
		t.Fatalf("unexpected service result: %+v startCalls=%d", result, startCalls)
	}
	wantCommand := windowsServiceRunCommand(launcher)
	if !strings.EqualFold(writeValue, wantCommand) {
		t.Fatalf("run entry=%q want %q", writeValue, wantCommand)
	}
	if got, err := os.ReadFile(launcher); err != nil || string(got) != windowsServiceLauncherContents(executable, stateDir) {
		t.Fatalf("launcher content=%q err=%v", got, err)
	}
}

func TestBeginServiceRepairRollbackRestoresExactWindowsState(t *testing.T) {
	for _, test := range []struct {
		name      string
		launcher  bool
		autoStart bool
		running   bool
	}{
		{name: "missing"},
		{name: "stopped", launcher: true, autoStart: true},
		{name: "running", launcher: true, autoStart: true, running: true},
		{name: "manual running", running: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, stateDir, launcher := windowsServiceFixture(t)
			if test.launcher {
				if err := os.WriteFile(launcher, []byte(windowsServiceLauncherContents(executable, stateDir)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runCommand := windowsServiceRunCommand(launcher)
			autoStart := test.autoStart
			running := test.running
			restoreHooks := replaceCurrentWindowsServiceHooks(
				func() (string, bool, error) { return runCommand, autoStart, nil },
				func(value string) error {
					if !strings.EqualFold(value, runCommand) {
						t.Fatalf("unexpected Run command: %s", value)
					}
					autoStart = true
					return nil
				},
				func() error { autoStart = false; return nil },
				func(string, ...string) error { running = true; return nil },
			)
			t.Cleanup(restoreHooks)
			originalStop := stopCurrentWindowsServiceProcess
			originalRunning := currentWindowsServiceRunning
			stopCurrentWindowsServiceProcess = func(string) error { running = false; return nil }
			currentWindowsServiceRunning = func(string) (bool, error) { return running, nil }
			t.Cleanup(func() {
				stopCurrentWindowsServiceProcess = originalStop
				currentWindowsServiceRunning = originalRunning
			})

			result, rollback, err := BeginServiceRepair(executable, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Installed || !result.Started || rollback == nil || !autoStart || !running {
				t.Fatalf("repair result=%+v rollback=%v autoStart=%v running=%v", result, rollback != nil, autoStart, running)
			}
			if err := rollback(); err != nil {
				t.Fatal(err)
			}
			_, launcherErr := os.Lstat(launcher)
			launcherExists := launcherErr == nil
			if launcherExists != test.launcher || autoStart != test.autoStart || running != test.running {
				t.Fatalf("restored launcher=%v autoStart=%v running=%v want launcher=%v autoStart=%v running=%v",
					launcherExists, autoStart, running, test.launcher, test.autoStart, test.running)
			}
		})
	}
}

func TestInstallServiceRegistryFailureRemovesLauncherCreatedByCall(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	registryErr := errors.New("registry write failed")
	readCalls := 0
	startCalls := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { readCalls++; return "", false, nil },
		func(string) error { return registryErr },
		func() error { return errors.New("unexpected registry delete") },
		func(string, ...string) error { startCalls++; return nil },
	)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, registryErr) {
		t.Fatalf("registry error=%v want %v", err, registryErr)
	}
	if readCalls < 2 {
		t.Fatalf("Run entry was not rechecked after launcher activation: reads=%d", readCalls)
	}
	if startCalls != 0 {
		t.Fatalf("service started after registry failure: calls=%d", startCalls)
	}
	if _, statErr := os.Lstat(launcher); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("launcher created by failed call remained: %v", statErr)
	}
}

func TestInstallServiceRegistryFailurePreservesPreexistingExactLauncher(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	wantLauncher := windowsServiceLauncherContents(executable, stateDir)
	if err := os.WriteFile(launcher, []byte(wantLauncher), 0o600); err != nil {
		t.Fatal(err)
	}
	registryErr := errors.New("registry write failed")
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return "", false, nil },
		func(string) error { return registryErr },
		func() error { return errors.New("unexpected registry delete") },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); !errors.Is(err, registryErr) {
		t.Fatalf("registry error=%v want %v", err, registryErr)
	}
	if got, err := os.ReadFile(launcher); err != nil || string(got) != wantLauncher {
		t.Fatalf("preexisting exact launcher changed: %q err=%v", got, err)
	}
}

func TestInstallServiceRegistryWriteThatPartiallySucceedsRollsBackOwnedRunEntry(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	registryErr := errors.New("registry write returned failure after mutation")
	runValue := ""
	runFound := false
	deleteCalls := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return runValue, runFound, nil },
		func(value string) error {
			runValue, runFound = value, true
			return registryErr
		},
		func() error {
			deleteCalls++
			runValue, runFound = "", false
			return nil
		},
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); !errors.Is(err, registryErr) {
		t.Fatalf("registry error=%v want %v", err, registryErr)
	}
	if runFound || runValue != "" || deleteCalls != 1 {
		t.Fatalf("partially-created Run entry remained: found=%v value=%q deletes=%d", runFound, runValue, deleteCalls)
	}
	if _, statErr := os.Lstat(launcher); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("launcher created with partial Run entry remained: %v", statErr)
	}
}

func TestInstallServiceSecondRunPreflightFailureRemovesOnlyNewLauncher(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	readErr := errors.New("second registry read failed")
	reads := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) {
			reads++
			if reads == 1 {
				return "", false, nil
			}
			return "", false, readErr
		},
		func(string) error { return errors.New("unexpected registry write") },
		func() error { return errors.New("unexpected registry delete") },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restore)

	if _, err := InstallService(executable, stateDir); !errors.Is(err, readErr) {
		t.Fatalf("second Run preflight error=%v want %v", err, readErr)
	}
	if _, statErr := os.Lstat(launcher); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("new launcher remained after second preflight failure: %v", statErr)
	}
}

func TestInstallServiceRollbackFailurePreservesPrimaryAndDetail(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	registryErr := errors.New("registry write failed after mutation")
	cleanupErr := errors.New("registry rollback failed")
	runValue := ""
	runFound := false
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return runValue, runFound, nil },
		func(value string) error {
			runValue, runFound = value, true
			return registryErr
		},
		func() error { return cleanupErr },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, registryErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("combined install error=%v primary=%v cleanup=%v", err, registryErr, cleanupErr)
	}
	if !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback detail is not recognizable: %v", err)
	}
	if _, statErr := os.Lstat(launcher); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("launcher was not independently rolled back: %v", statErr)
	}
}

func TestInstallServiceRollbackDoesNotDeleteForeignRunReplacement(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	registryErr := errors.New("registry write failed after replacement")
	foreignValue := `wscript.exe //B //Nologo "C:\foreign\service.vbs"`
	runValue := ""
	runFound := false
	deleteCalls := 0
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return runValue, runFound, nil },
		func(string) error {
			runValue, runFound = foreignValue, true
			return registryErr
		},
		func() error { deleteCalls++; return nil },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if !errors.Is(err, registryErr) || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("foreign replacement rollback error=%v", err)
	}
	if deleteCalls != 0 || !runFound || runValue != foreignValue {
		t.Fatalf("foreign Run replacement changed: deletes=%d found=%v value=%q", deleteCalls, runFound, runValue)
	}
	if _, statErr := os.Lstat(launcher); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owned launcher remained after foreign Run replacement: %v", statErr)
	}
}

func TestUninstallServiceRejectsForeignOwnedTargetsBeforeStopOrDelete(t *testing.T) {
	executable, stateDir, launcher := windowsServiceFixture(t)
	if err := os.WriteFile(launcher, []byte("foreign-launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopCalls, deleteCalls := 0, 0
	originalStop := stopCurrentWindowsServiceProcess
	stopCurrentWindowsServiceProcess = func(string) error { stopCalls++; return nil }
	restore := replaceCurrentWindowsServiceHooks(
		func() (string, bool, error) { return "", false, nil },
		func(string) error { return nil },
		func() error { deleteCalls++; return nil },
		func(string, ...string) error { return nil },
	)
	t.Cleanup(func() {
		stopCurrentWindowsServiceProcess = originalStop
		restore()
	})

	if err := UninstallService(executable, stateDir); err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("expected foreign uninstall target rejection, got %v", err)
	}
	if stopCalls != 0 || deleteCalls != 0 {
		t.Fatalf("uninstall mutated before rejection: stop=%d delete=%d", stopCalls, deleteCalls)
	}
	if got, err := os.ReadFile(launcher); err != nil || string(got) != "foreign-launcher" {
		t.Fatalf("foreign launcher changed: %q err=%v", got, err)
	}
}

func windowsServiceFixture(t *testing.T) (string, string, string) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "codex-usage")
	executable := filepath.Join(stateDir, "bin", "codex-usage.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("current-executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable, stateDir, filepath.Join(stateDir, "codex-usage-start.vbs")
}

func replaceCurrentWindowsServiceHooks(
	read func() (string, bool, error),
	write func(string) error,
	remove func() error,
	start func(string, ...string) error,
) func() {
	originalRead := readCurrentWindowsRunValue
	originalWrite := writeCurrentWindowsRunValue
	originalDelete := deleteCurrentWindowsRunValue
	originalStart := startCurrentWindowsServiceProcess
	readCurrentWindowsRunValue = read
	writeCurrentWindowsRunValue = write
	deleteCurrentWindowsRunValue = remove
	startCurrentWindowsServiceProcess = start
	return func() {
		readCurrentWindowsRunValue = originalRead
		writeCurrentWindowsRunValue = originalWrite
		deleteCurrentWindowsRunValue = originalDelete
		startCurrentWindowsServiceProcess = originalStart
	}
}

func startWindowsProcessHelper(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "codex-usage.exe")
	return target, startWindowsProcessHelperAt(t, target)
}

func startWindowsProcessHelperAt(t *testing.T, target string) *exec.Cmd {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(target, "-test.run=^TestWindowsProcessHelper$")
	command.Env = append(os.Environ(), windowsHelperEnvironment+"=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	return command
}

func waitForWindowsProcessPath(t *testing.T, pid int, target string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		path, err := windowsProcessExecutable(uint32(pid))
		if err == nil && strings.EqualFold(filepath.Clean(path), filepath.Clean(target)) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("helper process %d did not expose target path %s", pid, target)
}

func waitForWindowsProcessExit(t *testing.T, command *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("wait for helper process: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper process did not exit")
	}
}
