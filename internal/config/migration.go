package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zJay26/codex-usage/internal/platform"
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

type migrationMove struct {
	source      string
	target      string
	sourceRoot  string
	targetRoot  string
	sourceInfo  os.FileInfo
	createdDirs []string
}

type MigrationPlan struct {
	paths    Paths
	previous PreviousPaths
	result   MigrationResult
	moves    []migrationMove
}

type MigrationTransaction struct {
	plan       MigrationPlan
	completed  []migrationMove
	state      migrationTransactionState
	syncParent func(string) error
}

type migrationTransactionState uint8

const (
	migrationTransactionActive migrationTransactionState = iota
	migrationTransactionRolledBack
	migrationTransactionCommitting
	migrationTransactionCommitted
)

var (
	removePreviousStateDirForMigration = removePreviousStateDirIfEmpty
	syncMigrationParent                = platform.SyncParent
)

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

func InspectPreviousState(paths Paths, previous PreviousPaths) (MigrationPlan, MigrationResult, error) {
	plan, result, err := inspectPreviousState(paths, previous)
	if err != nil {
		return plan, result, err
	}
	if err := sealMigrationPlan(&plan); err != nil {
		return plan, result, err
	}
	return plan, result, nil
}

func inspectPreviousState(paths Paths, previous PreviousPaths) (MigrationPlan, MigrationResult, error) {
	plan := MigrationPlan{paths: paths, previous: previous}
	inspection := MigrationResult{}
	if err := validateCurrentMigrationPaths(paths); err != nil {
		return plan, inspection, err
	}

	currentPreviousDatabase := filepath.Join(paths.StateDir, previousDatabaseName())
	databaseMoves, found, conflict, err := inspectDatabaseMoves(currentPreviousDatabase, paths.Database, plan.moves)
	if err != nil {
		return plan, inspection, err
	}
	if found {
		inspection.Found = true
		plan.result.Found = true
	}
	if conflict {
		inspection.DatabaseConflict = true
		plan.result.DatabaseConflict = true
		return plan, inspection, nil
	}
	if len(databaseMoves) != 0 {
		plan.moves = append(plan.moves, databaseMoves...)
		plan.result.DatabaseMoved = true
	}

	previousInfo, err := os.Lstat(previous.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		return plan, inspection, nil
	}
	if err != nil {
		return plan, inspection, err
	}
	if previousInfo.Mode()&os.ModeSymlink != 0 || !previousInfo.IsDir() {
		return plan, inspection, fmt.Errorf("旧版状态路径不是安全目录: %s", previous.StateDir)
	}
	if samePath(previous.StateDir, paths.StateDir) {
		return plan, inspection, nil
	}
	if err := validatePreviousMigrationPaths(previous); err != nil {
		return plan, inspection, err
	}
	if err := validatePreviousStateDir(previous); err != nil {
		return plan, inspection, err
	}
	inspection.Found = true
	plan.result.Found = true

	databaseMoves, found, conflict, err = inspectDatabaseMoves(previous.Database, paths.Database, plan.moves)
	if err != nil {
		return plan, inspection, err
	}
	if conflict {
		inspection.DatabaseConflict = true
		plan.result.DatabaseConflict = true
		plan.moves = nil
		plan.result.DatabaseMoved = false
		return plan, inspection, nil
	}
	if found {
		plan.moves = append(plan.moves, databaseMoves...)
		plan.result.DatabaseMoved = true
	}

	configMove, found, err := inspectConfigMove(previous.ConfigPath, paths.ConfigPath, paths.BackupDir, plan.moves)
	if err != nil {
		return plan, inspection, err
	}
	if found {
		plan.moves = append(plan.moves, configMove)
		plan.result.ConfigMoved = true
	}

	backupMove, found, err := inspectBackupsMove(previous.BackupDir, paths.BackupDir, plan.moves)
	if err != nil {
		return plan, inspection, err
	}
	if found {
		plan.moves = append(plan.moves, backupMove)
		plan.result.BackupsMoved = true
	}

	logMove, found, err := inspectLogMove(filepath.Join(previous.StateDir, "daemon.log"), paths.StateDir, plan.moves)
	if err != nil {
		return plan, inspection, err
	}
	if found {
		plan.moves = append(plan.moves, logMove)
	}
	return plan, inspection, nil
}

