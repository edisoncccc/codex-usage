package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/usage"
)

var lifecycleTestNow = time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)

type lifecycleFixture struct {
	t                *testing.T
	root             string
	candidatePath    string
	destinationPath  string
	installRecord    string
	stateDir         string
	candidateContent string
	identity         buildIdentity
	calls            []string
}

func newLifecycleFixture(t *testing.T, version, content string) *lifecycleFixture {
	t.Helper()
	root := t.TempDir()
	candidateDir := filepath.Join(root, "download")
	if err := os.MkdirAll(candidateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(candidateDir, "codex-usage-candidate")
	if err := os.WriteFile(candidatePath, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	return &lifecycleFixture{
		t:                t,
		root:             root,
		candidatePath:    candidatePath,
		destinationPath:  filepath.Join(root, "program", "codex-usage"),
		installRecord:    filepath.Join(stateDir, "install.json"),
		stateDir:         stateDir,
		candidateContent: content,
		identity: buildIdentity{
			Version: version, Commit: "dev", Dirty: true, BuildDate: "unknown",
			OS: runtime.GOOS, Arch: runtime.GOARCH,
		},
	}
}

func (f *lifecycleFixture) request() lifecycleRequest {
	return lifecycleRequest{
		CandidatePath:     f.candidatePath,
		DestinationPath:   f.destinationPath,
		InstallRecordPath: f.installRecord,
		StateDir:          f.stateDir,
		ServiceURL:        "http://127.0.0.1:43189",
		Candidate:         f.identity,
		Source:            install.SourceBuild,
	}
}

func (f *lifecycleFixture) defaultOps() lifecycleOps {
	return lifecycleOps{
		StopService: func(string, string) error {
			f.calls = append(f.calls, "stop")
			return nil
		},
		InstallService: func(string, string) (platform.ServiceResult, error) {
			f.calls = append(f.calls, "install")
			return platform.ServiceResult{Installed: true, Started: true, Mode: "persistent"}, nil
		},
		UninstallService: func(string, string) error {
			f.calls = append(f.calls, "uninstall")
			return nil
		},
		SuspendPrevious: func(platform.PreviousService) error {
			f.calls = append(f.calls, "suspend")
			return nil
		},
		ResumePrevious: func(platform.PreviousService) error {
			f.calls = append(f.calls, "resume")
			return nil
		},
		RemovePrevious: func(platform.PreviousService) error {
			f.calls = append(f.calls, "remove_previous")
			return nil
		},
		ProbeIdentity: func(context.Context, string, buildIdentity) error {
			f.calls = append(f.calls, "health")
			return nil
		},
		Scan: func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
			f.calls = append(f.calls, "scan")
			return installScanOutcome{Result: usage.ScanResult{Files: 2, EventsInserted: 3}}, nil
		},
		Now: func() time.Time { return lifecycleTestNow },
	}
}

func (f *lifecycleFixture) writeExisting(version, content string) install.Record {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.destinationPath), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.destinationPath, []byte(content), 0o700); err != nil {
		f.t.Fatal(err)
	}
	digest, err := install.FileSHA256(f.destinationPath)
	if err != nil {
		f.t.Fatal(err)
	}
	record := install.Record{
		SchemaVersion:    install.RecordSchemaVersion,
		Product:          install.ProductName,
		Version:          version,
		Commit:           "dev",
		Dirty:            true,
		BuildDate:        "unknown",
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		ExecutablePath:   f.destinationPath,
		ExecutableSHA256: digest,
		Source:           install.SourceBuild,
		InstalledAt:      lifecycleTestNow.Add(-time.Hour).Format(time.RFC3339),
	}
	if err := install.Save(f.installRecord, record); err != nil {
		f.t.Fatal(err)
	}
	return record
}

