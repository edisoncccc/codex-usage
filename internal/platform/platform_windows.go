//go:build windows

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
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	showWindowHide        = 0
)

func InstallService(executable, stateDir string) (ServiceResult, error) {
	if strings.ContainsAny(executable+stateDir, "\x00\r\n") {
		return ServiceResult{}, fmt.Errorf("服务路径包含不安全的控制字符")
	}
	readRun := readCurrentWindowsRunValue
	writeRun := writeCurrentWindowsRunValue
	deleteRun := deleteCurrentWindowsRunValue
	startService := startCurrentWindowsServiceProcess
	syncParent := syncServiceParent
	preflight, err := inspectCurrentWindowsService(executable, stateDir)
	if err != nil {
		return ServiceResult{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return ServiceResult{}, err
	}
	if err := validateSafePreviousDirectory(stateDir); err != nil {
		return ServiceResult{}, untrustedPreviousService("current service state directory: %v", err)
	}
	createdLauncher := false
	runWriteAttempted := false
	failInstall := func(primary error) (ServiceResult, error) {
		rollbackErrors := rollbackWindowsServiceInstall(
			preflight, createdLauncher, runWriteAttempted, readRun, deleteRun, syncParent,
		)
		return ServiceResult{}, joinServiceInstallRollback(primary, rollbackErrors...)
	}
	if !preflight.launcherExists {
		if err := activateWindowsServiceLauncher(preflight.launcherPath, []byte(preflight.launcherContents)); err != nil {
			return ServiceResult{}, err
		}
		createdLauncher = true
	} else if err := validateExactWindowsLauncher(preflight.launcherPath, preflight.launcherContents); err != nil {
		return ServiceResult{}, err
	}
	runEntryExists, err := inspectExactCurrentWindowsRunWith(readRun, preflight.runCommand)
	if err != nil {
		return failInstall(err)
	}
	if !runEntryExists {
		runWriteAttempted = true
		if err := writeRun(preflight.runCommand); err != nil {
			return failInstall(&PermissionError{Operation: "注册当前用户登录启动项", Err: err})
		}
	}
	result := ServiceResult{Installed: true, Mode: ServiceModePersistent, Detail: "HKCU 当前用户登录启动项"}
	if err := startService(executable, "daemon"); err != nil {
		result.Warning = "启动项已安装，但本次后台服务启动失败: " + err.Error()
		return result, nil
	}
	result.Started = true
	return result, nil
}

func activateWindowsServiceLauncher(path string, contents []byte) error {
	parent := filepath.Dir(path)
	if err := validateSafePreviousDirectory(parent); err != nil {
		return untrustedPreviousService("launcher parent: %v", err)
	}
	temporary, err := os.CreateTemp(parent, ".codex-usage-start-*.tmp")
	if err != nil {
		return fmt.Errorf("create launcher stage: %w", err)
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
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := validateSafePreviousDirectory(parent); err != nil {
		return untrustedPreviousService("launcher parent changed: %v", err)
	}
	if err := RenameNoReplace(temporaryPath, path); err != nil {
		return fmt.Errorf("activate launcher without replacement: %w", err)
	}
	owned = false
	return nil
}

type currentWindowsServiceInspection struct {
	launcherPath     string
	launcherContents string
	runCommand       string
	launcherExists   bool
	runEntryExists   bool
}

func inspectCurrentWindowsService(executable, stateDir string) (currentWindowsServiceInspection, error) {
	inspection := currentWindowsServiceInspection{}
	if err := validateWindowsServiceInstallTarget(executable, stateDir); err != nil {
		return inspection, err
	}
	if err := validateSafePreviousDirectory(stateDir); err != nil {
		return inspection, untrustedPreviousService("current service state directory: %v", err)
	}
	if err := validateSafePreviousRegular(executable); err != nil {
		return inspection, untrustedPreviousService("current service executable: %v", err)
	}
	inspection.launcherPath = filepath.Join(stateDir, "codex-usage-start.vbs")
	inspection.launcherContents = windowsServiceLauncherContents(executable, stateDir)
	inspection.runCommand = windowsServiceRunCommand(inspection.launcherPath)
	info, err := os.Lstat(inspection.launcherPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return inspection, untrustedPreviousService("current launcher is not a safe regular file: %s", inspection.launcherPath)
		}
		if err := validateNoLinkedAncestor(inspection.launcherPath); err != nil {
			return inspection, untrustedPreviousService("current launcher boundary: %v", err)
		}
		data, readErr := readSafeRegularFile(inspection.launcherPath, 64*1024)
		if readErr != nil {
			return inspection, untrustedPreviousService("inspect current launcher: %v", readErr)
		}
		if normalizeWindowsLauncher(string(data)) != normalizeWindowsLauncher(inspection.launcherContents) {
			return inspection, untrustedPreviousService("current launcher targets an unknown executable or state directory")
		}
		inspection.launcherExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return inspection, untrustedPreviousService("inspect current launcher: %v", err)
	}
	found, err := inspectExactCurrentWindowsRun(inspection.runCommand)
	if err != nil {
		return inspection, err
	}
	inspection.runEntryExists = found
	return inspection, nil
}

func inspectExactCurrentWindowsRun(expected string) (bool, error) {
	readRun := readCurrentWindowsRunValue
	return inspectExactCurrentWindowsRunWith(readRun, expected)
}

func inspectExactCurrentWindowsRunWith(readRun func() (string, bool, error), expected string) (bool, error) {
	value, found, err := readRun()
	if err != nil {
		return false, &PermissionError{Operation: "检查当前用户登录启动项", Err: err}
	}
	if found && !strings.EqualFold(value, expected) {
		return false, untrustedPreviousService("HKCU Run entry CodexUsage targets an unknown command")
	}
	return found, nil
}

func rollbackWindowsServiceInstall(
	preflight currentWindowsServiceInspection,
	createdLauncher bool,
	runWriteAttempted bool,
	readRun func() (string, bool, error),
	deleteRun func() error,
	syncParent func(string) error,
) []error {
	rollbackErrors := make([]error, 0, 2)
	if runWriteAttempted {
		found, err := inspectExactCurrentWindowsRunWith(readRun, preflight.runCommand)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback inspect Run entry: %w", err))
		} else if found {
			if err := deleteRun(); err != nil {
				rollbackErrors = append(rollbackErrors, &PermissionError{Operation: "rollback remove current user startup entry", Err: err})
			}
		}
	}
	if createdLauncher {
		if err := rollbackExactCurrentWindowsLauncher(preflight.launcherPath, preflight.launcherContents, syncParent); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback remove launcher: %w", err))
		}
	}
	return rollbackErrors
}