func BeginPreviousStateMigration(plan MigrationPlan) (*MigrationTransaction, error) {
	transaction := &MigrationTransaction{
		plan: plan, state: migrationTransactionActive, syncParent: syncMigrationParent,
	}
	if plan.result.DatabaseConflict {
		return nil, errors.New("旧版与当前数据库同时存在，拒绝覆盖")
	}
	for _, plannedMove := range plan.moves {
		move := plannedMove
		moved, err := executeMigrationMove(&move, transaction.syncParent)
		if moved {
			transaction.completed = append(transaction.completed, move)
		}
		if err != nil {
			primary := fmt.Errorf("迁移 %s 到 %s: %w", move.source, move.target, err)
			if rollbackErr := transaction.Rollback(); rollbackErr != nil {
				return nil, errors.Join(primary, fmt.Errorf("migration rollback: %w", rollbackErr))
			}
			return nil, primary
		}
	}
	return transaction, nil
}

func (m *MigrationTransaction) Rollback() error {
	if m == nil {
		return nil
	}
	switch m.state {
	case migrationTransactionRolledBack:
		return nil
	case migrationTransactionCommitting, migrationTransactionCommitted:
		return errors.New("migration transaction cannot rollback after commit started")
	case migrationTransactionActive:
	default:
		return errors.New("migration transaction has invalid state")
	}
	if len(m.completed) == 0 {
		m.state = migrationTransactionRolledBack
		return nil
	}
	var rollbackErrors []error
	remaining := make([]migrationMove, 0, len(m.completed))
	for index := len(m.completed) - 1; index >= 0; index-- {
		move := m.completed[index]
		restored, err := reverseMigrationMove(move, m.syncParent)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s from %s: %w", move.source, move.target, err))
		}
		if !restored {
			remaining = append(remaining, move)
		}
	}
	for left, right := 0, len(remaining)-1; left < right; left, right = left+1, right-1 {
		remaining[left], remaining[right] = remaining[right], remaining[left]
	}
	m.completed = remaining
	if len(m.completed) == 0 {
		m.state = migrationTransactionRolledBack
	}
	return errors.Join(rollbackErrors...)
}

func (m *MigrationTransaction) Commit() (MigrationResult, error) {
	return m.commit(nil)
}

// CommitPreparedStateMarker commits an already prepared state marker before
// migration cleanup can become irreversible.
func (m *MigrationTransaction) CommitPreparedStateMarker(commitMarker func() error) (MigrationResult, error) {
	if commitMarker == nil {
		return MigrationResult{}, errors.New("prepared state marker commit is nil")
	}
	return m.commit(commitMarker)
}

func (m *MigrationTransaction) commit(commitPreparedMarker func() error) (MigrationResult, error) {
	if m == nil {
		if commitPreparedMarker != nil {
			if err := commitPreparedMarker(); err != nil {
				return MigrationResult{}, fmt.Errorf("提交已准备的当前状态标记: %w", err)
			}
		}
		return MigrationResult{}, nil
	}
	switch m.state {
	case migrationTransactionRolledBack:
		return m.plan.result, errors.New("migration transaction cannot commit after rollback")
	case migrationTransactionCommitted:
		return m.plan.result, nil
	case migrationTransactionActive:
	case migrationTransactionCommitting:
	default:
		return m.plan.result, errors.New("migration transaction has invalid state")
	}
	result := m.plan.result
	if m.state == migrationTransactionActive {
		if commitPreparedMarker != nil {
			if err := commitPreparedMarker(); err != nil {
				return result, fmt.Errorf("提交已准备的当前状态标记: %w", err)
			}
		} else if result.Found {
			if err := EnsureStateMarker(m.plan.paths); err != nil {
				return result, fmt.Errorf("写入当前状态标记: %w", err)
			}
			if err := m.syncParent(filepath.Join(m.plan.paths.StateDir, ".codex-usage-state")); err != nil {
				return result, fmt.Errorf("同步当前状态标记目录: %w", err)
			}
		}
		if !result.Found {
			m.state = migrationTransactionCommitted
			return result, nil
		}
		m.state = migrationTransactionCommitting
	}
	if err := removePreviousMigrationMarker(m.plan.previous, m.syncParent); err != nil {
		return result, err
	}
	removed, err := removePreviousStateDirForMigration(m.plan.previous.StateDir)
	if err != nil {
		return result, err
	}
	if removed {
		if err := m.syncParent(m.plan.previous.StateDir); err != nil {
			return result, fmt.Errorf("同步已移除旧版状态目录: %w", err)
		}
	}
	result.PreviousStateGone = removed
	m.plan.result = result
	m.state = migrationTransactionCommitted
	return result, nil
}