func (f *lifecycleFixture) addPreviousState() (config.MigrationPlan, config.Paths, config.PreviousPaths, platform.PreviousService) {
	f.t.Helper()
	current := config.Paths{
		StateDir:      f.stateDir,
		ConfigPath:    filepath.Join(f.stateDir, "config.json"),
		InstallRecord: f.installRecord,
		Database:      filepath.Join(f.stateDir, "usage.sqlite"),
		BackupDir:     filepath.Join(f.stateDir, "backups"),
		InstallDir:    filepath.Dir(f.destinationPath),
		InstalledEXE:  f.destinationPath,
	}
	previousState := filepath.Join(f.root, "previous-state")
	previousInstall := filepath.Join(f.root, "previous-program")
	previous := config.PreviousPaths{
		ProductName:  "previous-product",
		StateDir:     previousState,
		ConfigPath:   filepath.Join(previousState, "config.json"),
		Database:     filepath.Join(previousState, "previous.sqlite"),
		BackupDir:    filepath.Join(previousState, "backups"),
		InstallDir:   previousInstall,
		InstalledEXE: filepath.Join(previousInstall, "previous-product"),
		PIDPath:      filepath.Join(previousState, "previous.pid"),
		LauncherPath: filepath.Join(previousState, "previous-start.vbs"),
		MarkerPath:   filepath.Join(previousState, ".previous-state"),
		MarkerValue:  "previous-state-v1",
		StartupEntry: "PreviousProduct",
		ServiceName:  "previous-product.service",
	}
	for _, dir := range []string{previous.BackupDir, previous.InstallDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		previous.MarkerPath:                             previous.MarkerValue + "\n",
		previous.ConfigPath:                             "previous-config",
		previous.Database:                               "previous-database",
		previous.Database + "-wal":                      "previous-wal",
		filepath.Join(previous.BackupDir, "backup.txt"): "previous-backup",
		filepath.Join(previous.StateDir, "daemon.log"):  "previous-log",
		previous.PIDPath:                                "123\n",
		previous.LauncherPath:                           "previous-launcher",
		previous.InstalledEXE:                           "previous-executable",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	plan, result, err := config.InspectPreviousState(current, previous)
	if err != nil {
		f.t.Fatal(err)
	}
	if !result.Found || result.DatabaseConflict {
		f.t.Fatalf("unexpected previous-state inspection: %+v", result)
	}
	service := platform.PreviousService{
		StateDir: previous.StateDir, Executable: previous.InstalledEXE, InstallDir: previous.InstallDir,
		PIDPath: previous.PIDPath, LauncherPath: previous.LauncherPath,
		StartupEntry: previous.StartupEntry, ServiceName: previous.ServiceName,
	}
	return plan, current, previous, service
}

func TestLifecycleFreshInstallCommitsAfterIdentityHealth(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "fresh-candidate")
	ops := fixture.defaultOps()
	ops.ProbeIdentity = func(_ context.Context, _ string, expected buildIdentity) error {
		fixture.calls = append(fixture.calls, "health")
		if expected != fixture.identity {
			t.Fatalf("unexpected expected identity: %+v", expected)
		}
		record, err := install.Load(fixture.installRecord)
		if err != nil {
			t.Fatal(err)
		}
		if record != nil {
			t.Fatalf("record committed before identity health: %+v", record)
		}
		return nil
	}

	result, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != install.DecisionFresh || result.Service.Mode != "persistent" {
		t.Fatalf("unexpected lifecycle result: %+v", result)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"scan", "install", "health"})
	assertFileContent(t, fixture.destinationPath, fixture.candidateContent)
	record := mustLoadInstallRecord(t, fixture.installRecord)
	if record.Version != fixture.identity.Version || record.InstalledAt != lifecycleTestNow.Format(time.RFC3339) {
		t.Fatalf("unexpected committed record: %+v", record)
	}
	activeDigest, err := install.FileSHA256(fixture.destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateSHA256 != activeDigest || record.ExecutableSHA256 != activeDigest {
		t.Fatalf("digest did not come from active bytes: result=%q record=%q active=%q", result.CandidateSHA256, record.ExecutableSHA256, activeDigest)
	}
}

func TestLifecycleSameBinaryRepairsServiceWithoutReplacing(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "same-bytes")
	oldRecord := fixture.writeExisting("2.3.6", "same-bytes")
	ops := fixture.defaultOps()

	result, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != install.DecisionSame {
		t.Fatalf("decision=%q want same", result.Decision)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"install", "health"})
	assertFileContent(t, fixture.destinationPath, "same-bytes")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("same install changed record:\ngot  %+v\nwant %+v", *record, oldRecord)
	}
}

func TestLifecycleSameBinaryHardServiceErrorDoesNotBlindlyUninstall(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "same-bytes")
	oldRecord := fixture.writeExisting("2.3.6", "same-bytes")
	serviceErr := errors.New("transactional service repair failed")
	ops := fixture.defaultOps()
	ops.InstallService = func(string, string) (platform.ServiceResult, error) {
		fixture.calls = append(fixture.calls, "install")
		return platform.ServiceResult{}, serviceErr
	}
	ops.UninstallService = func(string, string) error {
		fixture.calls = append(fixture.calls, "uninstall")
		return nil
	}

	_, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if !errors.Is(err, serviceErr) {
		t.Fatalf("repair error=%v want %v", err, serviceErr)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"install"})
	assertFileContent(t, fixture.destinationPath, "same-bytes")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("same-binary hard service error changed record: %+v", record)
	}
}

