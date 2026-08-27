//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func init() {
	activeLinuxServiceOperations = linuxServiceOperations{
		LookPath: exec.LookPath,
		RunSystemctl: func(args ...string) ([]byte, error) {
			return exec.Command("systemctl", args...).CombinedOutput()
		},
		RunLoginctl: func(args ...string) ([]byte, error) {
			return exec.Command("loginctl", args...).Output()
		},
		StartDetached: StartDetached,
		RemoveUnit:    os.Remove,
		ReadProcessExecutable: func(pid int) (string, error) {
			return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		},
		SignalProcess: func(pid int, signal os.Signal) error {
			process, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return process.Signal(signal)
		},
		ProcessAlive: func(pid int) (bool, error) {
			err := unix.Kill(pid, 0)
			if err == nil || errors.Is(err, unix.EPERM) {
				return true, nil
			}
			if errors.Is(err, unix.ESRCH) {
				return false, nil
			}
			return false, err
		},
		Sleep: time.Sleep,
	}
}

func InstallService(executable, stateDir string) (ServiceResult, error) {
	if strings.ContainsAny(executable+stateDir, "\x00\r\n") {
		return ServiceResult{}, fmt.Errorf("服务路径包含不安全的控制字符")
	}
	ops := activeLinuxServiceOperations
	syncParent := syncServiceParent
	preflight, err := inspectCurrentLinuxUnit(executable, stateDir)
	if err != nil {
		return ServiceResult{}, err
	}
	if _, err := ops.LookPath("systemctl"); err != nil {
		result := ServiceResult{
			Installed: true,
			Mode:      ServiceModeDetachedFallback,
			Warning:   "未检测到 systemctl；未检查 systemd manager 状态且未写入 systemd unit",
		}
		if startErr := ops.StartDetached(executable, "daemon"); startErr != nil {
			result.Warning += "，后台启动失败: " + startErr.Error()
			return result, nil
		}
		result.Started = true
		result.Warning += "；本次已启动，但需自行配置登录自启"
		return result, nil
	}
	snapshot, err := inspectCurrentLinuxSystemdSnapshot(ops)
	if err != nil {
		var unavailable *linuxSystemdSnapshotFailure
		if errors.As(err, &unavailable) && unavailable.kind == linuxSystemdSnapshotUserBusUnavailable {
			result := ServiceResult{
				Installed: true,
				Mode:      ServiceModeDetachedFallback,
				Warning: fmt.Sprintf(
					"systemd --user user bus 不可用（%s）；未写入或修改 systemd unit",
					unavailable.diagnostic,
				),
			}
			if startErr := ops.StartDetached(executable, "daemon"); startErr != nil {
				result.Warning += "；后台启动失败: " + startErr.Error()
				return result, nil
			}
			result.Started = true
			result.Warning += "；本次已启动，但需在 user bus 可用后重试安装以启用登录自启"
			return result, nil
		}
		return ServiceResult{}, err
	}
	if err := validateCurrentLinuxSystemdSnapshot(preflight, snapshot); err != nil {
		return ServiceResult{}, err
	}
	managerStateInitiallyAbsent := snapshot.clearlyAbsent()
	unitDir := filepath.Dir(preflight.path)
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return ServiceResult{}, err
	}
	if err := validateSafePreviousDirectory(unitDir); err != nil {
		return ServiceResult{}, untrustedPreviousService("systemd user unit directory: %v", err)
	}
	createdUnit := false
	failInstall := func(primary error, reload, enableAttempted bool) (ServiceResult, error) {
		rollbackErrors := rollbackLinuxServiceInstall(
			preflight, createdUnit, reload, enableAttempted, managerStateInitiallyAbsent, ops, syncParent,
		)
		return ServiceResult{}, joinServiceInstallRollback(primary, rollbackErrors...)
	}
	if !preflight.exists {
		created, err := activateCurrentLinuxUnit(preflight.path, preflight.contents, syncParent)
		createdUnit = created
		if err != nil {
			return failInstall(err, false, false)
		}
	}
	if err := validateExactCurrentLinuxUnit(preflight.path, preflight.contents); err != nil {
		return failInstall(err, false, false)
	}
	if output, err := ops.RunSystemctl("--user", "daemon-reload"); err != nil {
		if startErr := ops.StartDetached(executable, "daemon"); startErr != nil {
			return failInstall(fmt.Errorf("systemctl --user daemon-reload: %w: %s；后台启动也失败: %v",
				err, strings.TrimSpace(string(output)), startErr), true, false)
		}
		return ServiceResult{
			Installed: true, Started: true, Mode: ServiceModeDetachedFallback, Detail: preflight.path,
			Warning: "systemd --user 当前不可用；本次已后台启动，但登录自启需在 user bus 可用后执行 systemctl --user enable --now codex-usage",
		}, nil
	}
	output, err := ops.RunSystemctl("--user", "enable", "--now", "codex-usage.service")
	if err != nil {
		if startErr := ops.StartDetached(executable, "daemon"); startErr != nil {
			return failInstall(fmt.Errorf("systemctl --user enable --now: %w: %s；后台启动也失败: %v",
				err, strings.TrimSpace(string(output)), startErr), true, true)
		}
		return ServiceResult{
			Installed: true, Started: true, Mode: ServiceModeDetachedFallback, Detail: preflight.path,
			Warning: "systemd unit 已写入但 enable --now 失败；本次已后台启动，请检查 user bus/linger 后重试",
		}, nil
	}
	result := ServiceResult{Installed: true, Started: true, Mode: ServiceModePersistent, Detail: preflight.path}
	if user := os.Getenv("USER"); user != "" {
		if value, err := ops.RunLoginctl("show-user", user, "-p", "Linger", "--value"); err == nil &&
			strings.TrimSpace(string(value)) != "yes" {
			result.Warning = "当前用户 linger 未开启：退出全部登录会话后 systemd --user 服务可能停止；可请管理员执行 loginctl enable-linger " + user
		}
	}
	return result, nil
}

