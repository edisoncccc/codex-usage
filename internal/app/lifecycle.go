package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/usage"
)

type installScanOutcome struct {
	Result   usage.ScanResult
	Warnings []string
}

type lifecycleRequest struct {
	CandidatePath     string
	DestinationPath   string
	InstallRecordPath string
	StateDir          string
	ServiceURL        string
	Candidate         buildIdentity
	Source            string
	SkipScan          bool
	Migration         config.MigrationPlan
	PreviousService   platform.PreviousService
}

type lifecycleResult struct {
	Decision        install.Decision
	CandidateSHA256 string
	Service         platform.ServiceResult
	Scan            usage.ScanResult
	ScanWarnings    []string
	DataPreserved   bool
}

type lifecycleMarkerTransaction interface {
	Prepare() error
	Validate() error
	Commit() error
	Rollback() error
}

type lifecycleOps struct {
	StopService      func(executable, stateDir string) error
	InstallService   func(executable, stateDir string) (platform.ServiceResult, error)
	UninstallService func(executable, stateDir string) error
	SuspendPrevious  func(platform.PreviousService) error
	ResumePrevious   func(platform.PreviousService) error
	RemovePrevious   func(platform.PreviousService) error
	ProbeIdentity    func(context.Context, string, buildIdentity) error
	Scan             func(context.Context, usage.ProgressObserver) (installScanOutcome, error)
	Now              func() time.Time
	Marker           lifecycleMarkerTransaction
}

type lifecycleProgress func(phase string, progress any)

var syncLifecycleParent = platform.SyncParent

type lifecycleFailure struct {
	primary  error
	rollback []error
}

func (e *lifecycleFailure) Error() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, len(e.rollback))
	for _, detail := range e.rollback {
		parts = append(parts, detail.Error())
	}
	return fmt.Sprintf("%v; rollback: %s", e.primary, strings.Join(parts, "; "))
}

func (e *lifecycleFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.primary
}

type lifecycleRollbackState struct {
	request              lifecycleRequest
	ops                  lifecycleOps
	oldRecord            *install.Record
	stagePath            string
	backupPath           string
	stageOwned           bool
	backupCreated        bool
	activated            bool
	newServiceAttempted  bool
	recordMayHaveChanged bool
	currentStopAttempted bool
	previousSuspendTried bool
	migration            *config.MigrationTransaction
	marker               lifecycleMarkerTransaction
	syncParent           func(string) error
}

