package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/zJay26/codex-usage/internal/pricing"
)

const (
	DefaultPort        = 43189
	databaseName       = "usage.sqlite"
	legacyManagedBegin = "# BEGIN codex-usage managed"
	legacyManagedEnd   = "# END codex-usage managed"
)

type Config struct {
	ListenAddress       string                      `json:"listen_address"`
	Port                int                         `json:"port"`
	ScanIntervalSeconds int                         `json:"scan_interval_seconds"`
	ExtraCodexHomes     []string                    `json:"extra_codex_homes,omitempty"`
	PricingOverrides    map[string]pricing.Override `json:"pricing_overrides,omitempty"`
}

type Paths struct {
	StateDir      string
	ConfigPath    string
	InstallRecord string
	Database      string
	BackupDir     string
	InstallDir    string
	InstalledEXE  string
}

func Default() Config {
	return Config{
		ListenAddress:       "127.0.0.1",
		Port:                DefaultPort,
		ScanIntervalSeconds: 600,
	}
}

func ResolvePaths() (Paths, error) {
	if override := strings.TrimSpace(os.Getenv("CODEX_USAGE_HOME")); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return Paths{}, err
		}
		if err := validateDedicatedStateDir(abs); err != nil {
			return Paths{}, err
		}
		name := "codex-usage"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return Paths{
			StateDir:      abs,
			ConfigPath:    filepath.Join(abs, "config.json"),
			InstallRecord: filepath.Join(abs, "install.json"),
			Database:      filepath.Join(abs, databaseName),
			BackupDir:     filepath.Join(abs, "backups"),
			InstallDir:    filepath.Join(abs, "bin"),
			InstalledEXE:  filepath.Join(abs, "bin", name),
		}, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	var stateDir, installDir string
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		stateDir = filepath.Join(base, "codex-usage")
		installDir = filepath.Join(base, "Programs", "codex-usage")
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		stateDir = filepath.Join(data, "codex-usage")
		installDir = filepath.Join(home, ".local", "bin")
	}
	if err := validateDedicatedStateDir(stateDir); err != nil {
		return Paths{}, err
	}
	name := "codex-usage"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return Paths{
		StateDir:      stateDir,
		ConfigPath:    filepath.Join(stateDir, "config.json"),
		InstallRecord: filepath.Join(stateDir, "install.json"),
		Database:      filepath.Join(stateDir, databaseName),
		BackupDir:     filepath.Join(stateDir, "backups"),
		InstallDir:    installDir,
		InstalledEXE:  filepath.Join(installDir, name),
	}, nil
}

func validateDedicatedStateDir(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	clean := filepath.Clean(absolute)
	root := filepath.Clean(filepath.VolumeName(clean) + string(os.PathSeparator))
	equalPath := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if equalPath(clean, root) {
		return fmt.Errorf("CODEX_USAGE_HOME 不能是文件系统根目录")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && equalPath(clean, home) {
		return fmt.Errorf("CODEX_USAGE_HOME 不能是用户主目录")
	}
	entries, readErr := os.ReadDir(clean)
	if errors.Is(readErr, os.ErrNotExist) {
		return nil
	}
	if readErr != nil {
		return readErr
	}
	if len(entries) == 0 {
		return nil
	}
	marker, markerErr := os.ReadFile(filepath.Join(clean, ".codex-usage-state"))
	if markerErr == nil && strings.TrimSpace(string(marker)) == "codex-usage-state-v1" {
		return nil
	}
	managed := map[string]bool{
		"backups": true, "bin": true, "config.json": true, "install.json": true, "daemon.log": true,
		databaseName: true, databaseName + "-shm": true, databaseName + "-wal": true,
		databaseName + "-journal": true, "codex-usage.pid": true,
		"codex-usage-start.vbs": true, ".codex-usage-state": true,
	}
	previousName := previousProductName()
	for _, name := range []string{
		previousDatabaseName(), previousDatabaseName() + "-shm", previousDatabaseName() + "-wal",
		previousDatabaseName() + "-journal", previousName + ".pid", previousName + "-start.vbs",
		"." + previousName + "-state",
	} {
		managed[name] = true
	}
	for _, entry := range entries {
		if !managed[entry.Name()] && !strings.HasPrefix(entry.Name(), ".codex-usage-") {
			return fmt.Errorf("%s 不是专用 Codex Usage 状态目录（发现 %q）；请选择新的空目录", clean, entry.Name())
		}
	}
	return nil
}

func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func Load(paths Paths) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(paths.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析 %s: %w", paths.ConfigPath, err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1"
	}
	if cfg.ListenAddress != "127.0.0.1" && cfg.ListenAddress != "localhost" {
		return Config{}, fmt.Errorf("拒绝非 loopback 监听地址 %q", cfg.ListenAddress)
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("无效端口 %d", cfg.Port)
	}
	if cfg.ScanIntervalSeconds < 600 {
		cfg.ScanIntervalSeconds = 600
	}
	cfg.ExtraCodexHomes = cleanHomes(cfg.ExtraCodexHomes)
	cfg.PricingOverrides, err = pricing.NormalizeOverrides(cfg.PricingOverrides)
	if err != nil {
		return Config{}, fmt.Errorf("无效 pricing_overrides: %w", err)
	}
	return cfg, nil
}

