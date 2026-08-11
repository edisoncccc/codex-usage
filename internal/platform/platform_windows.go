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
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return ServiceResult{}, err
	}
	launcher := filepath.Join(stateDir, "codex-usage-start.vbs")
	vbsPath := strings.ReplaceAll(executable, `"`, `""`)
	vbsState := strings.ReplaceAll(stateDir, `"`, `""`)
	vbs := `Set shell = CreateObject("WScript.Shell")` + "\r\n" +
		`shell.Environment("Process")("CODEX_USAGE_HOME") = "` + vbsState + `"` + "\r\n" +
		`shell.Run Chr(34) & "` + vbsPath + `" & Chr(34) & " daemon", 0, False` + "\r\n"
	if err := os.WriteFile(launcher, []byte(vbs), 0o600); err != nil {
		return ServiceResult{}, err
	}
	command := `wscript.exe //B //Nologo "` + launcher + `"`
	args := []string{"add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "CodexUsage", "/t", "REG_SZ", "/d", command, "/f"}
	output, err := exec.Command("reg.exe", args...).CombinedOutput()
	if err != nil {
		return ServiceResult{}, fmt.Errorf("注册当前用户登录启动项: %w: %s", err, strings.TrimSpace(string(output)))
	}
	result := ServiceResult{Installed: true, Detail: "HKCU 当前用户登录启动项"}
	if err := StartDetached(executable, "daemon"); err != nil {
		result.Warning = "启动项已安装，但本次后台服务启动失败: " + err.Error()
		return result, nil
	}
	result.Started = true
	return result, nil
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

func UninstallService(executable, stateDir string) error {
	pidPath := filepath.Join(stateDir, "codex-usage.pid")
	if err := stopWindowsExecutable(executable); err != nil {
		return err
	}
	_, _ = exec.Command("reg.exe", "delete",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "CodexUsage", "/f").CombinedOutput()
	for _, path := range []string{pidPath, filepath.Join(stateDir, "codex-usage-start.vbs")} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func StopPreviousService(previous PreviousService) error {
	if err := stopWindowsExecutable(previous.Executable); err != nil {
		return err
	}
	if previous.StartupEntry != "" {
		_, _ = exec.Command("reg.exe", "delete",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", previous.StartupEntry, "/f").CombinedOutput()
	}
	for _, path := range []string{previous.PIDPath, previous.LauncherPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("%s后台服务 PID %d 被拒绝访问；请右键选择“以管理员身份运行”后重试 install: %w", action, pid, err)
	}
	return fmt.Errorf("%s后台服务 PID %d: %w", action, pid, err)
}

func RemovePreviousExecutable(previous PreviousService) error {
	return removePreviousExecutable(previous)
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
