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
	target, command := startWindowsProcessHelper(t)
	waitForWindowsProcessPath(t, command.Process.Pid, target)
	stateDir := t.TempDir()
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	launcherPath := filepath.Join(stateDir, "codex-usage-start.vbs")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", command.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcherPath, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalTerminate := terminateWindowsProcessByPID
	terminateWindowsProcessByPID = func(uint32) error { return windows.ERROR_ACCESS_DENIED }
	err := UninstallService(target, stateDir)
	terminateWindowsProcessByPID = originalTerminate
	if err == nil || !strings.Contains(err.Error(), "以管理员身份运行") {
		t.Fatalf("expected actionable access error, got %v", err)
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

func startWindowsProcessHelper(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "codex-usage.exe")
	data, err := os.ReadFile(source)
	if err != nil {
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
	return target, command
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