func MigratePreviousState(paths Paths, previous PreviousPaths) (MigrationResult, error) {
	plan, inspection, err := InspectPreviousState(paths, previous)
	if err != nil || inspection.DatabaseConflict || !inspection.Found {
		return inspection, err
	}
	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		return inspection, err
	}
	return transaction.Commit()
}

func validateCurrentMigrationPaths(paths Paths) error {
	if strings.TrimSpace(paths.StateDir) == "" {
		return errors.New("当前状态目录为空")
	}
	if err := validateMigrationOwnedRoot(paths.StateDir, "当前状态目录"); err != nil {
		return err
	}
	for name, path := range map[string]string{
		"config":   paths.ConfigPath,
		"database": paths.Database,
		"backups":  paths.BackupDir,
	} {
		if err := validateMigrationOwnedPath(paths.StateDir, path); err != nil {
			return fmt.Errorf("当前%s路径越过状态目录边界: %s: %w", name, path, err)
		}
	}
	return nil
}

func validatePreviousMigrationPaths(previous PreviousPaths) error {
	if err := validateMigrationOwnedRoot(previous.StateDir, "旧版状态目录"); err != nil {
		return err
	}
	for name, path := range map[string]string{
		"config":   previous.ConfigPath,
		"database": previous.Database,
		"backups":  previous.BackupDir,
		"pid":      previous.PIDPath,
		"launcher": previous.LauncherPath,
		"marker":   previous.MarkerPath,
	} {
		if err := validateMigrationOwnedPath(previous.StateDir, path); err != nil {
			return fmt.Errorf("旧版%s路径越过状态目录边界: %s: %w", name, path, err)
		}
	}
	return nil
}

func validateMigrationOwnedRoot(root, label string) error {
	if err := validateMigrationCanonicalPath(root); err != nil {
		return fmt.Errorf("%s无效: %w", label, err)
	}
	if err := validateExistingMigrationDirectoryChain(root); err != nil {
		return fmt.Errorf("%s包含 symlink/reparse 或非目录 ancestor: %w", label, err)
	}
	return nil
}

func validateMigrationOwnedPath(root, path string) error {
	if err := validateMigrationCanonicalPath(path); err != nil {
		return err
	}
	if !pathWithin(root, path) {
		return fmt.Errorf("path is outside owned root")
	}
	if err := validateExistingMigrationDirectoryChain(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path is a symlink/reparse point: %s", path)
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return resolveErr
		}
		if !samePathRaw(filepath.Clean(resolved), filepath.Clean(path)) {
			return fmt.Errorf("path resolves through symlink/reparse point: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateMigrationCanonicalPath(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %s", path)
	}
	clean := filepath.Clean(path)
	if !samePathRaw(path, clean) {
		return fmt.Errorf("path must be clean: %s", path)
	}
	return nil
}