func executeLifecycle(
	ctx context.Context,
	request lifecycleRequest,
	ops lifecycleOps,
	report lifecycleProgress,
) (lifecycleResult, error) {
	result := lifecycleResult{}
	if ctx == nil {
		return result, errors.New("lifecycle context is nil")
	}
	ops = withLifecycleDefaults(ops)
	syncParent := syncLifecycleParent
	installedAt := ops.Now().UTC()
	if err := validateLifecycleRequest(request, installedAt); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	decision, err := install.Assess(request.InstallRecordPath, request.DestinationPath, install.Candidate{
		Version: request.Candidate.Version, ExecutablePath: request.CandidatePath,
	})
	if err != nil {
		return result, err
	}
	result.Decision = decision
	if decision == install.DecisionDowngrade {
		return result, errors.New("downgrade is not allowed without an explicit trusted workflow")
	}
	if decision == install.DecisionUntrusted {
		return result, errors.New("untrusted existing install cannot be replaced")
	}

	oldRecord, err := install.Load(request.InstallRecordPath)
	if err != nil {
		return result, err
	}
	observedCandidateSHA, err := install.FileSHA256(request.CandidatePath)
	if err != nil {
		return result, err
	}
	result.CandidateSHA256 = observedCandidateSHA
	marker := ops.Marker
	if marker == nil {
		marker = newInstallMarkerPersistence(config.Paths{StateDir: request.StateDir})
	}
	if err := marker.Prepare(); err != nil {
		return result, &installConfigPersistenceError{err: fmt.Errorf("prepare install state marker: %w", err)}
	}
	state := &lifecycleRollbackState{
		request: request, ops: ops, oldRecord: oldRecord,
		marker: marker, syncParent: syncParent,
	}
	if err := ctx.Err(); err != nil {
		return result, rollbackLifecycle(err, state)
	}
	if decision == install.DecisionSame {
		return executeSameLifecycle(ctx, request, ops, report, result, marker)
	}

	stagePath := request.DestinationPath + ".stage"
	backupPath := request.DestinationPath + ".backup"
	state.stagePath = stagePath
	state.backupPath = backupPath
	if err := rejectExistingRecoveryPoint(stagePath, "stage"); err != nil {
		return result, rollbackLifecycle(err, state)
	}
	if err := rejectExistingRecoveryPoint(backupPath, "backup"); err != nil {
		return result, rollbackLifecycle(err, state)
	}
	stagedSHA, err := stageLifecycleCandidate(request.CandidatePath, stagePath, syncParent)
	if err != nil {
		return result, rollbackLifecycle(err, state)
	}
	state.stageOwned = true
	if stagedSHA != observedCandidateSHA {
		return result, rollbackLifecycle(errors.New("candidate changed while staging"), state)
	}

	if decision == install.DecisionUpgrade {
		reportLifecycle(report, "stop_service", nil)
		state.currentStopAttempted = true
		if err := ops.StopService(request.DestinationPath, request.StateDir); err != nil {
			return result, rollbackLifecycle(fmt.Errorf("stop current service: %w", err), state)
		}
	}
	if hasPreviousService(request.PreviousService) {
		state.previousSuspendTried = true
		if err := ops.SuspendPrevious(request.PreviousService); err != nil {
			return result, rollbackLifecycle(fmt.Errorf("suspend previous service: %w", err), state)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, rollbackLifecycle(err, state)
	}

	transaction, err := config.BeginPreviousStateMigration(request.Migration)
	if err != nil {
		return result, rollbackLifecycle(fmt.Errorf("begin previous state migration: %w", err), state)
	}
	state.migration = transaction

	if decision == install.DecisionUpgrade {
		if err := platform.RenameNoReplace(request.DestinationPath, backupPath); err != nil {
			return result, rollbackLifecycle(fmt.Errorf("backup installed executable: %w", err), state)
		}
		state.backupCreated = true
		if err := syncLifecycleRename(request.DestinationPath, backupPath, syncParent); err != nil {
			return result, rollbackLifecycle(fmt.Errorf("sync executable backup: %w", err), state)
		}
	} else if _, err := os.Lstat(request.DestinationPath); err == nil {
		return result, rollbackLifecycle(fmt.Errorf("destination appeared after preflight: %s", request.DestinationPath), state)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, rollbackLifecycle(fmt.Errorf("inspect install destination: %w", err), state)
	}

	reportLifecycle(report, "install", nil)
	if err := platform.RenameNoReplace(stagePath, request.DestinationPath); err != nil {
		return result, rollbackLifecycle(fmt.Errorf("activate staged candidate: %w", err), state)
	}
	state.stageOwned = false
	state.activated = true
	if err := syncLifecycleRename(stagePath, request.DestinationPath, syncParent); err != nil {
		return result, rollbackLifecycle(fmt.Errorf("sync activated candidate: %w", err), state)
	}
	activeSHA, err := install.FileSHA256(request.DestinationPath)
	if err != nil {
		return result, rollbackLifecycle(err, state)
	}
	if activeSHA != stagedSHA {
		return result, rollbackLifecycle(errors.New("active executable digest differs from staged candidate"), state)
	}
	result.CandidateSHA256 = activeSHA

	if !request.SkipScan {
		reportLifecycle(report, "scan", nil)
		observer := usage.ProgressObserver(nil)
		if report != nil {
			observer = func(progress usage.ScanProgress) { report("scan", progress) }
		}
		scan, scanErr := ops.Scan(ctx, observer)
		if scanErr != nil {
			return result, rollbackLifecycle(fmt.Errorf("post-activate scan: %w", scanErr), state)
		}
		result.Scan = scan.Result
		result.ScanWarnings = append(result.ScanWarnings, scan.Warnings...)
	}
	if err := ctx.Err(); err != nil {
		return result, rollbackLifecycle(err, state)
	}

	reportLifecycle(report, "start_service", nil)
	state.newServiceAttempted = true
	service, err := ops.InstallService(request.DestinationPath, request.StateDir)
	if err != nil {
		return result, rollbackLifecycle(fmt.Errorf("install service: %w", err), state)
	}
	result.Service = service

	reportLifecycle(report, "health_check", nil)
	if err := ops.ProbeIdentity(ctx, request.ServiceURL, request.Candidate); err != nil {
		return result, rollbackLifecycle(fmt.Errorf("identity health: %w", err), state)
	}
	if err := ctx.Err(); err != nil {
		return result, rollbackLifecycle(err, state)
	}

	finalActiveSHA, err := install.FileSHA256(request.DestinationPath)
	if err != nil {
		return result, rollbackLifecycle(err, state)
	}
	if finalActiveSHA != stagedSHA {
		return result, rollbackLifecycle(errors.New("active executable changed before record commit"), state)
	}
	result.CandidateSHA256 = finalActiveSHA
	if err := marker.Validate(); err != nil {
		return result, rollbackLifecycle(
			&installConfigPersistenceError{err: fmt.Errorf("validate install state marker: %w", err)}, state,
		)
	}
	record := recordForLifecycle(request, finalActiveSHA, installedAt)
	state.recordMayHaveChanged = true
	if err := install.Save(request.InstallRecordPath, record); err != nil {
		return result, rollbackLifecycle(fmt.Errorf("save install record: %w", err), state)
	}

	markerCommitted := false
	migrationResult, commitErr := transaction.CommitPreparedStateMarker(func() error {
		if err := marker.Commit(); err != nil {
			return err
		}
		markerCommitted = true
		return nil
	})
	result.DataPreserved = decision == install.DecisionUpgrade || migrationResult.Found
	if !markerCommitted {
		if commitErr == nil {
			commitErr = errors.New("migration transaction did not commit the prepared install state marker")
		}
		return result, rollbackLifecycle(
			&installConfigPersistenceError{err: fmt.Errorf("commit install state marker: %w", commitErr)}, state,
		)
	}
	if commitErr != nil {
		result.ScanWarnings = append(result.ScanWarnings, "cleanup:migration_commit: "+commitErr.Error())
	}
	if hasPreviousService(request.PreviousService) {
		if err := ops.RemovePrevious(request.PreviousService); err != nil {
			result.ScanWarnings = append(result.ScanWarnings, "cleanup:previous_service: "+err.Error())
		}
	}
	if state.backupCreated {
		if err := removeOwnedRegularFile(backupPath, syncParent); err != nil {
			result.ScanWarnings = append(result.ScanWarnings, "cleanup:backup: "+err.Error())
		}
	}
	return result, nil
}

func executeSameLifecycle(
	ctx context.Context,
	request lifecycleRequest,
	ops lifecycleOps,
	report lifecycleProgress,
	result lifecycleResult,
	marker lifecycleMarkerTransaction,
) (lifecycleResult, error) {
	if err := ctx.Err(); err != nil {
		return result, rollbackSameLifecycle(err, request, ops, marker, false)
	}
	reportLifecycle(report, "start_service", nil)
	service, err := ops.InstallService(request.DestinationPath, request.StateDir)
	if err != nil {
		return result, rollbackSameLifecycle(fmt.Errorf("repair service: %w", err), request, ops, marker, false)
	}
	result.Service = service
	reportLifecycle(report, "health_check", nil)
	if err := ops.ProbeIdentity(ctx, request.ServiceURL, request.Candidate); err != nil {
		return result, rollbackSameLifecycle(fmt.Errorf("identity health: %w", err), request, ops, marker, true)
	}
	if err := ctx.Err(); err != nil {
		return result, rollbackSameLifecycle(err, request, ops, marker, true)
	}
	if err := marker.Commit(); err != nil {
		return result, rollbackSameLifecycle(
			&installConfigPersistenceError{err: fmt.Errorf("commit install state marker: %w", err)},
			request, ops, marker, true,
		)
	}
	result.DataPreserved = true
	return result, nil
}

func rollbackSameLifecycle(
	primary error,
	request lifecycleRequest,
	ops lifecycleOps,
	marker lifecycleMarkerTransaction,
	restoreService bool,
) error {
	var rollbackErrors []error
	if restoreService {
		if err := ops.UninstallService(request.DestinationPath, request.StateDir); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback uninstall service: %w", err))
		}
		if _, err := ops.InstallService(request.DestinationPath, request.StateDir); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore service: %w", err))
		}
	}
	if err := marker.Rollback(); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback install state marker: %w", err))
	}
	return joinLifecycleRollback(primary, rollbackErrors)
}