func rollbackExactCurrentWindowsLauncher(path, expected string, syncParent func(string) error) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateExactWindowsLauncher(path, expected); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncParent(path)
}

func windowsServiceLauncherContents(executable, stateDir string) string {
	vbsPath := strings.ReplaceAll(executable, `"`, `""`)
	vbsState := strings.ReplaceAll(stateDir, `"`, `""`)
	return `Set shell = CreateObject("WScript.Shell")` + "\r\n" +
		`shell.Environment("Process")("CODEX_USAGE_HOME") = "` + vbsState + `"` + "\r\n" +
		`shell.Run Chr(34) & "` + vbsPath + `" & Chr(34) & " daemon", 0, False` + "\r\n"
}

func windowsServiceRunCommand(launcher string) string {
	return `wscript.exe //B //Nologo "` + launcher + `"`
}

func LockDown(path string) error {
	output, err := exec.Command("whoami.exe", "/user", "/fo", "csv", "/nh").Output()
	if err != nil {
		return err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), ",")
	if len(fields) < 2 {
		return fmt.Errorf("无法解析当前用户 SID")
	}
	sid := strings.Trim(strings.TrimSpace(fields[1]), `"`)
	if !strings.HasPrefix(strings.ToUpper(sid), "S-1-") {
		return fmt.Errorf("无效当前用户 SID")
	}
	acl := "*" + sid + ":(OI)(CI)F"
	result, err := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", acl).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %w: %s", err, strings.TrimSpace(string(result)))
	}
	return nil
}

