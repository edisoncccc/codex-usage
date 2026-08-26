package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zJay26/codex-usage/internal/pricing"
)

func TestRemoveLegacyManagedOTelPreservesUserConfiguration(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	original := "# 用户注释\r\nmodel = \"gpt-5\"\r\n\r\n[otel]\r\n" +
		legacyManagedBegin + "\r\n" +
		`metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:43189/v1/metrics", protocol = "json" } }` + "\r\n" +
		legacyManagedEnd + "\r\n" +
		`log_user_prompt = false` + "\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := HasLegacyManagedOTel(home)
	if err != nil || !found {
		t.Fatalf("legacy detection found=%v err=%v", found, err)
	}
	changed, err := RemoveLegacyManagedOTel(home)
	if err != nil || !changed {
		t.Fatalf("legacy cleanup changed=%v err=%v", changed, err)
	}
	updated, _ := os.ReadFile(path)
	text := string(updated)
	if strings.Contains(text, "codex-usage managed") || strings.Contains(text, "127.0.0.1:43189") ||
		!strings.Contains(text, "# 用户注释") || !strings.Contains(text, `log_user_prompt = false`) {
		t.Fatalf("legacy cleanup damaged user config:\n%s", text)
	}
}

func TestRemoveLegacyManagedOTelLeavesThirdPartyExporterUntouched(t *testing.T) {
	home := t.TempDir()
	path := CodexConfigPath(home)
	original := "[otel]\n" +
		`metrics_exporter = { otlp-http = { endpoint = "http://collector:4318", protocol = "binary" } }` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := RemoveLegacyManagedOTel(home)
	if err != nil || changed {
		t.Fatalf("third-party config changed=%v err=%v", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatal("third-party exporter was modified")
	}
}

func TestResolvePathsRejectsNonDedicatedOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_USAGE_HOME", dir)
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("expected non-dedicated state directory rejection")
	}
}

func TestResolvePathsAcceptsEmptyOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_USAGE_HOME", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.StateDir != dir {
		t.Fatalf("state dir mismatch: %q", paths.StateDir)
	}
	if filepath.Base(paths.Database) != "usage.sqlite" {
		t.Fatalf("database name mismatch: %q", paths.Database)
	}
}

func TestResolvePathsIncludesInstallRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_USAGE_HOME", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "install.json")
	if paths.InstallRecord != want {
		t.Fatalf("install record=%q want=%q", paths.InstallRecord, want)
	}
	if err := os.WriteFile(paths.InstallRecord, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePaths(); err != nil {
		t.Fatalf("install record must remain within the dedicated state whitelist: %v", err)
	}
}