func Save(paths Paths, cfg Config) error {
	if err := EnsurePrivateDir(paths.StateDir); err != nil {
		return err
	}
	cfg.ExtraCodexHomes = cleanHomes(cfg.ExtraCodexHomes)
	if cfg.ScanIntervalSeconds < 600 {
		cfg.ScanIntervalSeconds = 600
	}
	var err error
	cfg.PricingOverrides, err = pricing.NormalizeOverrides(cfg.PricingOverrides)
	if err != nil {
		return fmt.Errorf("无效 pricing_overrides: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWrite(paths.ConfigPath, data, 0o600); err != nil {
		return err
	}
	return EnsureStateMarker(paths)
}

func EnsureStateMarker(paths Paths) error {
	if err := EnsurePrivateDir(paths.StateDir); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(paths.StateDir, ".codex-usage-state"),
		[]byte("codex-usage-state-v1\n"), 0o600)
}

func AddHome(paths Paths, raw string) (string, error) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录", abs)
	}
	cfg, err := Load(paths)
	if err != nil {
		return "", err
	}
	cfg.ExtraCodexHomes = append(cfg.ExtraCodexHomes, abs)
	if err := Save(paths, cfg); err != nil {
		return "", err
	}
	return abs, nil
}

func CodexHomes(cfg Config) ([]string, error) {
	var homes []string
	if explicit := strings.TrimSpace(os.Getenv("CODEX_HOME")); explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return nil, err
		}
		homes = append(homes, abs)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		homes = append(homes, filepath.Join(home, ".codex"))
	}
	homes = append(homes, cfg.ExtraCodexHomes...)
	return cleanHomes(homes), nil
}

func CodexConfigPath(home string) string {
	return filepath.Join(home, "config.toml")
}

func HasLegacyManagedOTel(home string) (bool, error) {
	data, err := os.ReadFile(CodexConfigPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte(legacyManagedBegin)) ||
		bytes.Contains(data, []byte(previousManagedBegin())), nil
}

// RemoveLegacyManagedOTel is upgrade cleanup for releases that used to add a
// managed exporter. It removes only our marker-delimited stanza and leaves any
// user or third-party [otel] configuration untouched.
func RemoveLegacyManagedOTel(home string) (bool, error) {
	path := CodexConfigPath(home)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	begin, endMarker := legacyManagedBegin, legacyManagedEnd
	start := bytes.Index(data, []byte(begin))
	if start < 0 {
		begin, endMarker = previousManagedBegin(), previousManagedEnd()
		start = bytes.Index(data, []byte(begin))
		if start < 0 {
			return false, nil
		}
	}
	endRel := bytes.Index(data[start:], []byte(endMarker))
	if endRel < 0 {
		return false, fmt.Errorf("发现不完整的 codex-usage managed stanza，拒绝自动修改")
	}
	end := start + endRel + len(endMarker)
	if end < len(data) && data[end] == '\r' {
		end++
	}
	if end < len(data) && data[end] == '\n' {
		end++
	}
	updated := append([]byte{}, data[:start]...)
	updated = append(updated, data[end:]...)
	updated = bytes.TrimRight(updated, "\r\n")
	if len(updated) > 0 {
		updated = append(updated, '\n')
	}
	var parsed map[string]any
	if len(updated) > 0 {
		if err := toml.Unmarshal(updated, &parsed); err != nil {
			return false, fmt.Errorf("移除后 TOML 校验失败，未修改: %w", err)
		}
	}
	if err := atomicWriteWithRollback(path, data, updated, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func cleanHomes(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		abs, err := filepath.Abs(value)
		if err != nil {
			continue
		}
		key := filepath.Clean(abs)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, filepath.Clean(abs))
	}
	sort.Strings(out)
	return out
}

func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "backup"
	}
	return hex.EncodeToString(buf[:])
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-usage-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(path); err == nil {
			swap := path + ".codex-usage-swap"
			_ = os.Remove(swap)
			if err := os.Rename(path, swap); err != nil {
				return err
			}
			if err := os.Rename(tmpName, path); err != nil {
				_ = os.Rename(swap, path)
				return err
			}
			_ = os.Remove(swap)
			return nil
		}
	}
	return os.Rename(tmpName, path)
}

func atomicWriteWithRollback(path string, original, updated []byte, mode os.FileMode) error {
	if err := atomicWrite(path, updated, mode); err != nil {
		return fmt.Errorf("原子写入 %s: %w", path, err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, updated) {
		_ = atomicWrite(path, original, mode)
		if err != nil {
			return fmt.Errorf("写入后读取校验失败并已回滚: %w", err)
		}
		return fmt.Errorf("写入后内容校验失败并已回滚")
	}
	return nil
}
