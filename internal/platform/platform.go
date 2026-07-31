package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ServiceResult struct {
	Installed bool
	Started   bool
	Warning   string
	Detail    string
}

func WritePID(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "codex-usage.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

func ReadPID(stateDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "codex-usage.pid"))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("无效 PID 文件")
	}
	return pid, nil
}

func ValidatePurgeStateDir(stateDir string) error {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	clean := filepath.Clean(absolute)
	root := filepath.Clean(filepath.VolumeName(clean) + string(os.PathSeparator))
	if samePlatformPath(clean, root) {
		return fmt.Errorf("拒绝 purge：状态目录不能是文件系统根目录")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && samePlatformPath(clean, filepath.Clean(home)) {
		return fmt.Errorf("拒绝 purge：状态目录不能是用户主目录")
	}
	data, err := os.ReadFile(filepath.Join(absolute, ".codex-usage-state"))
	if err != nil {
		return fmt.Errorf("拒绝 purge：%s 缺少 codex-usage 状态标记", absolute)
	}
	if strings.TrimSpace(string(data)) != "codex-usage-state-v1" {
		return fmt.Errorf("拒绝 purge：%s 状态标记无效", absolute)
	}
	return nil
}

func samePlatformPath(a, b string) bool {
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