type currentLinuxUnitInspection struct {
	path     string
	contents string
	exists   bool
}

type currentLinuxSystemdSnapshot struct {
	fragmentPath  string
	loadState     string
	unitFileState string
	activeState   string
	transient     string
}

type linuxSystemdSnapshotFailureKind string

const (
	linuxSystemdSnapshotFailureUnknown     linuxSystemdSnapshotFailureKind = "unknown"
	linuxSystemdSnapshotUserBusUnavailable linuxSystemdSnapshotFailureKind = "user_bus_unavailable"
)

type linuxSystemdSnapshotFailure struct {
	kind       linuxSystemdSnapshotFailureKind
	diagnostic string
	err        error
}

func (e *linuxSystemdSnapshotFailure) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v: %s", e.kind, e.err, e.diagnostic)
}

func (e *linuxSystemdSnapshotFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func inspectCurrentLinuxSystemdSnapshot(ops linuxServiceOperations) (currentLinuxSystemdSnapshot, error) {
	output, err := ops.RunSystemctl(
		"--user", "show", "codex-usage.service",
		"--property=FragmentPath",
		"--property=LoadState",
		"--property=UnitFileState",
		"--property=ActiveState",
		"--property=Transient",
		"--all",
		"--no-pager",
	)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if classifyLinuxSystemdSnapshotFailure(detail) == linuxSystemdSnapshotUserBusUnavailable {
			return currentLinuxSystemdSnapshot{}, &linuxSystemdSnapshotFailure{
				kind:       linuxSystemdSnapshotUserBusUnavailable,
				diagnostic: detail,
				err:        err,
			}
		}
		if detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return currentLinuxSystemdSnapshot{}, &PermissionError{Operation: "检查当前用户 systemd 服务状态", Err: err}
	}
	return parseCurrentLinuxSystemdSnapshot(output)
}

func classifyLinuxSystemdSnapshotFailure(diagnostic string) linuxSystemdSnapshotFailureKind {
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(diagnostic, "\r\n", "\n")))
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "failed to connect to bus: no medium found",
			"failed to connect to bus: no such file or directory",
			"failed to get d-bus connection: no such file or directory":
			return linuxSystemdSnapshotUserBusUnavailable
		}
		const missingSessionEnvironment = "failed to connect to bus: $dbus_session_bus_address and $xdg_runtime_dir not defined"
		if line == missingSessionEnvironment || strings.HasPrefix(line, missingSessionEnvironment+" (consider using --machine=") {
			return linuxSystemdSnapshotUserBusUnavailable
		}
	}
	return linuxSystemdSnapshotFailureUnknown
}

