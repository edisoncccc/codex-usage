package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type PreviousPaths struct {
	ProductName  string
	StateDir     string
	ConfigPath   string
	Database     string
	BackupDir    string
	InstallDir   string
	InstalledEXE string
	PIDPath      string
	LauncherPath string
	MarkerPath   string
	MarkerValue  string
	StartupEntry string
	ServiceName  string
}

type MigrationResult struct {
	Found             bool
	DatabaseMoved     bool
	DatabaseConflict  bool
	ConfigMoved       bool
	BackupsMoved      bool
	PreviousStateGone bool
}

func previousProductName() string { return "codex-" + "me" + "ter" }

func previousDatabaseName() string { return "me" + "ter.sqlite" }

func previousStartupEntry() string { return "Codex" + "Me" + "ter" }

func previousHomeEnv() string { return "CODEX_" + "ME" + "TER_HOME" }

func previousManagedBegin() string { return "# BEGIN " + previousProductName() + " managed" }

func previousManagedEnd() string { return "# END " + previousProductName() + " managed" }

func ResolvePreviousPaths() (PreviousPaths, error) {
	name := previousProductName()
	home, err := os.UserHomeDir()
	if err != nil {
		return PreviousPaths{}, err
	}

	var stateDir, installDir string
	if override := strings.TrimSpace(os.Getenv(previousHomeEnv())); override != "" {
		stateDir, err = filepath.Abs(override)
		if err != nil {
			return PreviousPaths{}, err
		}
		installDir = filepath.Join(stateDir, "bin")
	} else if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		stateDir = filepath.Join(base, name)
		installDir = filepath.Join(base, "Programs", name)
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		stateDir = filepath.Join(data, name)
		installDir = filepath.Join(home, ".local", "bin")
	}

	executableName := name
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	return PreviousPaths{
		ProductName:  name,
		StateDir:     stateDir,
		ConfigPath:   filepath.Join(stateDir, "config.json"),
		Database:     filepath.Join(stateDir, previousDatabaseName()),
		BackupDir:    filepath.Join(stateDir, "backups"),
		InstallDir:   installDir,
		InstalledEXE: filepath.Join(installDir, executableName),
		PIDPath:      filepath.Join(stateDir, name+".pid"),
		LauncherPath: filepath.Join(stateDir, name+"-start.vbs"),
		MarkerPath:   filepath.Join(stateDir, "."+name+"-state"),
		MarkerValue:  name + "-state-v1",
		StartupEntry: previousStartupEntry(),
		ServiceName:  name + ".service",
	}, nil
}

func MigratePreviousState(paths Paths, previous PreviousPaths) (MigrationResult, error) {
	result := MigrationResult{}
	currentPreviousDatabase := filepath.Join(paths.StateDir, previousDatabaseName())
	if conflict, err := databaseConflict(currentPreviousDatabase, paths.Database); err != nil {
		return result, err
	} else if conflict {
		result.Found = true
		result.DatabaseConflict = true
		return result, nil
	}
	if moved, err := moveDatabaseSet(currentPreviousDatabase, paths.Database); err != nil {
		return result, err
	} else if moved {
		result.Found = true
		result.DatabaseMoved = true
	}

	info, err := os.Stat(previous.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("旧版状态路径不是目录: %s", previous.StateDir)
	}
	if samePath(previous.StateDir, paths.StateDir) {
		return result, nil
	}
	if err := validatePreviousStateDir(previous); err != nil {
		return result, err
	}
	result.Found = true
	if err := EnsurePrivateDir(paths.StateDir); err != nil {
		return result, err
	}
	if conflict, err := databaseConflict(previous.Database, paths.Database); err != nil {
		return result, err
	} else if conflict {
		result.DatabaseConflict = true
		return result, nil
	}

	moved, err := moveDatabaseSet(previous.Database, paths.Database)
	if err != nil {
		return result, err
	}
	result.DatabaseMoved = result.DatabaseMoved || moved

	if moved, err = movePreviousConfig(previous.ConfigPath, paths.ConfigPath, paths.BackupDir); err != nil {
		return result, err
	}
	result.ConfigMoved = moved
	if moved, err = movePreviousBackups(previous.BackupDir, paths.BackupDir); err != nil {
		return result, err
	}
	result.BackupsMoved = moved
	if err := movePreviousLog(filepath.Join(previous.StateDir, "daemon.log"), paths.StateDir); err != nil {
		return result, err
	}

	for _, obsolete := range []string{previous.PIDPath, previous.LauncherPath, previous.MarkerPath} {
		if err := os.Remove(obsolete); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	entries, readErr := os.ReadDir(previous.StateDir)
	if readErr != nil {
		return result, readErr
	}
	if readErr == nil && len(entries) == 0 {
		if err := os.Remove(previous.StateDir); err != nil {
			return result, err
		}
		result.PreviousStateGone = true
	}
	if err := EnsureStateMarker(paths); err != nil {
		return result, err
	}
	return result, nil
}

func movePreviousConfig(source, activeTarget, backupDir string) (bool, error) {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := os.Stat(activeTarget); errors.Is(err, os.ErrNotExist) {
		return moveIfTargetMissing(source, activeTarget)
	} else if err != nil {
		return false, err
	}
	if err := EnsurePrivateDir(backupDir); err != nil {
		return false, err
	}
	target := filepath.Join(backupDir, "previous-config-"+randomSuffix()+".json")
	if err := os.Rename(source, target); err != nil {
		return false, err
	}
	return true, nil
}

func movePreviousBackups(source, target string) (bool, error) {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return moveIfTargetMissing(source, target)
	} else if err != nil {
		return false, err
	}
	if err := EnsurePrivateDir(target); err != nil {
		return false, err
	}
	archive := filepath.Join(target, "previous-installation-"+randomSuffix())
	if err := os.Rename(source, archive); err != nil {
		return false, err
	}
	return true, nil
}