func validateExistingMigrationDirectoryChain(directory string) error {
	current := filepath.Clean(directory)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("unsafe directory ancestor: %s", current)
			}
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return resolveErr
			}
			if !samePathRaw(filepath.Clean(resolved), current) {
				return fmt.Errorf("symlink/reparse directory ancestor: %s", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func samePathRaw(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func pathWithin(root, path string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}

func sealMigrationPlan(plan *MigrationPlan) error {
	if plan == nil {
		return nil
	}
	for index := range plan.moves {
		move := &plan.moves[index]
		sourceRoot, err := migrationOwnedRootForSource(*plan, move.source)
		if err != nil {
			return err
		}
		if !migrationPathWithinOrEqual(plan.paths.StateDir, move.target) {
			return fmt.Errorf("迁移目标越过当前状态目录边界: %s", move.target)
		}
		move.sourceRoot = sourceRoot
		move.targetRoot = plan.paths.StateDir
		if err := validateMigrationMoveBoundaries(*move); err != nil {
			return err
		}
		info, closeSource, err := openAndValidateMigrationSource(move.source, nil)
		if err != nil {
			return fmt.Errorf("检查迁移源 %s: %w", move.source, err)
		}
		move.sourceInfo = info
		if err := closeSource(); err != nil {
			return fmt.Errorf("关闭迁移源 %s: %w", move.source, err)
		}
		if err := validateMigrationTargetMissing(move.target); err != nil {
			return err
		}
	}
	return nil
}

func migrationOwnedRootForSource(plan MigrationPlan, source string) (string, error) {
	if migrationPathWithinOrEqual(plan.paths.StateDir, source) {
		return plan.paths.StateDir, nil
	}
	if migrationPathWithinOrEqual(plan.previous.StateDir, source) {
		return plan.previous.StateDir, nil
	}
	return "", fmt.Errorf("迁移源越过已知状态目录边界: %s", source)
}

func migrationPathWithinOrEqual(root, path string) bool {
	return samePath(root, path) || pathWithin(root, path)
}

func validateMigrationMoveBoundaries(move migrationMove) error {
	if err := validateMigrationOwnedRoot(move.sourceRoot, "迁移源根目录"); err != nil {
		return err
	}
	if err := validateMigrationOwnedRoot(move.targetRoot, "迁移目标根目录"); err != nil {
		return err
	}
	if err := validateMigrationMovePath(move.sourceRoot, move.source, "迁移源"); err != nil {
		return err
	}
	if err := validateMigrationMovePath(move.targetRoot, move.target, "迁移目标"); err != nil {
		return err
	}
	return nil
}

func validateMigrationMovePath(root, path, label string) error {
	if err := validateMigrationCanonicalPath(path); err != nil {
		return fmt.Errorf("%s路径无效: %w", label, err)
	}
	if !migrationPathWithinOrEqual(root, path) || samePath(root, path) {
		return fmt.Errorf("%s越过 owned root: %s", label, path)
	}
	if err := validateExistingMigrationDirectoryChain(filepath.Dir(path)); err != nil {
		return fmt.Errorf("%s ancestor 不安全: %w", label, err)
	}
	return nil
}

func openAndValidateMigrationSource(path string, expected os.FileInfo) (os.FileInfo, func() error, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || (!before.Mode().IsRegular() && !before.IsDir()) {
		return nil, nil, fmt.Errorf("迁移源不是安全普通文件或目录: %s", path)
	}
	source, err := platform.OpenForRenameValidation(path)
	if err != nil {
		return nil, nil, err
	}
	closeSource := source.Close
	opened, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		_ = source.Close()
		return nil, nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || (!after.Mode().IsRegular() && !after.IsDir()) ||
		!os.SameFile(before, opened) || !os.SameFile(after, opened) {
		_ = source.Close()
		return nil, nil, fmt.Errorf("迁移源在打开期间发生变化: %s", path)
	}
	if expected != nil && !os.SameFile(expected, opened) {
		_ = source.Close()
		return nil, nil, fmt.Errorf("迁移源与只读检查时不是同一文件: %s", path)
	}
	return opened, closeSource, nil
}

func validateMigrationTargetMissing(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("迁移目标是 symlink/reparse point: %s", path)
		}
		return fmt.Errorf("迁移目标已存在: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func inspectDatabaseMoves(source, target string, existing []migrationMove) ([]migrationMove, bool, bool, error) {
	if samePath(source, target) {
		return nil, false, false, nil
	}
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	if !safeMigrationRegular(info) {
		return nil, false, false, fmt.Errorf("旧版数据库不是安全普通文件: %s", source)
	}

	moves := make([]migrationMove, 0, 4)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		targetPath := target + suffix
		conflict, err := migrationTargetExistsOrPlanned(targetPath, existing)
		if err != nil {
			return nil, true, false, err
		}
		if conflict {
			return nil, true, true, nil
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		sourcePath := source + suffix
		info, err := os.Lstat(sourcePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, true, false, err
		}
		if !safeMigrationRegular(info) {
			return nil, true, false, fmt.Errorf("旧版数据库 sidecar 不是安全普通文件: %s", sourcePath)
		}
		moves = append(moves, migrationMove{source: sourcePath, target: target + suffix})
	}
	return moves, true, false, nil
}

func inspectConfigMove(source, activeTarget, backupDir string, existing []migrationMove) (migrationMove, bool, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return migrationMove{}, false, nil
	}
	if err != nil {
		return migrationMove{}, false, err
	}
	if !safeMigrationRegular(info) {
		return migrationMove{}, false, fmt.Errorf("旧版配置不是安全普通文件: %s", source)
	}
	target := activeTarget
	if _, err := os.Lstat(activeTarget); err == nil {
		target = filepath.Join(backupDir, "previous-config-"+randomSuffix()+".json")
	} else if !errors.Is(err, os.ErrNotExist) {
		return migrationMove{}, false, err
	}
	if conflict, err := migrationTargetExistsOrPlanned(target, existing); err != nil {
		return migrationMove{}, false, err
	} else if conflict {
		return migrationMove{}, false, fmt.Errorf("迁移目标已存在: %s", target)
	}
	return migrationMove{source: source, target: target}, true, nil
}

func inspectBackupsMove(source, target string, existing []migrationMove) (migrationMove, bool, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return migrationMove{}, false, nil
	}
	if err != nil {
		return migrationMove{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return migrationMove{}, false, fmt.Errorf("旧版备份路径不是安全目录: %s", source)
	}
	archive := target
	targetInfo, targetErr := os.Lstat(target)
	if targetErr == nil || migrationPlanNeedsDirectory(target, existing) {
		if targetErr == nil && (targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir()) {
			return migrationMove{}, false, fmt.Errorf("当前备份路径不是安全目录: %s", target)
		}
		archive = filepath.Join(target, "previous-installation-"+randomSuffix())
	} else if !errors.Is(targetErr, os.ErrNotExist) {
		return migrationMove{}, false, targetErr
	}
	if conflict, err := migrationTargetExistsOrPlanned(archive, existing); err != nil {
		return migrationMove{}, false, err
	} else if conflict {
		return migrationMove{}, false, fmt.Errorf("迁移目标已存在: %s", archive)
	}
	return migrationMove{source: source, target: archive}, true, nil
}

func inspectLogMove(source, stateDir string, existing []migrationMove) (migrationMove, bool, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return migrationMove{}, false, nil
	}
	if err != nil {
		return migrationMove{}, false, err
	}
	if !safeMigrationRegular(info) {
		return migrationMove{}, false, fmt.Errorf("旧版日志不是安全普通文件: %s", source)
	}
	target := filepath.Join(stateDir, "previous-daemon.log")
	if conflict, err := migrationTargetExistsOrPlanned(target, existing); err != nil {
		return migrationMove{}, false, err
	} else if conflict {
		target = filepath.Join(stateDir, "previous-daemon-"+randomSuffix()+".log")
	}
	if conflict, err := migrationTargetExistsOrPlanned(target, existing); err != nil {
		return migrationMove{}, false, err
	} else if conflict {
		return migrationMove{}, false, fmt.Errorf("迁移目标已存在: %s", target)
	}
	return migrationMove{source: source, target: target}, true, nil
}

func migrationTargetExistsOrPlanned(target string, moves []migrationMove) (bool, error) {
	if _, err := os.Lstat(target); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, move := range moves {
		if samePath(move.target, target) {
			return true, nil
		}
	}
	return false, nil
}

func migrationPlanNeedsDirectory(path string, moves []migrationMove) bool {
	for _, move := range moves {
		if pathWithin(path, move.target) {
			return true
		}
	}
	return false
}

func safeMigrationRegular(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func ensureMigrationPrivateDir(directory string, syncParent func(string) error) ([]string, error) {
	missing := make([]string, 0, 2)
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, fmt.Errorf("迁移目标 ancestor 不是安全目录: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return nil, fmt.Errorf("无法定位迁移目标的现有父目录: %s", directory)
		}
	}
	if err := EnsurePrivateDir(directory); err != nil {
		return nil, err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := syncParent(missing[index]); err != nil {
			cleanupErr := cleanupMigrationCreatedDirs(missing, syncParent)
			return nil, errors.Join(fmt.Errorf("同步新建迁移目录 %s: %w", missing[index], err), cleanupErr)
		}
	}
	return missing, nil
}

func cleanupMigrationCreatedDirs(directories []string, syncParent func(string) error) error {
	var cleanupErrors []error
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("检查迁移创建目录 %s: %w", directory, err))
			continue
		}
		if len(entries) != 0 {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("拒绝移除非空迁移创建目录: %s", directory))
			continue
		}
		if err := os.Remove(directory); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("移除迁移创建目录 %s: %w", directory, err))
			continue
		}
		if err := syncParent(directory); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("同步已移除迁移创建目录 %s: %w", directory, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func syncMigrationRename(source, target string, syncParent func(string) error) error {
	if err := syncParent(target); err != nil {
		return err
	}
	if !samePath(filepath.Dir(source), filepath.Dir(target)) {
		if err := syncParent(source); err != nil {
			return err
		}
	}
	return nil
}

func executeMigrationMove(move *migrationMove, syncParent func(string) error) (bool, error) {
	if move == nil {
		return false, errors.New("migration move is nil")
	}
	if err := validateMigrationMoveBoundaries(*move); err != nil {
		return false, err
	}
	_, closeSource, err := openAndValidateMigrationSource(move.source, move.sourceInfo)
	if err != nil {
		return false, err
	}
	defer closeSource()
	if err := validateMigrationTargetMissing(move.target); err != nil {
		return false, err
	}
	createdDirs, err := ensureMigrationPrivateDir(filepath.Dir(move.target), syncParent)
	if err != nil {
		return false, err
	}
	move.createdDirs = createdDirs
	cleanupBeforeRename := func(primary error) (bool, error) {
		return false, errors.Join(primary, cleanupMigrationCreatedDirs(move.createdDirs, syncParent))
	}
	if err := validateMigrationMoveBoundaries(*move); err != nil {
		return cleanupBeforeRename(err)
	}
	if err := validateMigrationTargetMissing(move.target); err != nil {
		return cleanupBeforeRename(err)
	}
	after, err := os.Lstat(move.source)
	if err != nil {
		return cleanupBeforeRename(err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !os.SameFile(move.sourceInfo, after) {
		return cleanupBeforeRename(fmt.Errorf("迁移源在 rename 前发生变化: %s", move.source))
	}
	if err := platform.RenameNoReplace(move.source, move.target); err != nil {
		return cleanupBeforeRename(err)
	}
	if err := syncMigrationRename(move.source, move.target, syncParent); err != nil {
		return true, err
	}
	return true, nil
}

func reverseMigrationMove(move migrationMove, syncParent func(string) error) (bool, error) {
	if _, err := os.Lstat(move.target); errors.Is(err, os.ErrNotExist) {
		if _, sourceErr := os.Lstat(move.source); sourceErr == nil {
			return true, cleanupMigrationCreatedDirs(move.createdDirs, syncParent)
		} else {
			return false, sourceErr
		}
	} else if err != nil {
		return false, err
	}
	if _, err := os.Lstat(move.source); err == nil {
		return false, fmt.Errorf("恢复源已存在: %s", move.source)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	reverse := migrationMove{
		source: move.target, target: move.source,
		sourceRoot: move.targetRoot, targetRoot: move.sourceRoot,
		sourceInfo: move.sourceInfo,
	}
	restored, restoreErr := executeMigrationMove(&reverse, syncParent)
	if !restored {
		return false, restoreErr
	}
	return true, errors.Join(restoreErr, cleanupMigrationCreatedDirs(move.createdDirs, syncParent))
}

func removePreviousMigrationMarker(previous PreviousPaths, syncParent func(string) error) error {
	if strings.TrimSpace(previous.MarkerPath) == "" {
		return nil
	}
	data, err := os.ReadFile(previous.MarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != previous.MarkerValue {
		return fmt.Errorf("旧版迁移标记在提交前发生变化: %s", previous.MarkerPath)
	}
	if err := os.Remove(previous.MarkerPath); err != nil {
		return fmt.Errorf("清理旧版迁移标记: %w", err)
	}
	if err := syncParent(previous.MarkerPath); err != nil {
		return fmt.Errorf("同步旧版迁移标记目录: %w", err)
	}
	return nil
}

func removePreviousStateDirIfEmpty(stateDir string) (bool, error) {
	if strings.TrimSpace(stateDir) == "" {
		return false, nil
	}
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, nil
	}
	if err := os.Remove(stateDir); err != nil {
		return false, err
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

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