func parseCurrentLinuxSystemdSnapshot(output []byte) (currentLinuxSystemdSnapshot, error) {
	wanted := map[string]*string{}
	snapshot := currentLinuxSystemdSnapshot{}
	wanted["FragmentPath"] = &snapshot.fragmentPath
	wanted["LoadState"] = &snapshot.loadState
	wanted["UnitFileState"] = &snapshot.unitFileState
	wanted["ActiveState"] = &snapshot.activeState
	wanted["Transient"] = &snapshot.transient
	seen := make(map[string]bool, len(wanted))
	normalized := strings.ReplaceAll(string(output), "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, "\x00\r") {
			return snapshot, untrustedPreviousService("systemd state snapshot contains control characters")
		}
		key, value, found := strings.Cut(line, "=")
		destination, expected := wanted[key]
		if !found || !expected || seen[key] {
			return snapshot, untrustedPreviousService("systemd state snapshot has unknown or duplicate property")
		}
		seen[key] = true
		*destination = value
	}
	for key := range wanted {
		if !seen[key] {
			return snapshot, untrustedPreviousService("systemd state snapshot is missing property %s", key)
		}
	}
	if snapshot.transient != "yes" && snapshot.transient != "no" {
		return snapshot, untrustedPreviousService("systemd state snapshot has invalid Transient value")
	}
	if snapshot.loadState == "" || snapshot.activeState == "" {
		return snapshot, untrustedPreviousService("systemd state snapshot has empty required state")
	}
	return snapshot, nil
}

func (snapshot currentLinuxSystemdSnapshot) clearlyAbsent() bool {
	return snapshot.fragmentPath == "" &&
		snapshot.loadState == "not-found" &&
		(snapshot.unitFileState == "" || snapshot.unitFileState == "not-found") &&
		snapshot.activeState == "inactive" &&
		snapshot.transient == "no"
}

func validateCurrentLinuxSystemdSnapshot(
	preflight currentLinuxUnitInspection,
	snapshot currentLinuxSystemdSnapshot,
) error {
	if snapshot.clearlyAbsent() {
		return nil
	}
	if !preflight.exists {
		return untrustedPreviousService(
			"systemd manager already has codex-usage.service: fragment=%q load=%q unit_file=%q active=%q transient=%q",
			snapshot.fragmentPath, snapshot.loadState, snapshot.unitFileState, snapshot.activeState, snapshot.transient,
		)
	}
	if snapshot.transient != "no" || snapshot.loadState != "loaded" || snapshot.fragmentPath == "" {
		return untrustedPreviousService("systemd manager state does not match the owned local unit")
	}
	fragmentAbsolute, err := filepath.Abs(snapshot.fragmentPath)
	if err != nil || filepath.Clean(fragmentAbsolute) != snapshot.fragmentPath || snapshot.fragmentPath != preflight.path {
		return untrustedPreviousService("systemd manager fragment targets an unknown unit: %s", snapshot.fragmentPath)
	}
	return nil
}

func inspectCurrentLinuxUnit(executable, stateDir string) (currentLinuxUnitInspection, error) {
	inspection := currentLinuxUnitInspection{contents: currentSystemdUnitContents(executable, stateDir)}
	if err := validateLinuxServiceInstallTarget(executable, stateDir); err != nil {
		return inspection, err
	}
	unitPath, err := currentLinuxUnitPath()
	if err != nil {
		return inspection, err
	}
	inspection.path = unitPath
	info, err := os.Lstat(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := validateNoLinkedAncestor(unitPath); err != nil {
			return inspection, untrustedPreviousService("systemd unit boundary: %v", err)
		}
		return inspection, nil
	}
	if err != nil {
		return inspection, untrustedPreviousService("inspect systemd unit: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return inspection, untrustedPreviousService("systemd unit is not a safe regular file: %s", unitPath)
	}
	if err := validateExactCurrentLinuxUnit(unitPath, inspection.contents); err != nil {
		return inspection, err
	}
	inspection.exists = true
	return inspection, nil
}

func currentLinuxUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	absolute, err := filepath.Abs(configHome)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if filepath.Clean(configHome) != absolute {
		return "", untrustedPreviousService("XDG_CONFIG_HOME must be a canonical absolute path: %s", configHome)
	}
	return filepath.Join(absolute, "systemd", "user", "codex-usage.service"), nil
}

