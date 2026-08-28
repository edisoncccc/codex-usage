package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ServiceMode string

const (
	ServiceModePersistent       ServiceMode = "persistent"
	ServiceModeDetachedFallback ServiceMode = "detached_fallback"
)

type RemovalMode string

const (
	RemovalModeRemoved   RemovalMode = "removed"
	RemovalModeScheduled RemovalMode = "scheduled"
)

type ServiceResult struct {
	Installed bool
	Started   bool
	Mode      ServiceMode
	Warning   string
	Detail    string
}

type PreviousService struct {
	StateDir     string
	Executable   string
	InstallDir   string
	PIDPath      string
	LauncherPath string
	StartupEntry string
	ServiceName  string
}

const (
	previousProductName  = "codex-meter"
	previousStartupEntry = "CodexMeter"
	previousServiceName  = previousProductName + ".service"
	previousHomeEnv      = "CODEX_METER_HOME"
)

var (
	inspectPreviousPersistence = inspectPreviousPersistencePlatform
	removePreviousPersistence  = removePreviousPersistencePlatform
	syncServiceParent          = SyncParent
)

type serviceInstallFailure struct {
	primary  error
	rollback []error
}

func (e *serviceInstallFailure) Error() string {
	if e == nil {
		return ""
	}
	details := make([]string, 0, len(e.rollback))
	for _, rollbackErr := range e.rollback {
		details = append(details, rollbackErr.Error())
	}
	return fmt.Sprintf("%v; rollback: %s", e.primary, strings.Join(details, "; "))
}

func (e *serviceInstallFailure) Unwrap() []error {
	if e == nil {
		return nil
	}
	unwrapped := make([]error, 0, len(e.rollback)+1)
	unwrapped = append(unwrapped, e.primary)
	unwrapped = append(unwrapped, e.rollback...)
	return unwrapped
}

func joinServiceInstallRollback(primary error, rollbackErrors ...error) error {
	filtered := rollbackErrors[:0]
	for _, rollbackErr := range rollbackErrors {
		if rollbackErr != nil {
			filtered = append(filtered, rollbackErr)
		}
	}
	if len(filtered) == 0 {
		return primary
	}
	return &serviceInstallFailure{primary: primary, rollback: filtered}
}

type linuxServiceOperations struct {
	LookPath              func(string) (string, error)
	RunSystemctl          func(...string) ([]byte, error)
	RunLoginctl           func(...string) ([]byte, error)
	StartDetached         func(string, ...string) error
	RemoveUnit            func(string) error
	ReadProcessExecutable func(int) (string, error)
	OpenProcessHandle     func(int) (int, error)
	SignalProcessHandle   func(int, os.Signal) error
	ProcessHandleExited   func(int) (bool, error)
	CloseProcessHandle    func(int) error
	Sleep                 func(time.Duration)
}

var activeLinuxServiceOperations linuxServiceOperations

type PermissionError struct {
	Operation string
	Err       error
}

func (e *PermissionError) Error() string {
	if e == nil {
		return "permission_required"
	}
	if e.Err == nil {
		return "permission_required: " + e.Operation
	}
	return fmt.Sprintf("permission_required: %s: %v", e.Operation, e.Err)
}

func (e *PermissionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func InstalledExecutableRemovalMode() RemovalMode {
	if runtime.GOOS == "windows" {
		return RemovalModeScheduled
	}
	return RemovalModeRemoved
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
	return readPIDFile(filepath.Join(stateDir, "codex-usage.pid"))
}

func readPIDFile(path string) (int, error) {
	data, err := readSafeRegularFile(path, 64)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("无效 PID 文件")
	}
	return pid, nil
}

func readSafeRegularFile(path string, maximumBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("文件不是安全普通文件: %s", path)
	}
	if err := validateNoLinkedAncestor(path); err != nil {
		return nil, err
	}
	file, err := OpenForRenameValidation(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("文件在安全打开期间发生变化: %s", path)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("文件超过安全读取上限: %s", path)
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("文件在安全读取期间发生变化: %s", path)
	}
	return data, nil
}

