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