func TestMigratePreviousDatabaseNameInCurrentState(t *testing.T) {
	stateDir := t.TempDir()
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	previous, err := ResolvePreviousPaths()
	if err != nil {
		t.Fatal(err)
	}
	previous.StateDir = filepath.Join(stateDir, "not-present")
	legacyBase := filepath.Join(stateDir, previousDatabaseName())
	if err := os.WriteFile(legacyBase, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyBase+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := MigratePreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || !result.DatabaseMoved {
		t.Fatalf("unexpected migration result: %+v", result)
	}
	for _, path := range []string{paths.Database, paths.Database + "-wal"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing migrated file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(legacyBase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous database still exists: %v", err)
	}
}

func TestMigratePreviousStateDoesNotOverwriteParallelDatabase(t *testing.T) {
	stateDir := t.TempDir()
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	previous := PreviousPaths{StateDir: filepath.Join(stateDir, "not-present")}
	previousDatabase := filepath.Join(stateDir, previousDatabaseName())
	if err := os.WriteFile(previousDatabase, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Database, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := MigratePreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DatabaseConflict || !result.Found {
		t.Fatalf("expected non-destructive conflict: %+v", result)
	}
	for path, want := range map[string]string{previousDatabase: "previous", paths.Database: "current"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("database %s changed: got=%q err=%v", path, got, err)
		}
	}
}

func TestMigratePreviousProductStatePreservesData(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "current")
	previousState := filepath.Join(root, "previous")
	name := previousProductName()
	previous := PreviousPaths{
		ProductName: name, StateDir: previousState,
		ConfigPath:   filepath.Join(previousState, "config.json"),
		Database:     filepath.Join(previousState, previousDatabaseName()),
		BackupDir:    filepath.Join(previousState, "backups"),
		PIDPath:      filepath.Join(previousState, name+".pid"),
		LauncherPath: filepath.Join(previousState, name+"-start.vbs"),
		MarkerPath:   filepath.Join(previousState, "."+name+"-state"),
		MarkerValue:  name + "-state-v1",
	}
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	if err := os.MkdirAll(previous.BackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		previous.MarkerPath: previous.MarkerValue + "\n", previous.ConfigPath: `{"port":43189}`,
		previous.Database: "database", previous.Database + "-wal": "wal",
		filepath.Join(previous.BackupDir, "config.toml"): "backup", previous.PIDPath: "123\n",
		previous.LauncherPath: "launcher", filepath.Join(previousState, "daemon.log"): "log",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := MigratePreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || !result.DatabaseMoved || !result.ConfigMoved || !result.BackupsMoved || result.PreviousStateGone {
		t.Fatalf("unexpected migration result: %+v", result)
	}
	for _, path := range []string{
		paths.Database, paths.Database + "-wal", paths.ConfigPath,
		filepath.Join(paths.BackupDir, "config.toml"), filepath.Join(paths.StateDir, "previous-daemon.log"),
		filepath.Join(paths.StateDir, ".codex-usage-state"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing migrated file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(previous.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous migration marker still exists: %v", err)
	}
	for _, path := range []string{previous.PIDPath, previous.LauncherPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("previous service metadata was removed before platform cleanup: %s: %v", path, err)
		}
	}
}

func TestMigratePreviousResidualLogWithoutMarker(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "current")
	previousState := filepath.Join(root, "previous")
	if err := os.MkdirAll(previousState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previousState, "daemon.log"), []byte("previous log"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := PreviousPaths{
		StateDir: previousState, Database: filepath.Join(previousState, previousDatabaseName()),
		ConfigPath: filepath.Join(previousState, "config.json"), BackupDir: filepath.Join(previousState, "backups"),
		PIDPath:      filepath.Join(previousState, previousProductName()+".pid"),
		LauncherPath: filepath.Join(previousState, previousProductName()+"-start.vbs"),
		MarkerPath:   filepath.Join(previousState, "."+previousProductName()+"-state"),
		MarkerValue:  previousProductName() + "-state-v1",
	}
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	result, err := MigratePreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || !result.PreviousStateGone {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "previous-daemon.log"))
	if err != nil || string(data) != "previous log" {
		t.Fatalf("previous log was not preserved: %q err=%v", data, err)
	}
}

func TestMigratePreviousStateArchivesParallelConfigAndBackups(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "current")
	previousState := filepath.Join(root, "previous")
	name := previousProductName()
	previous := PreviousPaths{
		StateDir: previousState, ConfigPath: filepath.Join(previousState, "config.json"),
		Database: filepath.Join(previousState, previousDatabaseName()), BackupDir: filepath.Join(previousState, "backups"),
		PIDPath: filepath.Join(previousState, name+".pid"), LauncherPath: filepath.Join(previousState, name+"-start.vbs"),
		MarkerPath: filepath.Join(previousState, "."+name+"-state"), MarkerValue: name + "-state-v1",
	}
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	if err := os.MkdirAll(previous.BackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.BackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		previous.MarkerPath: previous.MarkerValue, previous.ConfigPath: "previous-config",
		filepath.Join(previous.BackupDir, "previous.toml"): "previous-backup",
		paths.ConfigPath: "current-config", filepath.Join(paths.BackupDir, "current.toml"): "current-backup",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := MigratePreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigMoved || !result.BackupsMoved || !result.PreviousStateGone {
		t.Fatalf("unexpected result: %+v", result)
	}
	active, err := os.ReadFile(paths.ConfigPath)
	if err != nil || string(active) != "current-config" {
		t.Fatalf("active config changed: %q err=%v", active, err)
	}
	configs, err := filepath.Glob(filepath.Join(paths.BackupDir, "previous-config-*.json"))
	if err != nil || len(configs) != 1 {
		t.Fatalf("previous config was not archived: %v err=%v", configs, err)
	}
	archives, err := filepath.Glob(filepath.Join(paths.BackupDir, "previous-installation-*", "previous.toml"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("previous backups were not archived: %v err=%v", archives, err)
	}
}

func TestInspectPreviousStateIsReadOnlyAndRollbackRestoresMoves(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	beforeDatabase, err := os.ReadFile(previous.Database)
	if err != nil {
		t.Fatal(err)
	}

	plan, result, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.DatabaseConflict || result.DatabaseMoved || result.ConfigMoved || result.BackupsMoved {
		t.Fatalf("inspection reported mutations: %+v", result)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != string(beforeDatabase) {
		t.Fatalf("inspection changed previous database: %q err=%v", got, err)
	}
	if _, err := os.Stat(paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created migration target: %v", err)
	}

	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Database); err != nil {
		t.Fatalf("migration did not move database: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("second rollback must be idempotent: %v", err)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != string(beforeDatabase) {
		t.Fatalf("rollback did not restore previous database: %q err=%v", got, err)
	}
	if _, err := os.Stat(paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left migrated database: %v", err)
	}
}

func TestBeginPreviousStateMigrationRollsBackPartialFailure(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigPath, []byte("late-conflict"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := BeginPreviousStateMigration(plan); err == nil {
		t.Fatal("expected late target conflict")
	}
	for path, want := range map[string]string{
		previous.Database:   "previous-database",
		previous.ConfigPath: "previous-config",
		paths.ConfigPath:    "late-conflict",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("partial migration did not restore %s: %q err=%v", path, got, err)
		}
	}
}

func TestBeginPreviousStateMigrationSyncFailureRollsBackMovedData(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("migration parent sync failed")
	injected := false
	originalSync := syncMigrationParent
	syncMigrationParent = func(path string) error {
		if !injected {
			if _, statErr := os.Lstat(paths.Database); statErr == nil {
				injected = true
				return syncErr
			}
		}
		return nil
	}
	t.Cleanup(func() { syncMigrationParent = originalSync })

	if _, err := BeginPreviousStateMigration(plan); !errors.Is(err, syncErr) {
		t.Fatalf("migration sync error=%v want %v", err, syncErr)
	}
	if !injected {
		t.Fatal("sync failure was not injected after rename")
	}
	for path, want := range map[string]string{
		previous.Database:   "previous-database",
		previous.ConfigPath: "previous-config",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("sync rollback did not restore %s: %q err=%v", path, got, readErr)
		}
	}
	for _, path := range []string{paths.Database, paths.ConfigPath} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("sync rollback left migration target %s: %v", path, statErr)
		}
	}
	if _, statErr := os.Lstat(paths.StateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("sync rollback left migration-created state directory: %v", statErr)
	}
}

func TestMigrationCommitPreservesPreviousServiceMetadata(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		t.Fatal(err)
	}
	result, err := transaction.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if !result.DatabaseMoved || !result.ConfigMoved || result.PreviousStateGone {
		t.Fatalf("unexpected commit result: %+v", result)
	}
	if _, err := os.Stat(previous.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration marker remained after commit: %v", err)
	}
	for _, path := range []string{previous.PIDPath, previous.LauncherPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("commit removed previous service metadata %s: %v", path, err)
		}
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatalf("second commit must be idempotent: %v", err)
	}
}