func withLifecycleDefaults(ops lifecycleOps) lifecycleOps {
	if ops.StopService == nil {
		ops.StopService = platform.StopService
	}
	if ops.InstallService == nil {
		ops.InstallService = platform.InstallService
	}
	if ops.UninstallService == nil {
		ops.UninstallService = platform.UninstallService
	}
	if ops.SuspendPrevious == nil {
		ops.SuspendPrevious = platform.SuspendPreviousService
	}
	if ops.ResumePrevious == nil {
		ops.ResumePrevious = platform.ResumePreviousService
	}
	if ops.RemovePrevious == nil {
		ops.RemovePrevious = platform.RemovePreviousService
	}
	if ops.ProbeIdentity == nil {
		ops.ProbeIdentity = probeIdentity
	}
	if ops.Scan == nil {
		ops.Scan = func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
			return installScanOutcome{}, nil
		}
	}
	if ops.Now == nil {
		ops.Now = time.Now
	}
	return ops
}

func validateLifecycleRequest(request lifecycleRequest, installedAt time.Time) error {
	for name, path := range map[string]string{
		"candidate":      request.CandidatePath,
		"destination":    request.DestinationPath,
		"install record": request.InstallRecordPath,
		"state":          request.StateDir,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%s path must be absolute and clean: %q", name, path)
		}
	}
	if !pathInsideLifecycleRoot(request.StateDir, request.InstallRecordPath) {
		return fmt.Errorf("install record path must remain inside state directory")
	}
	if installedAt.IsZero() {
		return errors.New("install clock returned zero time")
	}
	if !isStableVersion(request.Candidate.Version) {
		return fmt.Errorf("invalid candidate version %q", request.Candidate.Version)
	}
	if request.Candidate.OS != runtime.GOOS || request.Candidate.Arch != runtime.GOARCH {
		return fmt.Errorf("candidate platform %s/%s does not match host %s/%s",
			request.Candidate.OS, request.Candidate.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if err := validateProbeIdentityShape(request.Candidate); err != nil {
		return err
	}
	switch request.Source {
	case install.SourceBuild:
		if request.Candidate.Commit == "dev" && !request.Candidate.Dirty {
			return errors.New("source build commit dev must be dirty")
		}
		if request.Candidate.Commit != "dev" && !fullLowerCommit(request.Candidate.Commit) {
			return fmt.Errorf("source build commit must be dev or a full lowercase sha")
		}
		if request.Candidate.BuildDate != "unknown" {
			if value, err := time.Parse(time.RFC3339, request.Candidate.BuildDate); err != nil || value.IsZero() {
				return fmt.Errorf("invalid source build date %q", request.Candidate.BuildDate)
			}
		}
	case install.SourceTrustedRelease:
		if err := validateTrustedReleaseIdentity(request.Candidate); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported install source %q", request.Source)
	}
	return nil
}

func fullLowerCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	nonZero := false
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
		if character != '0' {
			nonZero = true
		}
	}
	return nonZero
}

func pathInsideLifecycleRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func rejectExistingRecoveryPoint(path, kind string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("existing %s recovery point requires manual inspection: %s", kind, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s recovery point: %w", kind, err)
	}
	return nil
}

func stageLifecycleCandidate(source, stagePath string, syncParent func(string) error) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("candidate is not a safe regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o700); err != nil {
		return "", fmt.Errorf("create install directory: %w", err)
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", fmt.Errorf("candidate changed while opening: %s", source)
	}
	output, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", fmt.Errorf("create stage file: %w", err)
	}
	owned := true
	defer func() {
		_ = output.Close()
		if owned {
			if err := os.Remove(stagePath); err == nil {
				_ = syncParent(stagePath)
			}
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return "", fmt.Errorf("copy candidate to stage: %w", err)
	}
	if err := output.Chmod(0o700); err != nil {
		return "", fmt.Errorf("protect staged candidate: %w", err)
	}
	if err := output.Sync(); err != nil {
		return "", fmt.Errorf("sync staged candidate: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", fmt.Errorf("close staged candidate: %w", err)
	}
	if err := syncParent(stagePath); err != nil {
		return "", fmt.Errorf("sync staged candidate directory: %w", err)
	}
	digest, err := install.FileSHA256(stagePath)
	if err != nil {
		return "", err
	}
	owned = false
	return digest, nil
}

func rollbackLifecycle(primary error, state *lifecycleRollbackState) error {
	if state == nil {
		return primary
	}
	var rollbackErrors []error
	if state.activated || state.newServiceAttempted {
		if err := state.ops.UninstallService(state.request.DestinationPath, state.request.StateDir); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback uninstall service: %w", err))
		}
	}
	if state.activated {
		if err := platform.RenameNoReplace(state.request.DestinationPath, state.stagePath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback move candidate aside: %w", err))
		} else {
			state.stageOwned = true
			state.activated = false
			if err := syncLifecycleRename(state.request.DestinationPath, state.stagePath, state.syncParent); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback sync candidate move: %w", err))
			}
		}
	}
	if state.backupCreated {
		if err := platform.RenameNoReplace(state.backupPath, state.request.DestinationPath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore executable: %w", err))
		} else {
			state.backupCreated = false
			if err := syncLifecycleRename(state.backupPath, state.request.DestinationPath, state.syncParent); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback sync restored executable: %w", err))
			}
		}
	}
	if state.recordMayHaveChanged {
		if err := restoreLifecycleRecord(state.request.InstallRecordPath, state.oldRecord, state.syncParent); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore install record: %w", err))
		}
	}
	if state.migration != nil {
		if err := state.migration.Rollback(); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback previous state migration: %w", err))
		}
	}
	if state.currentStopAttempted {
		if _, err := state.ops.InstallService(state.request.DestinationPath, state.request.StateDir); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore current service: %w", err))
		}
	}
	if state.previousSuspendTried {
		if err := state.ops.ResumePrevious(state.request.PreviousService); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback resume previous service: %w", err))
		}
	}
	if state.stageOwned {
		if err := removeOwnedRegularFile(state.stagePath, state.syncParent); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback remove staged candidate: %w", err))
		}
	}
	if state.marker != nil {
		if err := state.marker.Rollback(); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback install state marker: %w", err))
		}
	}
	return joinLifecycleRollback(primary, rollbackErrors)
}