func validateLinuxServiceInstallTarget(executable, stateDir string) error {
	executableAbsolute, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	stateAbsolute, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	executableAbsolute = filepath.Clean(executableAbsolute)
	stateAbsolute = filepath.Clean(stateAbsolute)
	if executableAbsolute != filepath.Clean(executable) || stateAbsolute != filepath.Clean(stateDir) {
		return untrustedPreviousService("service paths must be canonical absolute paths")
	}
	if err := validateSafePreviousDirectory(stateAbsolute); err != nil {
		return untrustedPreviousService("current service state directory: %v", err)
	}
	if err := validateSafePreviousRegular(executableAbsolute); err != nil {
		return untrustedPreviousService("current service executable: %v", err)
	}
	override := strings.TrimSpace(os.Getenv("CODEX_USAGE_HOME"))
	if override != "" {
		overrideAbsolute, err := filepath.Abs(override)
		if err != nil || filepath.Clean(overrideAbsolute) != override || override != stateAbsolute || executableAbsolute != filepath.Join(stateAbsolute, "bin", "codex-usage") {
			return untrustedPreviousService("service executable is outside the configured install boundary: %s", executableAbsolute)
		}
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	if stateAbsolute != filepath.Clean(filepath.Join(dataHome, "codex-usage")) || executableAbsolute != filepath.Clean(filepath.Join(home, ".local", "bin", "codex-usage")) {
		return untrustedPreviousService("service executable is outside the current user install boundary: %s", executableAbsolute)
	}
	return nil
}

func activateCurrentLinuxUnit(path, contents string, syncParent func(string) error) (bool, error) {
	parent := filepath.Dir(path)
	if err := validateSafePreviousDirectory(parent); err != nil {
		return false, untrustedPreviousService("systemd unit parent: %v", err)
	}
	temporary, err := os.CreateTemp(parent, ".codex-usage.service-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	owned := true
	defer func() {
		_ = temporary.Close()
		if owned {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return false, err
	}
	if _, err := temporary.WriteString(contents); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := validateSafePreviousDirectory(parent); err != nil {
		return false, untrustedPreviousService("systemd unit parent changed: %v", err)
	}
	if err := RenameNoReplace(temporaryPath, path); err != nil {
		return false, fmt.Errorf("activate systemd unit without replacement: %w", err)
	}
	owned = false
	if err := syncParent(path); err != nil {
		return true, fmt.Errorf("sync systemd unit directory: %w", err)
	}
	return true, nil
}

func rollbackLinuxServiceInstall(
	preflight currentLinuxUnitInspection,
	createdUnit bool,
	reload bool,
	enableAttempted bool,
	managerStateInitiallyAbsent bool,
	ops linuxServiceOperations,
	syncParent func(string) error,
) []error {
	if !createdUnit {
		return nil
	}
	unitPresent, err := inspectExactRollbackLinuxUnit(preflight.path, preflight.contents)
	if err != nil {
		return []error{fmt.Errorf("rollback validate systemd unit: %w", err)}
	}
	rollbackErrors := make([]error, 0, 4)
	if enableAttempted && managerStateInitiallyAbsent {
		if output, err := ops.RunSystemctl("--user", "disable", "--now", "codex-usage.service"); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback systemctl --user disable --now: %w: %s", err, strings.TrimSpace(string(output))))
		}
		if unitPresent {
			unitPresent, err = inspectExactRollbackLinuxUnit(preflight.path, preflight.contents)
			if err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback revalidate systemd unit after disable: %w", err))
				return rollbackErrors
			}
		}
	}
	if unitPresent {
		if err := ops.RemoveUnit(preflight.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback remove systemd unit: %w", err))
		}
	}
	if err := syncParent(preflight.path); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback sync removed systemd unit: %w", err))
	}
	if reload {
		if output, err := ops.RunSystemctl("--user", "daemon-reload"); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(string(output))))
		}
	}
	return rollbackErrors
}

func inspectExactRollbackLinuxUnit(path, expected string) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := validateExactCurrentLinuxUnit(path, expected); err != nil {
		return false, err
	}
	return true, nil
}