func removePreviousExecutable(previous PreviousService) error {
	if strings.TrimSpace(previous.Executable) == "" {
		return nil
	}
	if err := validatePreviousExecutableCleanup(previous); err != nil {
		return err
	}
	if err := validateSafePreviousRegular(previous.Executable); err != nil {
		return err
	}
	if err := os.Remove(previous.Executable); err != nil && !os.IsNotExist(err) {
		return err
	}
	if previousInstallDirectoryIsProductOwned() {
		_ = os.Remove(previous.InstallDir)
	}
	return nil
}

func validatePreviousServiceIdentity(previous PreviousService) error {
	expected, err := expectedPreviousService()
	if err != nil {
		return untrustedPreviousService("resolve known legacy service: %v", err)
	}
	for _, path := range []struct {
		field    string
		observed string
		expected string
	}{
		{field: "state directory", observed: previous.StateDir, expected: expected.StateDir},
		{field: "install directory", observed: previous.InstallDir, expected: expected.InstallDir},
		{field: "executable", observed: previous.Executable, expected: expected.Executable},
		{field: "PID metadata", observed: previous.PIDPath, expected: expected.PIDPath},
		{field: "launcher metadata", observed: previous.LauncherPath, expected: expected.LauncherPath},
	} {
		field := path.field
		pair := [2]string{path.observed, path.expected}
		if strings.TrimSpace(pair[0]) == "" || !samePlatformPath(filepath.Clean(pair[0]), filepath.Clean(pair[1])) {
			return untrustedPreviousService("%s is outside the known legacy boundary: %s", field, pair[0])
		}
		absolute, absErr := filepath.Abs(pair[0])
		if absErr != nil || !samePlatformPath(filepath.Clean(absolute), filepath.Clean(pair[0])) {
			return untrustedPreviousService("%s must be a canonical absolute path: %s", field, pair[0])
		}
		if strings.ContainsAny(pair[0], "\x00\r\n") {
			return untrustedPreviousService("%s contains control characters", field)
		}
	}
	if previous.StartupEntry != previousStartupEntry {
		return untrustedPreviousService("unknown startup entry %q", previous.StartupEntry)
	}
	if previous.ServiceName != previousServiceName {
		return untrustedPreviousService("unknown service name %q", previous.ServiceName)
	}
	if err := validatePreviousExecutableCleanup(previous); err != nil {
		return untrustedPreviousService("legacy executable boundary: %v", err)
	}
	if err := validatePreviousDirectoryNotBroad(previous.StateDir); err != nil {
		return untrustedPreviousService("state directory boundary: %v", err)
	}
	for _, directory := range []struct{ label, path string }{
		{label: "state directory", path: previous.StateDir},
		{label: "install directory", path: previous.InstallDir},
	} {
		if err := validateSafePreviousDirectory(directory.path); err != nil {
			return untrustedPreviousService("%s: %v", directory.label, err)
		}
	}
	for _, file := range []struct{ label, path string }{
		{label: "executable", path: previous.Executable},
		{label: "PID metadata", path: previous.PIDPath},
		{label: "launcher metadata", path: previous.LauncherPath},
	} {
		if err := validateSafePreviousRegular(file.path); err != nil {
			return untrustedPreviousService("%s: %v", file.label, err)
		}
	}
	return nil
}

func validatePreviousServiceRemoval(previous PreviousService) error {
	if err := validatePreviousServiceIdentity(previous); err != nil {
		return err
	}
	if err := inspectPreviousPersistence(previous); err != nil {
		return err
	}
	return nil
}