func TestInspectPreviousStateRejectsSourceSymlinkAncestor(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	root := filepath.Dir(paths.StateDir)
	actualParent := filepath.Join(root, "actual-previous-parent")
	linkedParent := filepath.Join(root, "linked-previous-parent")
	if err := os.MkdirAll(actualParent, 0o700); err != nil {
		t.Fatal(err)
	}
	actualState := filepath.Join(actualParent, filepath.Base(previous.StateDir))
	if err := os.Rename(previous.StateDir, actualState); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actualParent, linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rebasePreviousState(&previous, filepath.Join(linkedParent, filepath.Base(actualState)))

	if _, _, err := InspectPreviousState(paths, previous); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("expected source ancestor symlink rejection, got %v", err)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("source changed during rejected inspection: %q err=%v", got, err)
	}
}

func TestInspectPreviousStateRejectsTargetSymlinkAncestor(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	root := filepath.Dir(paths.StateDir)
	actualParent := filepath.Join(root, "actual-current-parent")
	linkedParent := filepath.Join(root, "linked-current-parent")
	if err := os.MkdirAll(actualParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actualParent, linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rebaseCurrentState(&paths, filepath.Join(linkedParent, "current"))

	if _, _, err := InspectPreviousState(paths, previous); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("expected target ancestor symlink rejection, got %v", err)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("source changed during rejected inspection: %q err=%v", got, err)
	}
}