func TestLifecycleUpgradeUsesStrictOrder(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	fixture.writeExisting("2.3.5", "old-binary")
	plan, current, previous, previousService := fixture.addPreviousState()
	request := fixture.request()
	request.Migration = plan
	request.PreviousService = previousService
	ops := fixture.defaultOps()
	ops.StopService = func(string, string) error {
		fixture.calls = append(fixture.calls, "stop")
		assertFileContent(t, fixture.destinationPath, "old-binary")
		assertFileContent(t, previous.Database, "previous-database")
		return nil
	}
	ops.Scan = func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
		fixture.calls = append(fixture.calls, "scan")
		assertFileContent(t, fixture.destinationPath, "new-binary")
		assertFileContent(t, current.Database, "previous-database")
		if record := mustLoadInstallRecord(t, fixture.installRecord); record.Version != "2.3.5" {
			t.Fatalf("record committed before scan: %+v", record)
		}
		return installScanOutcome{Result: usage.ScanResult{EventsInserted: 4}, Warnings: []string{"recoverable"}}, nil
	}
	ops.ProbeIdentity = func(context.Context, string, buildIdentity) error {
		fixture.calls = append(fixture.calls, "health")
		if record := mustLoadInstallRecord(t, fixture.installRecord); record.Version != "2.3.5" {
			t.Fatalf("record committed before health: %+v", record)
		}
		return nil
	}
	ops.RemovePrevious = func(platform.PreviousService) error {
		fixture.calls = append(fixture.calls, "remove_previous")
		if record := mustLoadInstallRecord(t, fixture.installRecord); record.Version != "2.3.6" {
			t.Fatalf("previous cleanup ran before record commit: %+v", record)
		}
		if _, err := os.Stat(previous.MarkerPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration was not committed before previous cleanup: %v", err)
		}
		return nil
	}

	result, err := executeLifecycle(context.Background(), request, ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"stop", "suspend", "scan", "install", "health", "remove_previous"})
	if result.Decision != install.DecisionUpgrade || !result.DataPreserved || !reflect.DeepEqual(result.ScanWarnings, []string{"recoverable"}) {
		t.Fatalf("unexpected upgrade result: %+v", result)
	}
	assertFileContent(t, fixture.destinationPath, "new-binary")
	assertFileContent(t, current.Database+"-wal", "previous-wal")
	if matches, err := filepath.Glob(fixture.destinationPath + ".backup*"); err != nil || len(matches) != 0 {
		t.Fatalf("backup remained after commit: %v err=%v", matches, err)
	}
}

func TestLifecycleCopyFailureLeavesOldInstallUntouched(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	unknownStage := fixture.destinationPath + ".stage"
	if err := os.WriteFile(unknownStage, []byte("unknown-recovery-point"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := executeLifecycle(context.Background(), fixture.request(), fixture.defaultOps(), nil)
	if err == nil || result.Decision != install.DecisionUpgrade {
		t.Fatalf("expected upgrade staging rejection, result=%+v err=%v", result, err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stage") {
		t.Fatalf("staging error did not identify recovery point: %v", err)
	}
	assertLifecycleCalls(t, fixture.calls, nil)
	assertFileContent(t, fixture.destinationPath, "old-binary")
	assertFileContent(t, unknownStage, "unknown-recovery-point")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after stage failure: %+v", record)
	}
}

func TestLifecycleStageDirectorySyncFailureLeavesOldInstallUntouched(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	syncErr := errors.New("stage parent sync failed")
	originalSync := syncLifecycleParent
	syncLifecycleParent = func(path string) error {
		if path == fixture.destinationPath+".stage" {
			return syncErr
		}
		return nil
	}
	t.Cleanup(func() { syncLifecycleParent = originalSync })

	_, err := executeLifecycle(context.Background(), fixture.request(), fixture.defaultOps(), nil)
	if !errors.Is(err, syncErr) {
		t.Fatalf("stage sync error=%v want %v", err, syncErr)
	}
	assertLifecycleCalls(t, fixture.calls, nil)
	assertFileContent(t, fixture.destinationPath, "old-binary")
	if _, statErr := os.Lstat(fixture.destinationPath + ".stage"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stage remained after sync failure: %v", statErr)
	}
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after stage sync failure: %+v", record)
	}
}

func TestLifecycleActivationDirectorySyncFailureRollsBackOldExecutable(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	syncErr := errors.New("activation parent sync failed")
	activationFailed := false
	originalSync := syncLifecycleParent
	syncLifecycleParent = func(path string) error {
		if path == fixture.destinationPath && !activationFailed {
			if got, readErr := os.ReadFile(path); readErr == nil && string(got) == "new-binary" {
				activationFailed = true
				return syncErr
			}
		}
		return nil
	}
	t.Cleanup(func() { syncLifecycleParent = originalSync })
	ops := fixture.defaultOps()
	ops.InstallService = func(executable, _ string) (platform.ServiceResult, error) {
		fixture.calls = append(fixture.calls, "install:"+readFileContent(t, executable))
		return platform.ServiceResult{Installed: true, Started: true, Mode: platform.ServiceModePersistent}, nil
	}

	_, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if !errors.Is(err, syncErr) {
		t.Fatalf("activation sync error=%v want %v", err, syncErr)
	}
	if !activationFailed {
		t.Fatal("sync failure was not injected after activation")
	}
	assertLifecycleCalls(t, fixture.calls, []string{"stop", "uninstall", "install:old-binary"})
	assertFileContent(t, fixture.destinationPath, "old-binary")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after activation sync rollback: %+v", record)
	}
}