func expectedPreviousService() (PreviousService, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return PreviousService{}, err
	}
	stateDir := strings.TrimSpace(os.Getenv(previousHomeEnv))
	installDir := ""
	if stateDir != "" {
		stateDir, err = filepath.Abs(stateDir)
		if err != nil {
			return PreviousService{}, err
		}
		installDir = filepath.Join(stateDir, "bin")
	} else if runtime.GOOS == "windows" {
		base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		stateDir = filepath.Join(base, previousProductName)
		installDir = filepath.Join(base, "Programs", previousProductName)
	} else {
		dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		stateDir = filepath.Join(dataHome, previousProductName)
		installDir = filepath.Join(home, ".local", "bin")
	}
	stateDir = filepath.Clean(stateDir)
	installDir = filepath.Clean(installDir)
	executableName := previousProductName
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	return PreviousService{
		StateDir:     stateDir,
		Executable:   filepath.Join(installDir, executableName),
		InstallDir:   installDir,
		PIDPath:      filepath.Join(stateDir, previousProductName+".pid"),
		LauncherPath: filepath.Join(stateDir, previousProductName+"-start.vbs"),
		StartupEntry: previousStartupEntry,
		ServiceName:  previousServiceName,
	}, nil
}

func previousInstallDirectoryIsProductOwned() bool {
	return strings.TrimSpace(os.Getenv(previousHomeEnv)) != "" || runtime.GOOS == "windows"
}

func validatePreviousDirectoryNotBroad(path string) error {
	clean := filepath.Clean(path)
	root := filepath.Clean(filepath.VolumeName(clean) + string(os.PathSeparator))
	if samePlatformPath(clean, root) {
		return fmt.Errorf("refuse filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil && samePlatformPath(clean, filepath.Clean(home)) {
		return fmt.Errorf("refuse user home")
	}
	return nil
}

func validateSafePreviousDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return validateNoLinkedAncestor(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("not a safe directory: %s", path)
	}
	return validateNoLinkedAncestor(path)
}

func validateSafePreviousRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return validateNoLinkedAncestor(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("not a safe regular file: %s", path)
	}
	return validateNoLinkedAncestor(path)
}