func TestBeginPreviousStateMigrationRejectsSourceReplacedAfterInspection(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	displaced := previous.Database + ".inspected"
	if err := os.Rename(previous.Database, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.Database, []byte("replacement-database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := BeginPreviousStateMigration(plan); err == nil {
		t.Fatal("expected source identity replacement rejection")
	}
	for path, want := range map[string]string{
		previous.Database: "replacement-database",
		displaced:         "previous-database",
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("rejected migration changed %s: %q err=%v", path, got, readErr)
		}
	}
	if _, err := os.Lstat(paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected migration created target: %v", err)
	}
}

func TestBeginPreviousStateMigrationRejectsTargetAncestorReplacedAfterInspection(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(paths.StateDir), "outside-current")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, paths.StateDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := BeginPreviousStateMigration(plan); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("expected late target ancestor symlink rejection, got %v", err)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("rejected migration changed source: %q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, filepath.Base(paths.Database))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected migration wrote through target symlink: %v", err)
	}
}

func TestInspectPreviousStateTreatsOrphanTargetDatabaseSidecarAsConflict(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	if err := os.MkdirAll(filepath.Dir(paths.Database), 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := paths.Database + "-wal"
	if err := os.WriteFile(orphan, []byte("orphan-target-wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, result, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || !result.DatabaseConflict {
		t.Fatalf("orphan target sidecar was not a conflict: %+v", result)
	}
	if got, err := os.ReadFile(orphan); err != nil || string(got) != "orphan-target-wal" {
		t.Fatalf("inspection changed orphan target: %q err=%v", got, err)
	}
}

func TestMigrationTransactionRejectsCommitAfterRollback(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err == nil {
		t.Fatal("expected commit after rollback rejection")
	}
	if got, err := os.ReadFile(previous.MarkerPath); err != nil || strings.TrimSpace(string(got)) != previous.MarkerValue {
		t.Fatalf("commit after rollback changed marker: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(previous.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("commit after rollback changed source: %q err=%v", got, err)
	}
}

func TestMigrationTransactionRejectsRollbackAfterCommitStartsAndCommitCanRetry(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.MarkerPath, []byte("changed-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err == nil {
		t.Fatal("expected commit cleanup failure")
	}
	if err := transaction.Rollback(); err == nil {
		t.Fatal("expected rollback rejection after commit started")
	}
	if got, err := os.ReadFile(paths.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("rollback after commit start moved data: %q err=%v", got, err)
	}
	if err := os.WriteFile(previous.MarkerPath, []byte(previous.MarkerValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatalf("commit cleanup retry failed: %v", err)
	}
}

func TestMigrationTransactionPartialCommitCleanupRejectsRollbackAndRetries(t *testing.T) {
	paths, previous := migrationTransactionFixture(t)
	plan, _, err := InspectPreviousState(paths, previous)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginPreviousStateMigration(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{previous.PIDPath, previous.LauncherPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	cleanupCalls := 0
	originalRemoveStateDir := removePreviousStateDirForMigration
	removePreviousStateDirForMigration = func(string) (bool, error) {
		cleanupCalls++
		return false, errors.New("injected state directory cleanup failure")
	}
	t.Cleanup(func() { removePreviousStateDirForMigration = originalRemoveStateDir })

	if _, err := transaction.Commit(); err == nil {
		t.Fatal("expected partial commit cleanup failure")
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d want 1", cleanupCalls)
	}
	if _, err := os.Lstat(previous.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial commit did not remove marker first: %v", err)
	}
	if err := transaction.Rollback(); err == nil {
		t.Fatal("expected rollback rejection after partial cleanup")
	}
	if got, err := os.ReadFile(paths.Database); err != nil || string(got) != "previous-database" {
		t.Fatalf("rollback after partial cleanup moved data: %q err=%v", got, err)
	}

	removePreviousStateDirForMigration = originalRemoveStateDir
	result, err := transaction.Commit()
	if err != nil {
		t.Fatalf("partial commit cleanup retry failed: %v", err)
	}
	if !result.PreviousStateGone {
		t.Fatalf("retry did not finish previous state cleanup: %+v", result)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatalf("committed retry must be idempotent: %v", err)
	}
}

func rebasePreviousState(previous *PreviousPaths, stateDir string) {
	previous.StateDir = stateDir
	previous.ConfigPath = filepath.Join(stateDir, "config.json")
	previous.Database = filepath.Join(stateDir, previousDatabaseName())
	previous.BackupDir = filepath.Join(stateDir, "backups")
	previous.PIDPath = filepath.Join(stateDir, filepath.Base(previous.PIDPath))
	previous.LauncherPath = filepath.Join(stateDir, filepath.Base(previous.LauncherPath))
	previous.MarkerPath = filepath.Join(stateDir, filepath.Base(previous.MarkerPath))
}

func rebaseCurrentState(paths *Paths, stateDir string) {
	paths.StateDir = stateDir
	paths.ConfigPath = filepath.Join(stateDir, "config.json")
	paths.Database = filepath.Join(stateDir, databaseName)
	paths.BackupDir = filepath.Join(stateDir, "backups")
}

func migrationTransactionFixture(t *testing.T) (Paths, PreviousPaths) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "current")
	previousState := filepath.Join(root, "previous")
	paths := Paths{
		StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json"),
		Database: filepath.Join(stateDir, databaseName), BackupDir: filepath.Join(stateDir, "backups"),
	}
	previous := PreviousPaths{
		ProductName: "previous", StateDir: previousState,
		ConfigPath: filepath.Join(previousState, "config.json"), Database: filepath.Join(previousState, previousDatabaseName()),
		BackupDir: filepath.Join(previousState, "backups"), PIDPath: filepath.Join(previousState, "previous.pid"),
		LauncherPath: filepath.Join(previousState, "previous-start.vbs"), MarkerPath: filepath.Join(previousState, ".previous-state"),
		MarkerValue: "previous-state-v1",
	}
	if err := os.MkdirAll(previousState, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		previous.MarkerPath:   previous.MarkerValue + "\n",
		previous.Database:     "previous-database",
		previous.ConfigPath:   "previous-config",
		previous.PIDPath:      "123\n",
		previous.LauncherPath: "launcher",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths, previous
}

func TestPricingOverridesRoundTripWithoutChangingOtherSettings(t *testing.T) {
	stateDir := t.TempDir()
	paths := Paths{StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json")}
	cfg := Config{
		ListenAddress: "127.0.0.1", Port: 45678, ScanIntervalSeconds: 900,
		ExtraCodexHomes: []string{filepath.Join(stateDir, "codex-home")},
		PricingOverrides: map[string]pricing.Override{
			"CODEX-AUTO-REVIEW": {AliasOf: "GPT-5.6-LUNA"},
		},
	}
	if err := Save(paths, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 45678 || loaded.ScanIntervalSeconds != 900 || loaded.ListenAddress != "127.0.0.1" || len(loaded.ExtraCodexHomes) != 1 {
		t.Fatalf("unrelated settings changed: %#v", loaded)
	}
	if got := loaded.PricingOverrides["codex-auto-review"].AliasOf; got != "gpt-5.6-luna" {
		t.Fatalf("unexpected pricing override %q", got)
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["port"] != float64(45678) || persisted["scan_interval_seconds"] != float64(900) {
		t.Fatalf("persisted settings were overwritten: %#v", persisted)
	}
}

func TestFallbackScanIntervalDefaultsAndNormalizesToTenMinutes(t *testing.T) {
	if got := Default().ScanIntervalSeconds; got != 600 {
		t.Fatalf("default fallback interval=%d want 600", got)
	}
	stateDir := t.TempDir()
	paths := Paths{StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json")}
	if err := os.WriteFile(paths.ConfigPath, []byte(`{"listen_address":"127.0.0.1","port":43189,"scan_interval_seconds":60}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ScanIntervalSeconds != 600 {
		t.Fatalf("legacy fallback interval=%d want 600", loaded.ScanIntervalSeconds)
	}
}

func TestLoadRejectsInvalidPricingOverride(t *testing.T) {
	stateDir := t.TempDir()
	paths := Paths{StateDir: stateDir, ConfigPath: filepath.Join(stateDir, "config.json")}
	data := `{
		"listen_address":"127.0.0.1",
		"port":43189,
		"scan_interval_seconds":60,
		"pricing_overrides":{"internal":{"input_usd_per_million":"-1","cached_input_usd_per_million":"0.1","cache_write_input_usd_per_million":"1","output_usd_per_million":"1"}}
	}`
	if err := os.WriteFile(paths.ConfigPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(paths); err == nil {
		t.Fatal("expected invalid pricing override to be rejected")
	}
}