func TestLifecycleFatalPostActivateScanRestoresOldInstall(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	scanErr := errors.New("state database unavailable")
	ops := fixture.defaultOps()
	ops.Scan = func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
		fixture.calls = append(fixture.calls, "scan")
		assertFileContent(t, fixture.destinationPath, "new-binary")
		return installScanOutcome{}, scanErr
	}
	ops.InstallService = func(executable, _ string) (platform.ServiceResult, error) {
		fixture.calls = append(fixture.calls, "install:"+readFileContent(t, executable))
		return platform.ServiceResult{Installed: true, Started: true, Mode: "persistent"}, nil
	}

	_, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if !errors.Is(err, scanErr) {
		t.Fatalf("primary scan error was not preserved: %v", err)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"stop", "scan", "uninstall", "install:old-binary"})
	assertFileContent(t, fixture.destinationPath, "old-binary")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after scan rollback: %+v", record)
	}
}

func TestLifecycleMigrationFailureRestoresPreviousStateAndService(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	plan, current, previous, previousService := fixture.addPreviousState()
	request := fixture.request()
	request.Migration = plan
	request.PreviousService = previousService
	ops := fixture.defaultOps()
	ops.SuspendPrevious = func(platform.PreviousService) error {
		fixture.calls = append(fixture.calls, "suspend")
		if err := os.MkdirAll(filepath.Dir(current.ConfigPath), 0o700); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(current.ConfigPath, []byte("late-conflict"), 0o600)
	}

	_, err := executeLifecycle(context.Background(), request, ops, nil)
	if err == nil {
		t.Fatal("expected reversible migration to reject the late target conflict")
	}
	assertLifecycleCalls(t, fixture.calls, []string{"suspend", "resume"})
	assertFileContent(t, previous.Database, "previous-database")
	assertFileContent(t, previous.Database+"-wal", "previous-wal")
	assertFileContent(t, previous.ConfigPath, "previous-config")
	assertFileContent(t, current.ConfigPath, "late-conflict")
	if _, statErr := os.Stat(fixture.destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("candidate was activated after migration failure: %v", statErr)
	}
	if record, loadErr := install.Load(fixture.installRecord); loadErr != nil || record != nil {
		t.Fatalf("fresh migration failure left install record: %+v err=%v", record, loadErr)
	}
}

func TestLifecycleHealthFailureRollsBackPreviousStateMigration(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	plan, current, previous, previousService := fixture.addPreviousState()
	request := fixture.request()
	request.Migration = plan
	request.PreviousService = previousService
	healthErr := errors.New("health endpoint unavailable")
	ops := fixture.defaultOps()
	ops.ProbeIdentity = func(context.Context, string, buildIdentity) error {
		fixture.calls = append(fixture.calls, "health")
		return healthErr
	}

	_, err := executeLifecycle(context.Background(), request, ops, nil)
	if !errors.Is(err, healthErr) {
		t.Fatalf("primary health error was not preserved: %v", err)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"suspend", "scan", "install", "health", "uninstall", "resume"})
	assertFileContent(t, previous.Database, "previous-database")
	assertFileContent(t, previous.ConfigPath, "previous-config")
	if _, statErr := os.Stat(current.Database); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("migrated database remained after health rollback: %v", statErr)
	}
	if _, statErr := os.Stat(fixture.destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fresh candidate remained after health rollback: %v", statErr)
	}
	if record, loadErr := install.Load(fixture.installRecord); loadErr != nil || record != nil {
		t.Fatalf("fresh health failure left install record: %+v err=%v", record, loadErr)
	}
}

