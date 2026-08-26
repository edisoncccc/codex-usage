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
	if !result.Found || !result.DatabaseMoved || !result.ConfigMoved || !result.BackupsMoved || !result.PreviousStateGone {
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
	if _, err := os.Stat(previousState); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous state directory still exists: %v", err)
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