func validateNoLinkedAncestor(path string) error {
	current := filepath.Clean(path)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return resolveErr
			}
			if !samePlatformPath(filepath.Clean(resolved), current) {
				return fmt.Errorf("path contains a symlink or reparse point: %s", path)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func untrustedPreviousService(format string, args ...any) error {
	return fmt.Errorf("existing_install_untrusted: "+format, args...)
}

func validatePreviousExecutableCleanup(previous PreviousService) error {
	expected, expectedErr := expectedPreviousService()
	if expectedErr != nil {
		return expectedErr
	}
	if !samePlatformPath(filepath.Clean(previous.Executable), filepath.Clean(expected.Executable)) ||
		!samePlatformPath(filepath.Clean(previous.InstallDir), filepath.Clean(expected.InstallDir)) {
		return fmt.Errorf("previous executable is outside the known legacy install boundary")
	}
	executable, err := filepath.Abs(previous.Executable)
	if err != nil {
		return err
	}
	installDir, err := filepath.Abs(previous.InstallDir)
	if err != nil {
		return err
	}
	executable = filepath.Clean(executable)
	installDir = filepath.Clean(installDir)
	if !samePlatformPath(executable, filepath.Clean(previous.Executable)) ||
		!samePlatformPath(installDir, filepath.Clean(previous.InstallDir)) {
		return fmt.Errorf("previous cleanup paths must be absolute and canonical")
	}
	root := filepath.Clean(filepath.VolumeName(installDir) + string(os.PathSeparator))
	if samePlatformPath(installDir, root) {
		return fmt.Errorf("refuse previous cleanup from filesystem root")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && samePlatformPath(installDir, filepath.Clean(home)) {
		return fmt.Errorf("refuse previous cleanup from user home")
	}
	if !samePlatformPath(filepath.Dir(executable), installDir) {
		return fmt.Errorf("previous executable crosses install directory boundary: %s", executable)
	}
	return nil
}

func ValidatePurgeStateDir(stateDir string) error {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	clean := filepath.Clean(absolute)
	if !filepath.IsAbs(stateDir) || !samePlatformPath(clean, filepath.Clean(stateDir)) {
		return fmt.Errorf("拒绝 purge：状态目录必须是规范绝对路径")
	}
	root := filepath.Clean(filepath.VolumeName(clean) + string(os.PathSeparator))
	if samePlatformPath(clean, root) {
		return fmt.Errorf("拒绝 purge：状态目录不能是文件系统根目录")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && samePlatformPath(clean, filepath.Clean(home)) {
		return fmt.Errorf("拒绝 purge：状态目录不能是用户主目录")
	}
	if err := validateSafePreviousDirectory(clean); err != nil {
		return fmt.Errorf("拒绝 purge：状态目录边界不安全: %w", err)
	}
	markerPath := filepath.Join(clean, ".codex-usage-state")
	data, err := readSafeRegularFile(markerPath, 64)
	if err != nil {
		return fmt.Errorf("拒绝 purge：%s 缺少安全的 codex-usage 状态标记: %w", clean, err)
	}
	if strings.TrimSpace(string(data)) != "codex-usage-state-v1" {
		return fmt.Errorf("拒绝 purge：%s 状态标记无效", clean)
	}
	return nil
}

// ValidateInstalledRemoval verifies the current-user program and state
// boundaries before any uninstall hook is allowed to mutate service state.
func ValidateInstalledRemoval(executable, stateDir string, purge bool) error {
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
	if !filepath.IsAbs(executable) || !samePlatformPath(executableAbsolute, filepath.Clean(executable)) ||
		!filepath.IsAbs(stateDir) || !samePlatformPath(stateAbsolute, filepath.Clean(stateDir)) {
		return fmt.Errorf("existing_install_untrusted: uninstall paths must be canonical and absolute")
	}
	wantExecutable := "codex-usage"
	if runtime.GOOS == "windows" {
		wantExecutable += ".exe"
	}
	if !samePlatformPath(filepath.Base(executableAbsolute), wantExecutable) {
		return fmt.Errorf("existing_install_untrusted: executable name is not %s", wantExecutable)
	}
	root := filepath.Clean(filepath.VolumeName(stateAbsolute) + string(os.PathSeparator))
	if samePlatformPath(stateAbsolute, root) {
		return fmt.Errorf("existing_install_untrusted: state directory is the filesystem root")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && samePlatformPath(stateAbsolute, filepath.Clean(home)) {
		return fmt.Errorf("existing_install_untrusted: state directory is the user home")
	}
	if err := validateSafePreviousDirectory(stateAbsolute); err != nil {
		return fmt.Errorf("existing_install_untrusted: unsafe state directory: %w", err)
	}
	if err := validateSafePreviousRegular(executableAbsolute); err != nil {
		return fmt.Errorf("existing_install_untrusted: unsafe executable: %w", err)
	}
	if !knownCurrentUserInstallBoundary(executableAbsolute, stateAbsolute) {
		return fmt.Errorf("existing_install_untrusted: executable is outside the current-user install boundary")
	}
	if purge {
		return ValidatePurgeStateDir(stateAbsolute)
	}
	return nil
}

func knownCurrentUserInstallBoundary(executable, stateDir string) bool {
	if samePlatformPath(filepath.Dir(executable), filepath.Join(stateDir, "bin")) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return samePlatformPath(stateDir, filepath.Join(base, "codex-usage")) &&
			samePlatformPath(executable, filepath.Join(base, "Programs", "codex-usage", "codex-usage.exe"))
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return samePlatformPath(stateDir, filepath.Join(data, "codex-usage")) &&
		samePlatformPath(executable, filepath.Join(home, ".local", "bin", "codex-usage"))
}

func samePlatformPath(a, b string) bool {
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// SyncParent makes a completed directory-entry change durable on platforms
// where syncing directories is supported. Windows rename operations already
// request write-through semantics and directory handles are not synced here.
func SyncParent(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func currentSystemdUnitContents(executable, stateDir string) string {
	return `[Unit]
Description=Codex Usage local JSONL analytics service
After=default.target

[Service]
Type=simple
Environment=` + systemdQuote("CODEX_USAGE_HOME="+stateDir) + `
ExecStart=` + systemdQuote(executable) + ` daemon
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`
}

func systemdQuote(path string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`%`, `%%`,
	).Replace(path)
	return `"` + escaped + `"`
}