func validateExactCurrentLinuxUnit(path, expected string) error {
	data, err := readSafeRegularFile(path, 256*1024)
	if err != nil {
		return untrustedPreviousService("read systemd unit safely: %v", err)
	}
	if string(data) != expected {
		return untrustedPreviousService("systemd unit targets an unknown executable or state directory")
	}
	return nil
}

func LockDown(path string) error { return os.Chmod(path, 0o700) }

func SetPrivateUmask() { syscall.Umask(0o077) }

func StopService(executable, stateDir string) error {
	ops := activeLinuxServiceOperations
	return stopLinuxService(executable, stateDir, ops)
}

type currentLinuxPIDInspection struct {
	path          string
	info          os.FileInfo
	pid           int
	exists        bool
	processExists bool
}

type currentLinuxUninstallInspection struct {
	unit                currentLinuxUnitInspection
	pid                 currentLinuxPIDInspection
	systemctlAvailable  bool
	managerOwned        bool
	managerProcessOwned bool
}

func inspectCurrentLinuxUninstall(executable, stateDir string, ops linuxServiceOperations) (currentLinuxUninstallInspection, error) {
	inspection := currentLinuxUninstallInspection{}
	unit, err := inspectCurrentLinuxUnit(executable, stateDir)
	if err != nil {
		return inspection, err
	}
	inspection.unit = unit
	if _, err := ops.LookPath("systemctl"); err == nil {
		inspection.systemctlAvailable = true
		snapshot, err := inspectCurrentLinuxSystemdSnapshot(ops)
		if err != nil {
			var unavailable *linuxSystemdSnapshotFailure
			if !errors.As(err, &unavailable) || unavailable.kind != linuxSystemdSnapshotUserBusUnavailable {
				return inspection, err
			}
			inspection.systemctlAvailable = false
		} else {
			if err := validateCurrentLinuxSystemdSnapshot(unit, snapshot); err != nil {
				return inspection, err
			}
			inspection.managerOwned = !snapshot.clearlyAbsent()
			inspection.managerProcessOwned = inspection.managerOwned && snapshot.managerMayOwnProcess()
		}
	}
	pid, err := inspectCurrentLinuxPIDFile(filepath.Join(stateDir, "codex-usage.pid"), executable, ops)
	if err != nil {
		return inspection, err
	}
	inspection.pid = pid
	return inspection, nil
}

func (snapshot currentLinuxSystemdSnapshot) managerMayOwnProcess() bool {
	switch snapshot.activeState {
	case "inactive", "failed":
		return false
	default:
		return true
	}
}