func SetPrivateUmask() {}

func StopService(executable, _ string) error {
	stopService := stopCurrentWindowsServiceProcess
	return stopService(executable)
}

func UninstallService(executable, stateDir string) error {
	preflight, err := inspectCurrentWindowsService(executable, stateDir)
	if err != nil {
		return err
	}
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	pidExists, err := inspectCurrentWindowsPIDFile(pidPath)
	if err != nil {
		return err
	}
	stopService := stopCurrentWindowsServiceProcess
	if err := stopService(executable); err != nil {
		return err
	}
	if preflight.runEntryExists {
		found, err := inspectExactCurrentWindowsRun(preflight.runCommand)
		if err != nil {
			return err
		}
		if found {
			deleteRun := deleteCurrentWindowsRunValue
			if err := deleteRun(); err != nil {
				return &PermissionError{Operation: "移除当前用户登录启动项", Err: err}
			}
		}
	}
	if pidExists {
		if _, err := inspectCurrentWindowsPIDFile(pidPath); err != nil {
			return err
		}
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if preflight.launcherExists {
		if err := validateExactWindowsLauncher(preflight.launcherPath, preflight.launcherContents); err != nil {
			return err
		}
		if err := os.Remove(preflight.launcherPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func inspectCurrentWindowsPIDFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := validateNoLinkedAncestor(path); err != nil {
			return false, untrustedPreviousService("current PID metadata boundary: %v", err)
		}
		return false, nil
	}
	if err != nil {
		return false, untrustedPreviousService("inspect current PID metadata: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, untrustedPreviousService("current PID metadata is not a safe regular file: %s", path)
	}
	if err := validateNoLinkedAncestor(path); err != nil {
		return false, untrustedPreviousService("current PID metadata boundary: %v", err)
	}
	if _, err := readPIDFile(path); err != nil {
		return false, untrustedPreviousService("current PID metadata is invalid: %v", err)
	}
	return true, nil
}

func validateExactWindowsLauncher(path, expected string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return untrustedPreviousService("revalidate current launcher: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return untrustedPreviousService("current launcher is not a safe regular file: %s", path)
	}
	if err := validateNoLinkedAncestor(path); err != nil {
		return untrustedPreviousService("current launcher boundary: %v", err)
	}
	data, err := readSafeRegularFile(path, 64*1024)
	if err != nil {
		return untrustedPreviousService("read current launcher: %v", err)
	}
	if normalizeWindowsLauncher(string(data)) != normalizeWindowsLauncher(expected) {
		return untrustedPreviousService("current launcher changed before removal")
	}
	return nil
}

func SuspendPreviousService(previous PreviousService) error {
	if strings.TrimSpace(previous.Executable) == "" {
		return nil
	}
	if err := validatePreviousServiceRemoval(previous); err != nil {
		return err
	}
	stopService := stopPreviousWindowsServiceProcess
	return stopService(previous.Executable)
}

func ResumePreviousService(previous PreviousService) error {
	if strings.TrimSpace(previous.Executable) == "" {
		return nil
	}
	if err := validatePreviousServiceRemoval(previous); err != nil {
		return err
	}
	info, err := os.Lstat(previous.Executable)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
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

var (
	readCurrentWindowsRunValue = func() (string, bool, error) {
		return readWindowsRunValue("CodexUsage")
	}
	writeCurrentWindowsRunValue = func(value string) error {
		key, _, err := registry.CreateKey(registry.CURRENT_USER,
			`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer key.Close()
		return key.SetStringValue("CodexUsage", value)
	}
	deleteCurrentWindowsRunValue = func() error {
		return deleteWindowsRunValue("CodexUsage")
	}
	startCurrentWindowsServiceProcess = StartDetached
	stopCurrentWindowsServiceProcess  = stopWindowsExecutable
	stopPreviousWindowsServiceProcess = stopWindowsExecutable
	readPreviousWindowsRunValue       = func(name string) (string, bool, error) {
		return readWindowsRunValue(name)
	}
	deletePreviousWindowsRunValue = func(name string) error {
		return deleteWindowsRunValue(name)
	}
)

func readWindowsRunValue(name string) (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return value, err == nil, err
}

func deleteWindowsRunValue(name string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func inspectPreviousPersistencePlatform(previous PreviousService) error {
	value, found, err := readPreviousWindowsRunValue(previous.StartupEntry)
	if err != nil {
		return &PermissionError{Operation: "检查旧版当前用户登录启动项", Err: err}
	}
	expectedCommand := `wscript.exe //B //Nologo "` + previous.LauncherPath + `"`
	if found && !strings.EqualFold(strings.TrimSpace(value), expectedCommand) {
		return untrustedPreviousService("HKCU Run entry %q targets an unknown command", previous.StartupEntry)
	}
	data, err := readSafeRegularFile(previous.LauncherPath, 64*1024)
	if errors.Is(err, os.ErrNotExist) {
		if found {
			return untrustedPreviousService("HKCU Run entry %q targets a missing launcher", previous.StartupEntry)
		}
		return nil
	}
	if err != nil {
		return untrustedPreviousService("inspect launcher: %v", err)
	}
	expectedLauncher := previousWindowsLauncher(previous)
	if normalizeWindowsLauncher(string(data)) != normalizeWindowsLauncher(expectedLauncher) {
		return untrustedPreviousService("launcher targets an unknown executable or state directory")
	}
	return nil
}

func removePreviousPersistencePlatform(previous PreviousService) error {
	if err := deletePreviousWindowsRunValue(previous.StartupEntry); err != nil {
		return &PermissionError{Operation: "移除旧版当前用户登录启动项", Err: err}
	}
	return nil
}

func previousWindowsLauncher(previous PreviousService) string {
	executable := strings.ReplaceAll(previous.Executable, `"`, `""`)
	stateDir := strings.ReplaceAll(previous.StateDir, `"`, `""`)
	return `Set shell = CreateObject("WScript.Shell")` + "\r\n" +
		`shell.Environment("Process")("CODEX_METER_HOME") = "` + stateDir + `"` + "\r\n" +
		`shell.Run Chr(34) & "` + executable + `" & Chr(34) & " daemon", 0, False` + "\r\n"
}

func normalizeWindowsLauncher(value string) string {
	return strings.TrimSuffix(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
}

func StopPreviousService(previous PreviousService) error {
	if err := SuspendPreviousService(previous); err != nil {
		return err
	}
	return RemovePreviousService(previous)
}

var terminateWindowsProcessByPID = terminateWindowsProcess

func stopWindowsExecutable(executable string) error {
	target, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析后台服务路径: %w", err)
	}
	target = filepath.Clean(target)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("枚举后台进程: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return fmt.Errorf("读取后台进程列表: %w", err)
	}
	for {
		pid := entry.ProcessID
		if pid != 0 && pid != uint32(os.Getpid()) {
			processPath, queryErr := windowsProcessExecutable(pid)
			if queryErr == nil && strings.EqualFold(filepath.Clean(processPath), target) {
				if err := terminateWindowsProcessByPID(pid); err != nil {
					return windowsProcessError("停止", pid, err)
				}
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return fmt.Errorf("读取后台进程列表: %w", err)
		}
	}
	return nil
}

func windowsProcessExecutable(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func terminateWindowsProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.TerminateProcess(handle, 0); err != nil {
		return err
	}
	result, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("等待进程退出超时 (wait=%d)", result)
	}
	return nil
}

func windowsProcessError(action string, pid uint32, err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return &PermissionError{Operation: fmt.Sprintf("%s后台服务 PID %d", action, pid), Err: err}
	}
	return fmt.Errorf("%s后台服务 PID %d: %w", action, pid, err)
}

func RemovePreviousExecutable(previous PreviousService) error {
	return removePreviousExecutable(previous)
}

func RenameNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func OpenForRenameValidation(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("无法包装迁移验证句柄: %s", path)
	}
	return file, nil
}

func validateWindowsServiceInstallTarget(executable, stateDir string) error {
	executableAbsolute, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析服务可执行路径: %w", err)
	}
	stateAbsolute, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("解析服务状态路径: %w", err)
	}
	executableAbsolute = filepath.Clean(executableAbsolute)
	stateAbsolute = filepath.Clean(stateAbsolute)
	if !strings.EqualFold(executableAbsolute, filepath.Clean(executable)) ||
		!strings.EqualFold(stateAbsolute, filepath.Clean(stateDir)) {
		return fmt.Errorf("服务路径必须是规范绝对路径")
	}

	overrideTarget := filepath.Join(stateAbsolute, "bin", "codex-usage.exe")
	defaultBase := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if defaultBase == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			defaultBase = filepath.Join(home, "AppData", "Local")
		}
	}
	defaultTarget := filepath.Join(defaultBase, "Programs", "codex-usage", "codex-usage.exe")
	defaultState := filepath.Join(defaultBase, "codex-usage")
	if strings.EqualFold(executableAbsolute, overrideTarget) ||
		(strings.EqualFold(executableAbsolute, filepath.Clean(defaultTarget)) && strings.EqualFold(stateAbsolute, filepath.Clean(defaultState))) {
		return nil
	}
	return fmt.Errorf("服务可执行文件不是规范当前用户安装路径: %s", executableAbsolute)
}