func TestLifecycleServiceFailureRestoresOldExecutableAndRecord(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	serviceErr := errors.New("new service failed")
	ops := fixture.defaultOps()
	ops.InstallService = func(executable, _ string) (platform.ServiceResult, error) {
		content := readFileContent(t, executable)
		fixture.calls = append(fixture.calls, "install:"+content)
		if content == "new-binary" {
			return platform.ServiceResult{}, serviceErr
		}
		return platform.ServiceResult{Installed: true, Started: true, Mode: "persistent"}, nil
	}

	_, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if !errors.Is(err, serviceErr) {
		t.Fatalf("primary service error was not preserved: %v", err)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"stop", "scan", "install:new-binary", "uninstall", "install:old-binary"})
	assertFileContent(t, fixture.destinationPath, "old-binary")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after service rollback: %+v", record)
	}
}

func TestLifecycleHealthMismatchRestoresOldService(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "new-binary")
	oldRecord := fixture.writeExisting("2.3.5", "old-binary")
	healthErr := errors.New("health_identity_mismatch")
	rollbackErr := errors.New("candidate service cleanup failed")
	ops := fixture.defaultOps()
	ops.InstallService = func(executable, _ string) (platform.ServiceResult, error) {
		fixture.calls = append(fixture.calls, "install:"+readFileContent(t, executable))
		return platform.ServiceResult{Installed: true, Started: true, Mode: "persistent"}, nil
	}
	ops.ProbeIdentity = func(context.Context, string, buildIdentity) error {
		fixture.calls = append(fixture.calls, "health")
		return healthErr
	}
	ops.UninstallService = func(string, string) error {
		fixture.calls = append(fixture.calls, "uninstall")
		return rollbackErr
	}

	_, err := executeLifecycle(context.Background(), fixture.request(), ops, nil)
	if !errors.Is(err, healthErr) {
		t.Fatalf("primary health mismatch was not preserved: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback") || !strings.Contains(err.Error(), rollbackErr.Error()) {
		t.Fatalf("rollback detail missing from error: %v", err)
	}
	assertLifecycleCalls(t, fixture.calls, []string{"stop", "scan", "install:new-binary", "health", "uninstall", "install:old-binary"})
	assertFileContent(t, fixture.destinationPath, "old-binary")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after identity rollback: %+v", record)
	}
}

func TestLifecycleRejectsDowngradeBeforeStoppingService(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.5", "older-candidate")
	oldRecord := fixture.writeExisting("2.3.6", "installed-newer")

	result, err := executeLifecycle(context.Background(), fixture.request(), fixture.defaultOps(), nil)
	if err == nil || result.Decision != install.DecisionDowngrade || !strings.Contains(strings.ToLower(err.Error()), "downgrade") {
		t.Fatalf("expected downgrade rejection, result=%+v err=%v", result, err)
	}
	assertLifecycleCalls(t, fixture.calls, nil)
	assertFileContent(t, fixture.destinationPath, "installed-newer")
	if record := mustLoadInstallRecord(t, fixture.installRecord); !reflect.DeepEqual(*record, oldRecord) {
		t.Fatalf("record changed after downgrade rejection: %+v", record)
	}
}

func TestLifecycleRejectsUntrustedExistingInstall(t *testing.T) {
	fixture := newLifecycleFixture(t, "2.3.6", "trusted-candidate")
	if err := os.MkdirAll(filepath.Dir(fixture.destinationPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.destinationPath, []byte("foreign-binary"), 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := executeLifecycle(context.Background(), fixture.request(), fixture.defaultOps(), nil)
	if err == nil || result.Decision != install.DecisionUntrusted || !strings.Contains(strings.ToLower(err.Error()), "untrusted") {
		t.Fatalf("expected untrusted rejection, result=%+v err=%v", result, err)
	}
	assertLifecycleCalls(t, fixture.calls, nil)
	assertFileContent(t, fixture.destinationPath, "foreign-binary")
}

func assertLifecycleCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle calls:\ngot  %v\nwant %v", got, want)
	}
}

func mustLoadInstallRecord(t *testing.T, path string) *install.Record {
	t.Helper()
	record, err := install.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatalf("missing install record %s", path)
	}
	return record
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	if got := readFileContent(t, path); got != want {
		t.Fatalf("%s content=%q want %q", path, got, want)
	}
}

func readFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