func inspectCurrentLinuxPIDFile(path, expectedExecutable string, ops linuxServiceOperations) (currentLinuxPIDInspection, error) {
	inspection := currentLinuxPIDInspection{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := validateNoLinkedAncestor(path); err != nil {
			return inspection, untrustedPreviousService("current PID metadata boundary: %v", err)
		}
		return inspection, nil
	}
	if err != nil {
		return inspection, untrustedPreviousService("inspect current PID metadata: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return inspection, untrustedPreviousService("current PID metadata is not a safe regular file: %s", path)
	}
	if err := validateNoLinkedAncestor(path); err != nil {
		return inspection, untrustedPreviousService("current PID metadata boundary: %v", err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		return inspection, untrustedPreviousService("current PID metadata is invalid: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) {
		return inspection, untrustedPreviousService("current PID metadata changed during inspection")
	}
	if pid == os.Getpid() {
		return inspection, untrustedPreviousService("PID metadata points to the installer process")
	}
	expectedAbsolute, err := filepath.Abs(expectedExecutable)
	if err != nil || expectedAbsolute != filepath.Clean(expectedExecutable) {
		return inspection, untrustedPreviousService("expected executable must be a canonical absolute path: %s", expectedExecutable)
	}
	inspection.info = after
	inspection.pid = pid
	inspection.exists = true
	observed, err := ops.ReadProcessExecutable(pid)
	if errors.Is(err, os.ErrNotExist) {
		return inspection, nil
	}
	if err != nil {
		return inspection, fmt.Errorf("读取 PID %d 可执行路径: %w", pid, err)
	}
	observedAbsolute, err := filepath.Abs(observed)
	if err != nil || observedAbsolute != filepath.Clean(observed) || !samePlatformPath(observedAbsolute, expectedAbsolute) {
		return inspection, untrustedPreviousService("PID %d executable mismatch: %s", pid, observed)
	}
	inspection.processExists = true
	return inspection, nil
}

func revalidateCurrentLinuxPIDFile(
	expected currentLinuxPIDInspection,
	expectedExecutable string,
	ops linuxServiceOperations,
) (currentLinuxPIDInspection, error) {
	current, err := inspectCurrentLinuxPIDFile(expected.path, expectedExecutable, ops)
	if err != nil {
		return current, err
	}
	if !current.exists {
		return current, nil
	}
	if !expected.exists || expected.info == nil || current.pid != expected.pid || !os.SameFile(expected.info, current.info) {
		return current, untrustedPreviousService("current PID metadata changed before removal")
	}
	if !expected.processExists && current.processExists {
		return current, untrustedPreviousService("PID %d appeared after ownership preflight", current.pid)
	}
	return current, nil
}

func stopInspectedLinuxPIDFile(
	preflight currentLinuxPIDInspection,
	expectedExecutable string,
	ops linuxServiceOperations,
) (bool, error) {
	current, err := revalidateCurrentLinuxPIDFile(preflight, expectedExecutable, ops)
	if err != nil {
		return false, err
	}
	if !current.exists || !current.processExists {
		return false, nil
	}
	if err := ops.SignalProcess(current.pid, syscall.SIGTERM); err != nil {
		return false, fmt.Errorf("停止后台服务 PID %d: %w", current.pid, err)
	}
	if err := waitForLinuxPIDExit(current.pid, ops); err != nil {
		return false, err
	}
	return true, nil
}

func stopResidualManagerOwnedLinuxPIDFile(
	preflight currentLinuxPIDInspection,
	expectedExecutable string,
	ops linuxServiceOperations,
) (bool, error) {
	current, err := revalidateCurrentLinuxPIDFile(preflight, expectedExecutable, ops)
	if err != nil {
		return false, err
	}
	if !current.exists || !current.processExists {
		return false, nil
	}
	return stopInspectedLinuxPIDFile(current, expectedExecutable, ops)
}

func waitForLinuxPIDExit(pid int, ops linuxServiceOperations) error {
	for attempt := 0; attempt < 20; attempt++ {
		alive, err := ops.ProcessAlive(pid)
		if err != nil {
			return fmt.Errorf("确认后台服务 PID %d 退出: %w", pid, err)
		}
		if !alive {
			return nil
		}
		if attempt < 19 {
			ops.Sleep(50 * time.Millisecond)
		}
	}
	return fmt.Errorf("等待后台服务 PID %d 退出超时", pid)
}

func stopLinuxService(executable, stateDir string, ops linuxServiceOperations) error {
	preflight, err := inspectCurrentLinuxUninstall(executable, stateDir, ops)
	if err != nil {
		return err
	}
	var systemdErr error
	if preflight.managerOwned {
		if err := validateExactCurrentLinuxUnit(preflight.unit.path, preflight.unit.contents); err != nil {
			return err
		}
		output, stopErr := ops.RunSystemctl("--user", "stop", "codex-usage.service")
		if stopErr != nil {
			systemdErr = fmt.Errorf("systemctl --user stop: %w: %s", stopErr, strings.TrimSpace(string(output)))
		}
	}
	if preflight.managerProcessOwned && systemdErr == nil {
		_, stopErr := stopResidualManagerOwnedLinuxPIDFile(preflight.pid, executable, ops)
		return stopErr
	}
	stopped, pidErr := stopInspectedLinuxPIDFile(preflight.pid, executable, ops)
	if pidErr != nil {
		return pidErr
	}
	if stopped {
		return nil
	}
	return systemdErr
}

func UninstallService(executable, stateDir string) error {
	ops := activeLinuxServiceOperations
	syncParent := syncServiceParent
	preflight, err := inspectCurrentLinuxUninstall(executable, stateDir, ops)
	if err != nil {
		return err
	}
	var systemdErr error
	if preflight.managerOwned {
		if err := validateExactCurrentLinuxUnit(preflight.unit.path, preflight.unit.contents); err != nil {
			return err
		}
		output, stopErr := ops.RunSystemctl("--user", "stop", "codex-usage.service")
		if stopErr != nil {
			systemdErr = fmt.Errorf("systemctl --user stop: %w: %s", stopErr, strings.TrimSpace(string(output)))
		}
	}
	if preflight.managerProcessOwned && systemdErr == nil {
		if _, stopErr := stopResidualManagerOwnedLinuxPIDFile(preflight.pid, executable, ops); stopErr != nil {
			return stopErr
		}
	} else {
		stopped, pidErr := stopInspectedLinuxPIDFile(preflight.pid, executable, ops)
		if pidErr != nil {
			return pidErr
		}
		if systemdErr != nil && !stopped {
			return systemdErr
		}
	}
	preflight.pid, err = revalidateCurrentLinuxPIDFile(preflight.pid, executable, ops)
	if err != nil {
		return err
	}
	if preflight.unit.exists {
		if err := validateExactCurrentLinuxUnit(preflight.unit.path, preflight.unit.contents); err != nil {
			return err
		}
		if preflight.managerOwned {
			if output, disableErr := ops.RunSystemctl("--user", "disable", "codex-usage.service"); disableErr != nil {
				return fmt.Errorf("systemctl --user disable: %w: %s", disableErr, strings.TrimSpace(string(output)))
			}
		}
		if err := validateExactCurrentLinuxUnit(preflight.unit.path, preflight.unit.contents); err != nil {
			return err
		}
		if err := ops.RemoveUnit(preflight.unit.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncParent(preflight.unit.path); err != nil {
			return fmt.Errorf("sync removed systemd unit: %w", err)
		}
	}
	if preflight.systemctlAvailable && preflight.unit.exists {
		if output, reloadErr := ops.RunSystemctl("--user", "daemon-reload"); reloadErr != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w: %s", reloadErr, strings.TrimSpace(string(output)))
		}
	}
	if preflight.pid.exists {
		currentPID, err := revalidateCurrentLinuxPIDFile(preflight.pid, executable, ops)
		if err != nil {
			return err
		}
		if !currentPID.exists {
			return nil
		}
		if err := os.Remove(currentPID.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncParent(currentPID.path); err != nil {
			return fmt.Errorf("sync removed PID metadata: %w", err)
		}
	}
	return nil
}

func SuspendPreviousService(previous PreviousService) error {
	if err := validatePreviousServiceRemoval(previous); err != nil {
		return err
	}
	ops := activeLinuxServiceOperations
	unitPath, unitExists, err := previousSystemdUnit(previous)
	if err != nil {
		return err
	}
	_ = unitPath
	var systemdErr error
	if unitExists {
		if _, err := ops.LookPath("systemctl"); err == nil {
			if output, stopErr := ops.RunSystemctl("--user", "stop", previous.ServiceName); stopErr == nil {
				return nil
			} else {
				systemdErr = fmt.Errorf("systemctl --user stop %s: %w: %s", previous.ServiceName, stopErr, strings.TrimSpace(string(output)))
			}
		}
	}
	stopped, err := stopLinuxPIDFile(previous.PIDPath, previous.Executable, ops)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	return systemdErr
}

func stopLinuxPIDFile(pidPath, expectedExecutable string, ops linuxServiceOperations) (bool, error) {
	preflight, err := inspectCurrentLinuxPIDFile(pidPath, expectedExecutable, ops)
	if err != nil {
		return false, err
	}
	return stopInspectedLinuxPIDFile(preflight, expectedExecutable, ops)
}

func ResumePreviousService(previous PreviousService) error {
	if err := validatePreviousServiceRemoval(previous); err != nil {
		return err
	}
	_, unitExists, err := previousSystemdUnit(previous)
	if err != nil {
		return err
	}
	if unitExists {
		if _, err := exec.LookPath("systemctl"); err == nil {
			if output, startErr := runPreviousSystemctl("--user", "start", previous.ServiceName); startErr == nil {
				return nil
			} else if strings.TrimSpace(previous.Executable) == "" {
				return fmt.Errorf("systemctl --user start %s: %w: %s", previous.ServiceName, startErr, strings.TrimSpace(string(output)))
			}
		}
	}
	if strings.TrimSpace(previous.Executable) == "" {
		return nil
	}
	info, err := os.Lstat(previous.Executable)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("旧版可执行文件不是安全普通文件: %s", previous.Executable)
	}
	return StartDetached(previous.Executable, "daemon")
}

func RemovePreviousService(previous PreviousService) error {
	if err := validatePreviousServiceRemoval(previous); err != nil {
		return err
	}
	if err := removePreviousPersistence(previous); err != nil {
		return err
	}
	for _, path := range []string{previous.PIDPath, previous.LauncherPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return removePreviousExecutable(previous)
}

var runPreviousSystemctl = func(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}

func inspectPreviousPersistencePlatform(previous PreviousService) error {
	unitPath, exists, err := previousSystemdUnit(previous)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := validateSafePreviousRegular(unitPath); err != nil {
		return untrustedPreviousService("systemd unit: %v", err)
	}
	data, err := readSafeRegularFile(unitPath, 256*1024)
	if err != nil {
		return untrustedPreviousService("inspect systemd unit: %v", err)
	}
	if normalizePreviousSystemdUnit(string(data)) != normalizePreviousSystemdUnit(previousSystemdUnitContents(previous)) {
		return untrustedPreviousService("systemd unit targets an unknown executable or state directory")
	}
	return nil
}

func removePreviousPersistencePlatform(previous PreviousService) error {
	unitPath, exists, err := previousSystemdUnit(previous)
	if err != nil || !exists {
		return err
	}
	if err := inspectPreviousPersistencePlatform(previous); err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if output, disableErr := runPreviousSystemctl("--user", "disable", "--now", previous.ServiceName); disableErr != nil {
			return fmt.Errorf("systemctl --user disable --now %s: %w: %s", previous.ServiceName, disableErr, strings.TrimSpace(string(output)))
		}
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if output, reloadErr := runPreviousSystemctl("--user", "daemon-reload"); reloadErr != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w: %s", reloadErr, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func previousSystemdUnit(previous PreviousService) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, untrustedPreviousService("resolve systemd unit home: %v", err)
	}
	configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	unitPath, err := filepath.Abs(filepath.Join(configHome, "systemd", "user", previous.ServiceName))
	if err != nil {
		return "", false, untrustedPreviousService("resolve systemd unit: %v", err)
	}
	unitPath = filepath.Clean(unitPath)
	info, err := os.Lstat(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := validateNoLinkedAncestor(unitPath); err != nil {
			return "", false, untrustedPreviousService("systemd unit boundary: %v", err)
		}
		return unitPath, false, nil
	}
	if err != nil {
		return "", false, untrustedPreviousService("inspect systemd unit: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, untrustedPreviousService("systemd unit is not a safe regular file: %s", unitPath)
	}
	return unitPath, true, nil
}

func previousSystemdUnitContents(previous PreviousService) string {
	return `[Unit]
Description=Codex Meter local JSONL analytics service
After=default.target

[Service]
Type=simple
Environment=` + systemdQuote("CODEX_METER_HOME="+previous.StateDir) + `
ExecStart=` + systemdQuote(previous.Executable) + ` daemon
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`
}

func normalizePreviousSystemdUnit(value string) string {
	return strings.TrimSuffix(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
}

func StopPreviousService(previous PreviousService) error {
	if err := SuspendPreviousService(previous); err != nil {
		return err
	}
	return RemovePreviousService(previous)
}

func RemovePreviousExecutable(previous PreviousService) error {
	return removePreviousExecutable(previous)
}

func RenameNoReplace(source, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}

func OpenForRenameValidation(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("无法包装迁移验证句柄: %s", path)
	}
	return file, nil
}

func StartDetached(executable string, args ...string) error {
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	return command.Start()
}

func HideConsole() {}

func HasGUI() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func OpenURL(rawURL string) error {
	if !HasGUI() {
		return fmt.Errorf("无图形环境")
	}
	return exec.Command("xdg-open", rawURL).Start()
}

func RemoveInstalledExecutable(executable, stateDir string, purge bool) error {
	if err := ValidateInstalledRemoval(executable, stateDir, purge); err != nil {
		return err
	}
	exeAbs, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	stateAbs, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if err := os.Remove(exeAbs); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := SyncParent(exeAbs); err != nil {
		return fmt.Errorf("sync removed executable: %w", err)
	}
	if purge {
		if err := os.RemoveAll(stateAbs); err != nil {
			return err
		}
		return SyncParent(stateAbs)
	}
	return nil
}