func StartDetached(executable string, args ...string) error {
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	return command.Start()
}

func HideConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	user32 := syscall.NewLazyDLL("user32.dll")
	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	showWindow := user32.NewProc("ShowWindow")
	window, _, _ := getConsoleWindow.Call()
	if window != 0 {
		showWindow.Call(window, showWindowHide)
	}
}

func HasGUI() bool { return true }

func OpenURL(rawURL string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", rawURL).Start()
}

func RemoveInstalledExecutable(executable, stateDir string, purge bool) error {
	// A running Windows executable cannot delete itself. A narrowly scoped
	// helper script waits for this process to exit, removes only the resolved
	// install file, optionally removes the codex-usage state directory, then
	// deletes itself.
	exeAbs, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	stateAbs, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(exeAbs), "codex-usage.exe") ||
		exeAbs == filepath.VolumeName(exeAbs)+string(os.PathSeparator) {
		return fmt.Errorf("拒绝清理非 codex-usage 可执行文件")
	}
	if purge {
		if err := ValidatePurgeStateDir(stateAbs); err != nil {
			return err
		}
	}
	if strings.ContainsAny(exeAbs+stateAbs, "\"\r\n") {
		return fmt.Errorf("拒绝包含批处理控制字符的路径")
	}
	exeBatch := strings.ReplaceAll(exeAbs, "%", "%%")
	stateBatch := strings.ReplaceAll(stateAbs, "%", "%%")
	helper := filepath.Join(os.TempDir(), fmt.Sprintf("codex-usage-uninstall-%d.cmd", time.Now().UnixNano()))
	lines := []string{
		"@echo off",
		"ping 127.0.0.1 -n 3 > nul",
		`del /f /q "` + exeBatch + `"`,
	}
	if strings.EqualFold(filepath.Base(filepath.Dir(exeAbs)), "codex-usage") {
		parentBatch := strings.ReplaceAll(filepath.Dir(exeAbs), "%", "%%")
		lines = append(lines, `rmdir "`+parentBatch+`" 2>nul`)
	}
	if purge {
		lines = append(lines, `rmdir /s /q "`+stateBatch+`"`)
	}
	lines = append(lines, `del /f /q "%~f0"`)
	if err := os.WriteFile(helper, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o600); err != nil {
		return err
	}
	command := exec.Command("cmd.exe", "/c", helper)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess}
	return command.Start()
}