func joinLifecycleRollback(primary error, rollbackErrors []error) error {
	if len(rollbackErrors) == 0 {
		return primary
	}
	return &lifecycleFailure{primary: primary, rollback: rollbackErrors}
}

func restoreLifecycleRecord(path string, oldRecord *install.Record, syncParent func(string) error) error {
	if oldRecord != nil {
		return install.Save(path, *oldRecord)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to remove unknown install record path: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncParent(path)
}

func removeOwnedRegularFile(path string, syncParent func(string) error) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("owned cleanup target is no longer a regular file: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncParent(path)
}

func syncLifecycleRename(source, target string, syncParent func(string) error) error {
	if err := syncParent(target); err != nil {
		return err
	}
	if !sameLifecycleDirectory(filepath.Dir(source), filepath.Dir(target)) {
		if err := syncParent(source); err != nil {
			return err
		}
	}
	return nil
}

func sameLifecycleDirectory(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func recordForLifecycle(request lifecycleRequest, digest string, installedAt time.Time) install.Record {
	return install.Record{
		SchemaVersion:    install.RecordSchemaVersion,
		Product:          install.ProductName,
		Version:          request.Candidate.Version,
		Commit:           request.Candidate.Commit,
		Dirty:            request.Candidate.Dirty,
		BuildDate:        request.Candidate.BuildDate,
		OS:               request.Candidate.OS,
		Arch:             request.Candidate.Arch,
		ExecutablePath:   request.DestinationPath,
		ExecutableSHA256: digest,
		Source:           request.Source,
		InstalledAt:      installedAt.Format(time.RFC3339),
	}
}

func hasPreviousService(previous platform.PreviousService) bool {
	return previous.StateDir != "" || previous.Executable != "" || previous.PIDPath != "" ||
		previous.LauncherPath != "" || previous.StartupEntry != "" || previous.ServiceName != ""
}

func reportLifecycle(report lifecycleProgress, phase string, progress any) {
	if report != nil {
		report(phase, progress)
	}
}
