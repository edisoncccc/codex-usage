//go:build !windows && !linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
)

func InstallService(executable, stateDir string) (ServiceResult, error) {
	return ServiceResult{Warning: "当前平台没有自动服务安装器"}, nil
}
func LockDown(path string) error             { return os.Chmod(path, 0o700) }
func SetPrivateUmask()                       {}
func UninstallService(stateDir string) error { return nil }
func StopPreviousService(previous PreviousService) error {
	if pid, err := readPIDFile(previous.PIDPath); err == nil && pid != os.Getpid() {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
	}
	_ = os.Remove(previous.PIDPath)
	return nil
}
func RemovePreviousExecutable(previous PreviousService) error {
	return removePreviousExecutable(previous)
}
func StartDetached(executable string, args ...string) error {
	return exec.Command(executable, args...).Start()
}
func HideConsole()                {}
func HasGUI() bool                { return false }
func OpenURL(rawURL string) error { return fmt.Errorf("当前平台不支持自动打开浏览器") }
func RemoveInstalledExecutable(executable, stateDir string, purge bool) error {
	if purge {
		if err := ValidatePurgeStateDir(stateDir); err != nil {
			return err
		}
	}
	if err := os.Remove(executable); err != nil && !os.IsNotExist(err) {
		return err
	}
	if purge {
		return os.RemoveAll(stateDir)
	}
	return nil
}
