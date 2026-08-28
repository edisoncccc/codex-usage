//go:build linux

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLinuxInstallServiceUnavailableBusDoesNotRestartTrustedDetachedProcess(t *testing.T) {
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)
	writeLinuxPIDFixture(t, stateDir, 424242)

	startCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") != linuxSystemdSnapshotCommand {
			t.Fatalf("unexpected systemctl mutation: %v", args)
		}
		return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
	}
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.StartDetached = func(string, ...string) error { startCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	result, err := InstallService(executable, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Started || result.Mode != ServiceModeDetachedFallback {
		t.Fatalf("unexpected service result: %+v", result)
	}
	if startCalls != 0 || !strings.Contains(result.Warning, "未重复启动") {
		t.Fatalf("trusted process fallback startCalls=%d warning=%q", startCalls, result.Warning)
	}
}

func TestLinuxInstallServiceUnavailableBusFailsClosedForExistingUnitWithoutTrustedProcess(t *testing.T) {
	executable, stateDir, unitPath := linuxServiceFixture(t)
	writeExactLinuxUnitFixture(t, unitPath, executable, stateDir)

	startCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") != linuxSystemdSnapshotCommand {
			t.Fatalf("unexpected systemctl mutation: %v", args)
		}
		return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
	}
	ops.StartDetached = func(string, ...string) error { startCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("error=%v want existing_install_untrusted", err)
	}
	if startCalls != 0 {
		t.Fatalf("untrusted existing unit started %d detached processes", startCalls)
	}
}

func TestLinuxStopServiceSignalsBoundProcessHandle(t *testing.T) {
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)

	opened, signaled, closed := 0, 0, 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	ops.ReadProcessExecutable = func(pid int) (string, error) {
		if pid != 424242 {
			t.Fatalf("unexpected PID read: %d", pid)
		}
		return executable, nil
	}
	ops.OpenProcessHandle = func(pid int) (int, error) {
		opened++
		if pid != 424242 {
			t.Fatalf("unexpected PID handle target: %d", pid)
		}
		return 77, nil
	}
	ops.SignalProcessHandle = func(handle int, signal os.Signal) error {
		signaled++
		if handle != 77 || signal != syscall.SIGTERM {
			t.Fatalf("unexpected handle signal: handle=%d signal=%v", handle, signal)
		}
		return nil
	}
	ops.ProcessHandleExited = func(handle int) (bool, error) {
		if handle != 77 {
			t.Fatalf("unexpected waited handle: %d", handle)
		}
		return signaled > 0, nil
	}
	ops.CloseProcessHandle = func(handle int) error {
		closed++
		if handle != 77 {
			t.Fatalf("unexpected closed handle: %d", handle)
		}
		return nil
	}
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	if err := StopService(executable, stateDir); err != nil {
		t.Fatal(err)
	}
	if opened != 1 || signaled != 1 || closed != 1 {
		t.Fatalf("pidfd lifecycle opened=%d signaled=%d closed=%d", opened, signaled, closed)
	}
}

func TestLinuxStopServiceFailsClosedWhenProcessHandleUnavailable(t *testing.T) {
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)

	signalCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	ops.ReadProcessExecutable = func(int) (string, error) { return executable, nil }
	ops.OpenProcessHandle = func(int) (int, error) { return -1, syscall.ENOSYS }
	ops.SignalProcessHandle = func(int, os.Signal) error { signalCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	err := StopService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "process handle") {
		t.Fatalf("error=%v want process handle failure", err)
	}
	if signalCalls != 0 {
		t.Fatalf("unsafe signal fallback called %d times", signalCalls)
	}
}

func TestLinuxProcessHandleObservesExitedZombieWithoutPIDLookup(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	ops := activeLinuxServiceOperations
	handle, err := ops.OpenProcessHandle(command.Process.Pid)
	if err != nil {
		t.Fatalf("open process handle: %v", err)
	}
	defer func() {
		if err := ops.CloseProcessHandle(handle); err != nil {
			t.Errorf("close process handle: %v", err)
		}
	}()
	if err := ops.SignalProcessHandle(handle, syscall.SIGTERM); err != nil {
		t.Fatalf("signal process handle: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		exited, err := ops.ProcessHandleExited(handle)
		if err != nil {
			t.Fatal(err)
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pidfd did not report the exited child before wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Wait()
	waited = true
}

func TestLinuxSystemdUnavailableDiagnosticRequiresWholeKnownMessage(t *testing.T) {
	for _, diagnostic := range []string{
		"Failed to connect to bus: No medium found\nPermission denied",
		"prefix\nFailed to connect to bus: No medium found",
		"无法连接到总线：介质不存在",
	} {
		if got := classifyLinuxSystemdSnapshotFailure(diagnostic); got != linuxSystemdSnapshotFailureUnknown {
			t.Errorf("diagnostic %q classified as %q", diagnostic, got)
		}
	}
}

func TestLinuxSystemctlEnvironmentForcesCLocale(t *testing.T) {
	base := []string{"PATH=/usr/bin", "LANG=zh_CN.UTF-8", "LC_ALL=zh_CN.UTF-8", "HOME=/tmp/home"}
	got := linuxSystemctlEnvironment(base)
	want := []string{"PATH=/usr/bin", "LANG=zh_CN.UTF-8", "HOME=/tmp/home", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemctl environment=%v want %v", got, want)
	}
}

func TestLinuxInstallServiceUnavailableBusRejectsForeignPIDBeforeFallback(t *testing.T) {
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)

	startCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "/fake/systemctl", nil }
	ops.RunSystemctl = func(...string) ([]byte, error) {
		return []byte("Failed to connect to bus: No such file or directory\n"), errors.New("exit status 1")
	}
	ops.ReadProcessExecutable = func(int) (string, error) {
		return filepath.Join(filepath.Dir(stateDir), "foreign", "codex-usage"), nil
	}
	ops.StartDetached = func(string, ...string) error { startCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("error=%v want existing_install_untrusted", err)
	}
	if startCalls != 0 {
		t.Fatalf("foreign PID caused %d detached starts", startCalls)
	}
}

func TestLinuxInstallServiceWithoutSystemctlRejectsForeignPIDBeforeFallback(t *testing.T) {
	executable, stateDir, _ := linuxServiceFixture(t)
	writeLinuxPIDFixture(t, stateDir, 424242)

	startCalls := 0
	ops := inertLinuxServiceOperations()
	ops.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	ops.ReadProcessExecutable = func(int) (string, error) {
		return filepath.Join(filepath.Dir(stateDir), "foreign", "codex-usage"), nil
	}
	ops.StartDetached = func(string, ...string) error { startCalls++; return nil }
	restore := replaceLinuxServiceOperations(ops)
	t.Cleanup(restore)

	_, err := InstallService(executable, stateDir)
	if err == nil || !strings.Contains(err.Error(), "existing_install_untrusted") {
		t.Fatalf("error=%v want existing_install_untrusted", err)
	}
	if startCalls != 0 {
		t.Fatalf("foreign PID caused %d detached starts", startCalls)
	}
}