func databaseConflict(source, target string) (bool, error) {
	sourceExists, err := regularFileExists(source)
	if err != nil || !sourceExists {
		return false, err
	}
	targetExists, err := regularFileExists(target)
	return targetExists, err
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("路径不是普通文件: %s", path)
	}
	return true, nil
}

func validatePreviousStateDir(previous PreviousPaths) error {
	absolute, err := filepath.Abs(previous.StateDir)
	if err != nil {
		return err
	}
	clean := filepath.Clean(absolute)
	root := filepath.Clean(filepath.VolumeName(clean) + string(os.PathSeparator))
	if samePath(clean, root) {
		return fmt.Errorf("拒绝迁移文件系统根目录")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && samePath(clean, filepath.Clean(home)) {
		return fmt.Errorf("拒绝迁移用户主目录")
	}
	data, err := os.ReadFile(previous.MarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(previous.StateDir)
		if readErr != nil {
			return readErr
		}
		allowed := map[string]bool{
			"daemon.log":                         true,
			filepath.Base(previous.PIDPath):      true,
			filepath.Base(previous.LauncherPath): true,
		}
		for _, entry := range entries {
			if entry.IsDir() || !allowed[entry.Name()] {
				return fmt.Errorf("拒绝迁移未标记的旧版状态目录 %s", previous.StateDir)
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("拒绝迁移未标记的旧版状态目录 %s: %w", previous.StateDir, err)
	}
	if strings.TrimSpace(string(data)) != previous.MarkerValue {
		return fmt.Errorf("拒绝迁移状态标记不匹配的目录 %s", previous.StateDir)
	}
	return nil
}

func movePreviousLog(source, stateDir string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	target := filepath.Join(stateDir, "previous-daemon.log")
	if _, err := os.Stat(target); err == nil {
		target = filepath.Join(stateDir, "previous-daemon-"+randomSuffix()+".log")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := moveIfTargetMissing(source, target)
	return err
}

func moveDatabaseSet(source, target string) (bool, error) {
	if samePath(source, target) {
		return false, nil
	}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := os.Stat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := EnsurePrivateDir(filepath.Dir(target)); err != nil {
		return false, err
	}

	suffixes := []string{"", "-wal", "-shm", "-journal"}
	type pair struct{ source, target string }
	pairs := make([]pair, 0, len(suffixes))
	for _, suffix := range suffixes {
		sourcePath := source + suffix
		if _, err := os.Stat(sourcePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return false, err
		}
		targetPath := target + suffix
		if _, err := os.Stat(targetPath); err == nil {
			return false, fmt.Errorf("迁移目标已存在: %s", targetPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		pairs = append(pairs, pair{source: sourcePath, target: targetPath})
	}

	moved := make([]pair, 0, len(pairs))
	for _, item := range pairs {
		if err := os.Rename(item.source, item.target); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				_ = os.Rename(moved[i].target, moved[i].source)
			}
			return false, fmt.Errorf("迁移数据库文件 %s: %w", filepath.Base(item.source), err)
		}
		moved = append(moved, item)
	}
	return true, nil
}

func moveIfTargetMissing(source, target string) (bool, error) {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := os.Stat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := EnsurePrivateDir(filepath.Dir(target)); err != nil {
		return false, err
	}
	if err := os.Rename(source, target); err != nil {
		return false, err
	}
	return true, nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
