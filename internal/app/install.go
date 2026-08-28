package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/pricing"
	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
)

const (
	installConfigTemporaryPattern       = ".codex-usage-install-config-new-*"
	installConfigAtomicTemporaryPattern = ".codex-usage-install-config-atomic-*"
	installMarkerTemporaryPattern       = ".codex-usage-install-marker-new-*"
	installStateMarkerName              = ".codex-usage-state"
	installStateMarkerContent           = "codex-usage-state-v1\n"
)

type verificationResult struct {
	Name   string             `json:"name"`
	Status verificationStatus `json:"status"`
	Detail string             `json:"detail,omitempty"`
}

type installReceipt struct {
	Identity         buildIdentity        `json:"identity"`
	InstallPath      string               `json:"install_path"`
	StatePath        string               `json:"state_path"`
	DatabasePath     string               `json:"database_path"`
	ServiceMode      string               `json:"service_mode"`
	DashboardURL     string               `json:"dashboard_url"`
	Scan             usage.ScanResult     `json:"scan"`
	ScanWarnings     []string             `json:"scan_warnings,omitempty"`
	DataPreserved    bool                 `json:"data_preserved"`
	Verifications    []verificationResult `json:"verifications"`
	UninstallCommand string               `json:"uninstall_command"`
	PurgeCommand     string               `json:"purge_command"`
}

type installCommandDeps struct {
	ResolvePaths    func() (config.Paths, error)
	Executable      func() (string, error)
	PreflightPort   func(context.Context, string, *install.Record) error
	InspectPrevious func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error)
	RunLifecycle    func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error)
}

type installConfirmation struct {
	Repository             string   `json:"repository"`
	Source                 string   `json:"source"`
	InstallPath            string   `json:"install_path"`
	StatePath              string   `json:"state_path"`
	DatabasePath           string   `json:"database_path"`
	Service                string   `json:"service"`
	DashboardURL           string   `json:"dashboard_url"`
	ScanScope              []string `json:"scan_scope"`
	DataPreservedByDefault bool     `json:"data_preserved_by_default"`
}

type installPreflightFailure struct {
	code string
	err  error
}

func (e *installPreflightFailure) Error() string { return e.err.Error() }
func (e *installPreflightFailure) Unwrap() error { return e.err }

type installConfigPreviewFailure struct{ err error }

func (e *installConfigPreviewFailure) Error() string { return e.err.Error() }
func (e *installConfigPreviewFailure) Unwrap() error { return e.err }

type installConfigSource uint8

const (
	installConfigSourceDefault installConfigSource = iota
	installConfigSourceCurrent
	installConfigSourcePrevious
)

type installConfigPreview struct {
	cfg          config.Config
	homes        []string
	written      []byte
	changed      bool
	source       installConfigSource
	current      installConfigSnapshot
	previous     installConfigSnapshot
	previousPath string
}

type installStatePreview struct {
	migration       config.MigrationPlan
	migrationResult config.MigrationResult
	previousService platform.PreviousService
	config          installConfigPreview
}

func (c CLI) installCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	yes := flags.Bool("yes", false, c.tr("flag.yes"))
	skipScan := flags.Bool("skip-scan", false, c.tr("flag.skipScan"))
	if err := flags.Parse(args); err != nil {
		return commandResult{}, invalidInstallArguments(c, err)
	}
	if flags.NArg() != 0 {
		return commandResult{}, invalidInstallArguments(c, errors.New("unexpected positional arguments"))
	}

	deps := c.resolvedInstallDependencies()
	paths, err := deps.ResolvePaths()
	if err != nil {
		return commandResult{}, installCommandFailure(err, "internal_error")
	}
	paths, err = absoluteInstallPaths(paths)
	if err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	explicitHome, err := resolvedExplicitCodexHome()
	if err != nil {
		return commandResult{}, installCommandFailure(err, "config_invalid")
	}
	initial, err := inspectInstallStatePreview(deps, paths, explicitHome)
	if err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	if initial.migrationResult.DatabaseConflict {
		return commandResult{}, installCommandFailure(errors.New(c.tr("install.dbConflict")), "existing_install_untrusted")
	}
	confirmation := newInstallConfirmation(paths, initial.config.cfg, initial.config.homes)
	if emitter.enabled && !*yes {
		return commandResult{}, &codedError{
			Code: "confirmation_required", ExitCode: 2,
			Err: errors.New(c.tr("install.confirm.required")), Details: confirmation,
		}
	}

	if !emitter.enabled {
		if err := writeHumanInstallConfirmation(c, confirmation, !*yes); err != nil {
			return commandResult{}, installCommandFailure(err, "internal_error")
		}
		if !*yes {
			confirmed, confirmErr := readInstallConfirmation(c.Stdin)
			if confirmErr != nil {
				return commandResult{}, installCommandFailure(confirmErr, "confirmation_required")
			}
			if !confirmed {
				return commandResult{}, &codedError{
					Code: "confirmation_required", ExitCode: 1,
					Err: errors.New(c.tr("install.confirm.declined")), Details: confirmation,
				}
			}
		}
	}

	if !emitter.enabled {
		fmt.Fprintln(c.Stdout, c.tr("install.phase.preflight"))
	}
	for _, directory := range []string{paths.StateDir, paths.InstallDir} {
		if err := probeInstallDirectoryWritable(directory); err != nil {
			return commandResult{}, installCommandFailure(err, "permission_required")
		}
	}
	if emitter.enabled {
		if err := emitter.Progress("preflight", "running", "install_preflight", "", confirmation); err != nil {
			return commandResult{}, err
		}
	}

	candidatePath, err := deps.Executable()
	if err != nil {
		return commandResult{}, installCommandFailure(err, "source_build_blocked")
	}
	candidatePath, err = canonicalAbsolutePath(candidatePath)
	if err != nil {
		return commandResult{}, installCommandFailure(err, "source_build_blocked")
	}
	identity, err := currentBuildIdentity()
	if err != nil {
		return commandResult{}, err
	}
	candidateDigest, err := install.FileSHA256(candidatePath)
	if err != nil {
		return commandResult{}, installCommandFailure(err, "source_build_blocked")
	}
	decision, err := install.Assess(paths.InstallRecord, paths.InstalledEXE, install.Candidate{
		Version: identity.Version, ExecutablePath: candidatePath,
	})
	if err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	if err := rejectUnsafeInstallDecision(decision); err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	record, err := install.Load(paths.InstallRecord)
	if err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	confirmed, err := inspectInstallStatePreview(deps, paths, explicitHome)
	if err != nil {
		var previewFailure *installConfigPreviewFailure
		if errors.As(err, &previewFailure) {
			return commandResult{}, &codedError{
				Code: "preflight_changed", ExitCode: 1,
				Err: fmt.Errorf("install config changed after confirmation: %w", err),
			}
		}
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	if confirmed.migrationResult.DatabaseConflict {
		return commandResult{}, installCommandFailure(errors.New(c.tr("install.dbConflict")), "existing_install_untrusted")
	}
	if !sameInstallStatePreview(initial, confirmed) {
		return commandResult{}, &codedError{
			Code: "preflight_changed", ExitCode: 1,
			Err: errors.New("install inputs changed after confirmation"),
		}
	}
	migration := confirmed.migration
	migrationResult := confirmed.migrationResult
	previousService := confirmed.previousService
	homes := append([]string(nil), confirmed.config.homes...)
	serviceURL := fmt.Sprintf("http://127.0.0.1:%d", confirmed.config.cfg.Port)
	if err := deps.PreflightPort(context.Background(), serviceURL, record); err != nil {
		return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
	}
	configPersistence := newInstallConfigPersistence(paths, explicitHome, writeInstallConfig)
	configPersistence.Expect(confirmed.config)
	prepared, err := configPersistence.PrepareMigratedConfig(previousService, confirmed.config)
	if err != nil {
		return commandResult{}, installCommandFailure(&installConfigPersistenceError{err: err}, "config_write_failed")
	}
	if prepared {
		migration, migrationResult, previousService, err = deps.InspectPrevious(paths)
		if err != nil {
			err = errors.Join(err, configPersistence.Restore())
			return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
		}
		if migrationResult.DatabaseConflict {
			err = errors.Join(errors.New(c.tr("install.dbConflict")), configPersistence.Restore())
			return commandResult{}, installCommandFailure(err, "existing_install_untrusted")
		}
	}
	request := lifecycleRequest{
		CandidatePath: candidatePath, DestinationPath: paths.InstalledEXE,
		InstallRecordPath: paths.InstallRecord, StateDir: paths.StateDir,
		ServiceURL: serviceURL, Candidate: identity, Source: install.SourceBuild,
		SkipScan: *skipScan, Migration: migration, PreviousService: previousService,
	}
	ctx, cancel := context.WithCancel(context.Background())
	tracker := startInstallProgressTracker(
		installProgressSnapshot{phase: "preflight", status: "running", code: "install_preflight", progress: confirmation},
		c.HeartbeatInterval,
		func(snapshot installProgressSnapshot) error { return c.emitInstallProgress(emitter, snapshot) },
		cancel,
	)
	operations := withInstallStopProgress(
		withLifecycleDefaults(c.installLifecycleOperations(paths, homes)),
		hasPreviousService(previousService),
		tracker.Report,
	)
	operations = configPersistence.Wrap(operations)
	result, lifecycleErr := deps.RunLifecycle(ctx, request, operations, tracker.Report)
	if lifecycleErr != nil {
		if restoreErr := configPersistence.Restore(); restoreErr != nil {
			lifecycleErr = errors.Join(lifecycleErr, fmt.Errorf("restore install config: %w", restoreErr))
		}
		cancel()
		_ = tracker.Stop()
		return commandResult{}, installCommandFailure(lifecycleErr, "install_failed")
	}
	configPersistence.Commit()
	result = c.cleanupLegacyManagedOTel(homes, result, !emitter.enabled)
	receipt := newInstallReceipt(identity, paths, serviceURL, candidateDigest, result)
	if err := tracker.Advance("complete", nil); err != nil {
		cancel()
		_ = tracker.Stop()
		return commandResult{}, installCommandFailure(err, "internal_error")
	}
	cancel()
	if stopErr := tracker.Stop(); stopErr != nil {
		return commandResult{}, installCommandFailure(stopErr, "internal_error")
	}
	return commandResult{Code: "install_complete", Data: receipt}, nil
}

func (c CLI) cleanupLegacyManagedOTel(homes []string, result lifecycleResult, human bool) lifecycleResult {
	for _, home := range homes {
		changed, err := config.RemoveLegacyManagedOTel(home)
		if err != nil {
			warning := fmt.Sprintf("legacy_otel_cleanup_failed: %s: %v", home, err)
			result.ScanWarnings = append(result.ScanWarnings, warning)
			if human {
				_, _ = fmt.Fprintln(c.Stderr, c.tr("install.warning"), warning)
			}
			continue
		}
		if changed && human {
			_, _ = fmt.Fprintln(c.Stdout, c.tr("install.legacyRemoved", home))
		}
	}
	return result
}

func newInstallReceipt(
	identity buildIdentity,
	paths config.Paths,
	serviceURL string,
	candidateDigest string,
	result lifecycleResult,
) installReceipt {
	copyVerification := verificationResult{
		Name: "candidate_copy_sha256", Status: verificationNotChecked,
		Detail: "installed copy digest was not verified",
	}
	installedDigest, err := install.FileSHA256(paths.InstalledEXE)
	if err == nil && candidateDigest != "" && candidateDigest == result.CandidateSHA256 && installedDigest == candidateDigest {
		copyVerification.Status = verificationVerified
		copyVerification.Detail = candidateDigest
	} else if err != nil {
		copyVerification.Detail = err.Error()
	} else {
		copyVerification.Detail = "candidate, lifecycle, and installed copy digests differ"
	}
	return installReceipt{
		Identity: identity, InstallPath: paths.InstalledEXE,
		StatePath: paths.StateDir, DatabasePath: paths.Database,
		ServiceMode: string(result.Service.Mode), DashboardURL: serviceURL,
		Scan: result.Scan, ScanWarnings: append([]string(nil), result.ScanWarnings...),
		DataPreserved: result.DataPreserved,
		Verifications: []verificationResult{
			{Name: "release_immutable", Status: verificationNotApplicable, Detail: install.SourceBuild},
			{Name: "artifact_attestation", Status: verificationNotApplicable, Detail: install.SourceBuild},
			{Name: "authenticode", Status: verificationNotApplicable, Detail: install.SourceBuild},
			copyVerification,
		},
		UninstallCommand: "codex-usage uninstall --yes --json",
		PurgeCommand:     "codex-usage uninstall --purge --yes --json",
	}
}

type installProgressSnapshot struct {
	phase    string
	status   string
	code     string
	progress any
}

type installProgressStatus struct {
	status string
}

type installProgressTracker struct {
	mu       sync.Mutex
	latest   installProgressSnapshot
	index    int
	emit     func(installProgressSnapshot) error
	cancel   context.CancelFunc
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	err      error
}

var installPhases = []string{
	"preflight", "stop_service", "install", "scan", "start_service", "health_check", "complete",
}

func startInstallProgressTracker(
	initial installProgressSnapshot,
	interval time.Duration,
	emit func(installProgressSnapshot) error,
	cancel context.CancelFunc,
) *installProgressTracker {
	if interval <= 0 {
		interval = 4 * time.Second
	}
	tracker := &installProgressTracker{
		latest: initial, emit: emit, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go tracker.run(interval)
	return tracker
}

func (t *installProgressTracker) Report(phase string, progress any) {
	if transition, ok := progress.(installProgressStatus); ok {
		_ = t.setPhaseStatus(phase, transition.status)
		return
	}
	_ = t.Advance(phase, progress)
}

func (t *installProgressTracker) Advance(phase string, progress any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	target := -1
	for index, known := range installPhases {
		if phase == known {
			target = index
			break
		}
	}
	if target < 0 {
		t.failLocked(fmt.Errorf("unknown install lifecycle phase %q", phase))
		return t.err
	}
	if target < t.index {
		t.failLocked(fmt.Errorf("install lifecycle phase moved backwards from %q to %q", installPhases[t.index], phase))
		return t.err
	}
	if target == t.index {
		if phase == "stop_service" && progress == nil &&
			(t.latest.status == "running" || t.latest.status == "completed" || t.latest.status == "failed") {
			return nil
		}
		t.latest = installProgressSnapshot{phase: phase, status: "running", code: "install_" + phase, progress: progress}
		if err := t.emit(t.latest); err != nil {
			t.failLocked(err)
		}
		return t.err
	}
	for next := t.index + 1; next <= target; next++ {
		status := "skipped"
		code := "install_" + installPhases[next] + "_skipped"
		value := any(nil)
		if next == target {
			status = "running"
			code = "install_" + phase
			value = progress
		}
		t.latest = installProgressSnapshot{phase: installPhases[next], status: status, code: code, progress: value}
		t.index = next
		if err := t.emit(t.latest); err != nil {
			t.failLocked(err)
			return t.err
		}
	}
	return nil
}

func (t *installProgressTracker) setPhaseStatus(phase, status string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	if phase != "stop_service" {
		t.failLocked(fmt.Errorf("explicit install phase status is unsupported for %q", phase))
		return t.err
	}
	if status != "running" && status != "completed" && status != "failed" {
		t.failLocked(fmt.Errorf("invalid install phase status %q", status))
		return t.err
	}
	target := 1
	if t.index > target {
		return nil
	}
	if t.index < target {
		t.index = target
	}
	if t.latest.phase == phase {
		if t.latest.status == status {
			return nil
		}
		if t.latest.status == "completed" || t.latest.status == "failed" {
			return nil
		}
	}
	code := "install_" + phase
	if status != "running" {
		code += "_" + status
	}
	t.latest = installProgressSnapshot{phase: phase, status: status, code: code}
	if err := t.emit(t.latest); err != nil {
		t.failLocked(err)
	}
	return t.err
}

func (t *installProgressTracker) failLocked(err error) {
	if t.err == nil {
		t.err = err
		if t.cancel != nil {
			t.cancel()
		}
	}
}

func (t *installProgressTracker) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(t.done)
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.mu.Lock()
			if t.err == nil {
				if err := t.emit(t.latest); err != nil {
					t.failLocked(err)
				}
			}
			t.mu.Unlock()
		}
	}
}

func (t *installProgressTracker) Stop() error {
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (c CLI) emitInstallProgress(emitter *eventEmitter, snapshot installProgressSnapshot) error {
	if emitter.enabled {
		return emitter.Progress(snapshot.phase, snapshot.status, snapshot.code, "", snapshot.progress)
	}
	if snapshot.status == "completed" || snapshot.status == "failed" {
		return nil
	}
	if snapshot.phase == "scan" {
		if progress, ok := snapshot.progress.(usage.ScanProgress); ok {
			_, err := fmt.Fprintf(c.Stdout, c.tr("install.progress.scan"),
				progress.FilesDiscovered, progress.FilesProcessed,
				progress.EventsInserted, progress.Warnings)
			return err
		}
	}
	_, err := fmt.Fprintln(c.Stdout, c.tr("install.phase."+snapshot.phase))
	return err
}

type installStopProgress struct {
	mu       sync.Mutex
	report   lifecycleProgress
	started  bool
	finished bool
}

func withInstallStopProgress(ops lifecycleOps, hasPrevious bool, report lifecycleProgress) lifecycleOps {
	progress := &installStopProgress{report: report}
	stopService := ops.StopService
	ops.StopService = func(executable, stateDir string) error {
		progress.begin()
		err := stopService(executable, stateDir)
		if err != nil {
			progress.fail()
			return err
		}
		if !hasPrevious {
			progress.complete()
		}
		return nil
	}
	suspendPrevious := ops.SuspendPrevious
	ops.SuspendPrevious = func(previous platform.PreviousService) error {
		progress.begin()
		err := suspendPrevious(previous)
		if err != nil {
			progress.fail()
			return err
		}
		progress.complete()
		return nil
	}
	return ops
}

func (p *installStopProgress) begin() {
	p.mu.Lock()
	if p.started || p.finished {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	p.emit("running")
}

func (p *installStopProgress) complete() {
	p.finish("completed")
}

func (p *installStopProgress) fail() {
	p.finish("failed")
}

func (p *installStopProgress) finish(status string) {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return
	}
	p.finished = true
	p.mu.Unlock()
	p.emit(status)
}

func (p *installStopProgress) emit(status string) {
	if p.report != nil {
		p.report("stop_service", installProgressStatus{status: status})
	}
}

func (c CLI) installLifecycleOperations(paths config.Paths, homes []string) lifecycleOps {
	scanHomes := append([]string(nil), homes...)
	return lifecycleOps{
		ProbeIdentity: waitForInstallIdentity,
		Scan: func(ctx context.Context, observer usage.ProgressObserver) (installScanOutcome, error) {
			if err := ensureInstallStateMarker(paths); err != nil {
				return installScanOutcome{}, err
			}
			if err := platform.LockDown(paths.StateDir); err != nil {
				return installScanOutcome{}, &platform.PermissionError{Operation: "protect state directory", Err: err}
			}
			stateStore, err := store.Open(paths.Database)
			if err != nil {
				return installScanOutcome{}, err
			}
			scanner := &usage.Scanner{Store: stateStore}
			result, scanErr := scanner.ScanWithProgress(ctx, scanHomes, false, observer)
			closeErr := stateStore.Close()
			if scanErr != nil {
				return installScanOutcome{}, scanErr
			}
			if closeErr != nil {
				return installScanOutcome{}, fmt.Errorf("close scan database: %w", closeErr)
			}
			outcome := installScanOutcome{Result: result}
			if result.Warnings > 0 {
				outcome.Warnings = []string{fmt.Sprintf("scan completed with %d warning(s)", result.Warnings)}
			}
			return outcome, nil
		},
		Now: c.Now,
	}
}

type installConfigPersistenceError struct {
	err error
}

func (e *installConfigPersistenceError) Error() string { return e.err.Error() }
func (e *installConfigPersistenceError) Unwrap() error { return e.err }

type installMarkerPersistence struct {
	mu         sync.Mutex
	paths      config.Paths
	create     func(string, []byte) (os.FileInfo, error)
	attempted  bool
	created    bool
	finished   bool
	marker     *os.File
	markerInfo os.FileInfo
	prepareErr error
}

func newInstallMarkerPersistence(paths config.Paths) *installMarkerPersistence {
	return &installMarkerPersistence{paths: paths, create: createInstallStateMarker}
}

func (p *installMarkerPersistence) Prepare() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempted {
		return p.prepareErr
	}
	p.attempted = true

	markerPath, err := p.safeMarkerPath()
	if err != nil {
		p.prepareErr = err
		return err
	}
	marker, markerInfo, exists, err := openExactInstallStateMarker(markerPath)
	if err != nil {
		p.prepareErr = err
		return err
	}
	if exists {
		p.marker = marker
		p.markerInfo = markerInfo
		return nil
	}

	creationInfo, createErr := p.create(markerPath, []byte(installStateMarkerContent))
	marker, markerInfo, exists, inspectErr := openExactInstallStateMarker(markerPath)
	if createErr != nil {
		if inspectErr == nil && exists && creationInfo != nil && os.SameFile(creationInfo, markerInfo) {
			p.created = true
			p.marker = marker
			p.markerInfo = markerInfo
			cleanupPath, cleanupErr := p.safeMarkerPath()
			if cleanupErr != nil {
				err = errors.Join(createErr, cleanupErr,
					errors.New("install state marker path changed after creation; rollback refused"), p.closeMarkerLocked())
			} else {
				err = errors.Join(createErr, p.rollbackLocked(cleanupPath))
			}
			p.finished = true
		} else {
			if marker != nil {
				_ = marker.Close()
			}
			err = errors.Join(createErr, inspectErr)
			if creationInfo != nil || exists {
				err = errors.Join(err, errors.New("install state marker changed during creation; rollback refused"))
			}
		}
		p.prepareErr = err
		return err
	}
	if inspectErr != nil {
		p.prepareErr = inspectErr
		return inspectErr
	}
	if !exists || creationInfo == nil || !os.SameFile(creationInfo, markerInfo) {
		if marker != nil {
			_ = marker.Close()
		}
		err = errors.New("created install state marker does not retain the created file identity and exact content; rollback refused")
		p.prepareErr = err
		return err
	}
	p.created = true
	p.marker = marker
	p.markerInfo = markerInfo
	return nil
}

func (p *installMarkerPersistence) Rollback() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return nil
	}
	p.finished = true
	if !p.created {
		return p.closeMarkerLocked()
	}
	markerPath, err := p.safeMarkerPath()
	if err != nil {
		return errors.Join(err, errors.New("install state marker path changed after creation; rollback refused"), p.closeMarkerLocked())
	}
	return p.rollbackLocked(markerPath)
}

func (p *installMarkerPersistence) rollbackLocked(markerPath string) error {
	if err := p.verifyLocked(markerPath); err != nil {
		return errors.Join(err, errors.New("install state marker changed after creation; rollback refused"), p.closeMarkerLocked())
	}
	if runtime.GOOS == "windows" {
		expected := p.markerInfo
		if err := p.closeMarkerLocked(); err != nil {
			return err
		}
		marker, markerInfo, exists, err := openExactInstallStateMarker(markerPath)
		if err != nil || !exists || !os.SameFile(expected, markerInfo) {
			if marker != nil {
				err = errors.Join(err, marker.Close())
			}
			return errors.Join(err, errors.New("install state marker changed after releasing rollback handle; rollback refused"))
		}
		closeErr := marker.Close()
		after, statErr := os.Lstat(markerPath)
		if err := errors.Join(closeErr, statErr); err != nil ||
			after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(markerInfo, after) {
			return errors.Join(err, errors.New("install state marker changed after releasing rollback handle; rollback refused"))
		}
	}
	if err := os.Remove(markerPath); err != nil {
		return errors.Join(err, p.closeMarkerLocked())
	}
	return errors.Join(p.closeMarkerLocked(), platform.SyncParent(markerPath))
}

func (p *installMarkerPersistence) Validate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateLocked()
}

func (p *installMarkerPersistence) Commit() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return nil
	}
	if err := p.validateLocked(); err != nil {
		return err
	}
	if err := p.closeMarkerLocked(); err != nil {
		return err
	}
	p.finished = true
	return nil
}

func (p *installMarkerPersistence) validateLocked() error {
	if p.finished {
		return errors.New("install state marker transaction is already closed")
	}
	if !p.attempted || p.prepareErr != nil || p.marker == nil {
		return errors.New("install state marker transaction was not prepared")
	}
	markerPath, err := p.safeMarkerPath()
	if err != nil {
		return err
	}
	if err := p.verifyLocked(markerPath); err != nil {
		return err
	}
	return nil
}

func (p *installMarkerPersistence) verifyLocked(markerPath string) error {
	if p.marker == nil || p.markerInfo == nil {
		return errors.New("install state marker handle is unavailable")
	}
	current, err := os.Lstat(markerPath)
	if err != nil {
		return fmt.Errorf("install state marker changed before commit: %w", err)
	}
	opened, err := p.marker.Stat()
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(p.markerInfo, opened) || !os.SameFile(opened, current) {
		return errors.New("install state marker identity changed before commit")
	}
	if _, err := p.marker.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(p.marker, int64(len(installStateMarkerContent)+1)))
	if err != nil {
		return err
	}
	after, err := os.Lstat(markerPath)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !bytes.Equal(data, []byte(installStateMarkerContent)) {
		return errors.New("install state marker identity or content changed before commit")
	}
	return nil
}

func (p *installMarkerPersistence) closeMarkerLocked() error {
	if p.marker == nil {
		return nil
	}
	err := p.marker.Close()
	p.marker = nil
	return err
}

func openExactInstallStateMarker(path string) (*os.File, os.FileInfo, bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("install state marker is not a safe regular file: %s", path)
	}
	marker, err := os.Open(path)
	if err != nil {
		return nil, nil, false, err
	}
	opened, statErr := marker.Stat()
	data, readErr := io.ReadAll(io.LimitReader(marker, int64(len(installStateMarkerContent)+1)))
	after, afterErr := os.Lstat(path)
	if err := errors.Join(statErr, readErr, afterErr); err != nil {
		_ = marker.Close()
		return nil, nil, false, err
	}
	if !opened.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = marker.Close()
		return nil, nil, false, fmt.Errorf("install state marker changed during safe open: %s", path)
	}
	if !bytes.Equal(data, []byte(installStateMarkerContent)) {
		_ = marker.Close()
		return nil, nil, false, fmt.Errorf("install state marker has unexpected content: %s", path)
	}
	return marker, opened, true, nil
}

func (p *installMarkerPersistence) safeMarkerPath() (string, error) {
	if err := probeInstallDirectoryWritable(p.paths.StateDir); err != nil {
		return "", err
	}
	markerPath, err := canonicalAbsolutePath(filepath.Join(p.paths.StateDir, installStateMarkerName))
	if err != nil {
		return "", err
	}
	if !sameFilePath(filepath.Dir(markerPath), p.paths.StateDir) {
		return "", errors.New("install state marker escapes state directory")
	}
	return markerPath, nil
}

type installConfigSnapshot struct {
	exists bool
	data   []byte
	mode   os.FileMode
	info   os.FileInfo
}

type installConfigPersistence struct {
	mu           sync.Mutex
	paths        config.Paths
	explicitHome string
	write        func(config.Paths, []byte, os.FileMode) error
	expected     *installConfigSnapshot
	attempted    bool
	applied      bool
	restored     bool
	committed    bool
	original     installConfigSnapshot
	written      []byte
}

func newInstallConfigPersistence(
	paths config.Paths,
	explicitHome string,
	write func(config.Paths, []byte, os.FileMode) error,
) *installConfigPersistence {
	return &installConfigPersistence{paths: paths, explicitHome: explicitHome, write: write}
}

func (p *installConfigPersistence) Expect(preview installConfigPreview) {
	if preview.source == installConfigSourcePrevious {
		return
	}
	expected := preview.current
	p.expected = &expected
}

func (p *installConfigPersistence) Wrap(ops lifecycleOps) lifecycleOps {
	beginServiceRepair := ops.BeginServiceRepair
	ops.BeginServiceRepair = func(executable, stateDir string) (platform.ServiceResult, func() error, error) {
		if err := p.apply(); err != nil {
			return platform.ServiceResult{}, nil, &installConfigPersistenceError{err: err}
		}
		result, rollbackService, err := beginServiceRepair(executable, stateDir)
		if err != nil {
			return result, nil, errors.Join(err, p.Restore())
		}
		return result, func() error {
			var serviceErr error
			if rollbackService != nil {
				serviceErr = rollbackService()
			}
			return errors.Join(serviceErr, p.Restore())
		}, nil
	}
	installService := ops.InstallService
	ops.InstallService = func(executable, stateDir string) (platform.ServiceResult, error) {
		if err := p.apply(); err != nil {
			return platform.ServiceResult{}, &installConfigPersistenceError{err: err}
		}
		result, err := installService(executable, stateDir)
		if err == nil {
			return result, nil
		}
		if restoreErr := p.Restore(); restoreErr != nil {
			return result, errors.Join(err, fmt.Errorf("restore install config: %w", restoreErr))
		}
		return result, err
	}
	uninstallService := ops.UninstallService
	ops.UninstallService = func(executable, stateDir string) error {
		serviceErr := uninstallService(executable, stateDir)
		restoreErr := p.Restore()
		if restoreErr != nil {
			restoreErr = fmt.Errorf("restore install config: %w", restoreErr)
		}
		return errors.Join(serviceErr, restoreErr)
	}
	return ops
}

func (p *installConfigPersistence) PrepareMigratedConfig(
	previous platform.PreviousService,
	preview installConfigPreview,
) (bool, error) {
	if strings.TrimSpace(p.explicitHome) == "" || !hasPreviousService(previous) ||
		preview.source != installConfigSourcePrevious || !preview.changed {
		return false, nil
	}
	original, err := readInstallConfigSnapshot(p.paths.ConfigPath)
	if err != nil {
		return false, err
	}
	if !sameInstallConfigSnapshot(original, preview.current) {
		return false, errors.New("current install config changed after preflight")
	}
	source, err := readInstallConfigSnapshot(preview.previousPath)
	if err != nil {
		return false, err
	}
	if !sameInstallConfigSnapshot(source, preview.previous) {
		return false, errors.New("previous install config changed after preflight")
	}
	written := append([]byte(nil), preview.written...)

	p.mu.Lock()
	p.attempted = true
	p.original = original
	p.written = written
	p.mu.Unlock()
	writeErr := writeNewInstallConfig(p.paths.ConfigPath, written)
	current, currentErr := readInstallConfigSnapshot(p.paths.ConfigPath)
	owned := currentErr == nil && current.exists && bytes.Equal(current.data, written)
	p.mu.Lock()
	p.applied = owned
	p.mu.Unlock()
	if writeErr != nil {
		if owned {
			return false, errors.Join(writeErr, p.Restore())
		}
		if currentErr != nil {
			return false, errors.Join(writeErr, currentErr)
		}
		return false, writeErr
	}
	if currentErr != nil {
		return false, currentErr
	}
	if !owned {
		return false, errors.New("prepared install config does not match the requested content")
	}
	if _, err := parseInstallConfigData(current.data, p.paths.ConfigPath); err != nil {
		return false, errors.Join(err, p.Restore())
	}
	return true, nil
}

func (p *installConfigPersistence) apply() error {
	p.mu.Lock()
	if p.attempted || strings.TrimSpace(p.explicitHome) == "" {
		p.attempted = true
		p.mu.Unlock()
		return nil
	}
	p.attempted = true
	p.mu.Unlock()

	original, err := readInstallConfigSnapshot(p.paths.ConfigPath)
	if err != nil {
		return err
	}
	if p.expected != nil && !sameInstallConfigSnapshot(original, *p.expected) {
		return errors.New("install config changed after preflight")
	}
	data := original.data
	if !original.exists {
		data, err = marshalInstallConfig(config.Default())
		if err != nil {
			return err
		}
	}
	_, written, changed, err := prepareInstallConfigData(data, p.paths.ConfigPath, p.explicitHome)
	if err != nil || !changed {
		return err
	}
	p.mu.Lock()
	p.original = original
	p.written = written
	p.mu.Unlock()
	if original.exists && bytes.Equal(original.data, written) {
		return nil
	}

	saveErr := p.write(p.paths, written, 0o600)
	current, currentErr := readInstallConfigSnapshot(p.paths.ConfigPath)
	owned := currentErr == nil && current.exists && bytes.Equal(current.data, written)
	p.mu.Lock()
	p.applied = owned
	p.mu.Unlock()
	if saveErr != nil {
		if owned {
			return errors.Join(saveErr, p.Restore())
		}
		if currentErr != nil {
			return errors.Join(saveErr, currentErr)
		}
		if !sameInstallConfigSnapshot(current, original) {
			return errors.Join(saveErr, errors.New("install config changed to unowned content; rollback refused"))
		}
		return saveErr
	}
	if currentErr != nil {
		return currentErr
	}
	if !owned {
		return errors.New("saved install config does not match the requested content")
	}
	return nil
}

func (p *installConfigPersistence) Restore() error {
	p.mu.Lock()
	if !p.applied || p.restored || p.committed {
		p.mu.Unlock()
		return nil
	}
	original := p.original
	written := append([]byte(nil), p.written...)
	p.mu.Unlock()

	current, err := readInstallConfigSnapshot(p.paths.ConfigPath)
	if err != nil {
		return err
	}
	if !current.exists || !bytes.Equal(current.data, written) {
		return errors.New("install config changed after persistence; rollback refused")
	}
	if original.exists {
		err = atomicWriteInstallConfig(p.paths.ConfigPath, original.data, original.mode.Perm())
	} else {
		if current.mode&os.ModeSymlink != 0 || !current.mode.IsRegular() {
			return errors.New("created install config is no longer a safe regular file")
		}
		err = os.Remove(p.paths.ConfigPath)
		if err == nil {
			err = platform.SyncParent(p.paths.ConfigPath)
		}
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.restored = true
	p.mu.Unlock()
	return nil
}

func (p *installConfigPersistence) Commit() {
	p.mu.Lock()
	p.committed = true
	p.mu.Unlock()
}

func inspectInstallStatePreview(
	deps installCommandDeps,
	paths config.Paths,
	explicitHome string,
) (installStatePreview, error) {
	migration, result, previousService, err := deps.InspectPrevious(paths)
	if err != nil {
		return installStatePreview{}, err
	}
	preview, err := buildInstallConfigPreview(paths, result, previousService, explicitHome)
	if err != nil {
		return installStatePreview{}, &installConfigPreviewFailure{err: err}
	}
	return installStatePreview{
		migration: migration, migrationResult: result,
		previousService: previousService, config: preview,
	}, nil
}

func buildInstallConfigPreview(
	paths config.Paths,
	migration config.MigrationResult,
	previousService platform.PreviousService,
	explicitHome string,
) (installConfigPreview, error) {
	current, err := readInstallConfigSnapshot(paths.ConfigPath)
	if err != nil {
		return installConfigPreview{}, err
	}
	preview := installConfigPreview{current: current, source: installConfigSourceDefault}
	if migration.Found || hasPreviousService(previousService) {
		previousPath, pathErr := resolvedPreviousInstallConfigPath(previousService)
		if pathErr != nil {
			return installConfigPreview{}, pathErr
		}
		previous, readErr := readInstallConfigSnapshot(previousPath)
		if readErr != nil {
			return installConfigPreview{}, readErr
		}
		preview.previousPath = previousPath
		preview.previous = previous
	}

	data := current.data
	switch {
	case current.exists:
		preview.source = installConfigSourceCurrent
	case preview.previous.exists:
		preview.source = installConfigSourcePrevious
		data = preview.previous.data
	default:
		data, err = marshalInstallConfig(config.Default())
		if err != nil {
			return installConfigPreview{}, err
		}
	}
	preview.cfg, preview.written, preview.changed, err = prepareInstallConfigData(data, paths.ConfigPath, explicitHome)
	if err != nil {
		return installConfigPreview{}, err
	}
	preview.homes, err = installCodexHomes(preview.cfg, explicitHome)
	if err != nil {
		return installConfigPreview{}, err
	}
	return preview, nil
}

func resolvedPreviousInstallConfigPath(previousService platform.PreviousService) (string, error) {
	if strings.TrimSpace(previousService.StateDir) != "" {
		stateDir, err := canonicalAbsolutePath(previousService.StateDir)
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, "config.json"), nil
	}
	previous, err := config.ResolvePreviousPaths()
	if err != nil {
		return "", err
	}
	return canonicalAbsolutePath(previous.ConfigPath)
}

func prepareInstallConfigData(
	data []byte,
	path string,
	explicitHome string,
) (config.Config, []byte, bool, error) {
	cfg, err := parseInstallConfigData(data, path)
	if err != nil {
		return config.Config{}, nil, false, err
	}
	written := append([]byte(nil), data...)
	if strings.TrimSpace(explicitHome) == "" || containsInstallHome(cfg.ExtraCodexHomes, explicitHome) {
		return cfg, written, false, nil
	}
	written, changed, err := installConfigWithHome(data, explicitHome)
	if err != nil {
		return config.Config{}, nil, false, err
	}
	if !changed {
		return cfg, append([]byte(nil), data...), false, nil
	}
	cfg, err = parseInstallConfigData(written, path)
	if err != nil {
		return config.Config{}, nil, false, err
	}
	return cfg, written, true, nil
}

func parseInstallConfigData(data []byte, path string) (config.Config, error) {
	cfg := config.Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config.Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1"
	}
	if cfg.ListenAddress != "127.0.0.1" && cfg.ListenAddress != "localhost" {
		return config.Config{}, fmt.Errorf("refuse non-loopback listen address %q", cfg.ListenAddress)
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return config.Config{}, fmt.Errorf("invalid port %d", cfg.Port)
	}
	if cfg.ScanIntervalSeconds < 600 {
		cfg.ScanIntervalSeconds = 600
	}
	cfg.ExtraCodexHomes = normalizedInstallHomes(cfg.ExtraCodexHomes)
	normalizedPricing, err := pricing.NormalizeOverrides(cfg.PricingOverrides)
	if err != nil {
		return config.Config{}, fmt.Errorf("invalid pricing_overrides: %w", err)
	}
	cfg.PricingOverrides = normalizedPricing
	return cfg, nil
}

func installCodexHomes(cfg config.Config, explicitHome string) ([]string, error) {
	homes := append([]string(nil), cfg.ExtraCodexHomes...)
	if strings.TrimSpace(explicitHome) != "" {
		homes = append(homes, explicitHome)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		homes = append(homes, filepath.Join(home, ".codex"))
	}
	return normalizedInstallHomes(homes), nil
}

func sameInstallStatePreview(left, right installStatePreview) bool {
	return left.migrationResult == right.migrationResult &&
		left.previousService == right.previousService &&
		left.config.source == right.config.source &&
		sameFilePath(left.config.previousPath, right.config.previousPath) &&
		sameInstallConfigSnapshot(left.config.current, right.config.current) &&
		sameInstallConfigSnapshot(left.config.previous, right.config.previous) &&
		bytes.Equal(left.config.written, right.config.written) &&
		equalInstallStrings(left.config.homes, right.config.homes)
}

func equalInstallStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func resolvedExplicitCodexHome() (string, error) {
	raw := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if raw == "" {
		return "", nil
	}
	return canonicalAbsolutePath(raw)
}

func normalizedInstallHomes(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		absolute, err := canonicalAbsolutePath(value)
		if err != nil {
			continue
		}
		key := absolute
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, absolute)
	}
	sort.Strings(result)
	return result
}

func containsInstallHome(values []string, want string) bool {
	want, err := canonicalAbsolutePath(want)
	if err != nil {
		return false
	}
	for _, value := range normalizedInstallHomes(values) {
		if value == want || (runtime.GOOS == "windows" && strings.EqualFold(value, want)) {
			return true
		}
	}
	return false
}

func installConfigWithHome(data []byte, home string) ([]byte, bool, error) {
	cfg := config.Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false, err
	}
	if containsInstallHome(cfg.ExtraCodexHomes, home) {
		return nil, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, false, err
	}
	if fields == nil {
		return nil, false, errors.New("install config must be a JSON object")
	}
	homes, err := json.Marshal(normalizedInstallHomes(append(cfg.ExtraCodexHomes, home)))
	if err != nil {
		return nil, false, err
	}
	fields["extra_codex_homes"] = homes
	written, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(written, '\n'), true, nil
}

func marshalInstallConfig(cfg config.Config) ([]byte, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeNewInstallConfig(path string, data []byte) (returnErr error) {
	return writeNewInstallFile(path, data, installConfigTemporaryPattern)
}

func writeNewInstallFile(path string, data []byte, pattern string) error {
	_, err := createNewInstallFile(path, data, pattern)
	return err
}

func createInstallStateMarker(path string, data []byte) (os.FileInfo, error) {
	return createNewInstallFile(path, data, installMarkerTemporaryPattern)
}

func createNewInstallFile(path string, data []byte, pattern string) (os.FileInfo, error) {
	directory := filepath.Dir(path)
	_, statErr := os.Lstat(directory)
	directoryMissing := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !directoryMissing {
		return nil, statErr
	}
	if err := config.EnsurePrivateDir(directory); err != nil {
		return nil, err
	}
	if directoryMissing {
		if err := platform.SyncParent(directory); err != nil {
			return nil, err
		}
	}
	temporary, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := temporary.Write(data); err != nil {
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		return nil, err
	}
	temporaryInfo, err := temporary.Stat()
	if err != nil {
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := platform.RenameNoReplace(temporaryPath, path); err != nil {
		return nil, err
	}
	return temporaryInfo, platform.SyncParent(path)
}

func readInstallConfigSnapshot(path string) (installConfigSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return installConfigSnapshot{}, nil
	}
	if err != nil {
		return installConfigSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return installConfigSnapshot{}, fmt.Errorf("install config is not a safe regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return installConfigSnapshot{}, err
	}
	return installConfigSnapshot{exists: true, data: data, mode: info.Mode(), info: info}, nil
}

func sameInstallConfigSnapshot(left, right installConfigSnapshot) bool {
	if left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	return bytes.Equal(left.data, right.data) && left.mode.Perm() == right.mode.Perm() &&
		left.info != nil && right.info != nil && os.SameFile(left.info, right.info)
}

func atomicWriteInstallConfig(path string, data []byte, mode os.FileMode) (returnErr error) {
	directory := filepath.Dir(path)
	_, statErr := os.Lstat(directory)
	directoryMissing := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !directoryMissing {
		return statErr
	}
	if err := config.EnsurePrivateDir(directory); err != nil {
		return err
	}
	if directoryMissing {
		if err := platform.SyncParent(directory); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(directory, installConfigAtomicTemporaryPattern)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return platform.SyncParent(path)
}

func writeInstallConfig(paths config.Paths, data []byte, mode os.FileMode) error {
	if err := atomicWriteInstallConfig(paths.ConfigPath, data, mode); err != nil {
		return err
	}
	return ensureInstallStateMarker(paths)
}

func ensureInstallStateMarker(paths config.Paths) error {
	return config.EnsureStateMarker(paths)
}

func waitForInstallIdentity(ctx context.Context, serviceURL string, expected buildIdentity) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		last = probeIdentity(probeCtx, serviceURL, expected)
		cancel()
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return last
		}
		timer := time.NewTimer(180 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func invalidInstallArguments(c CLI, err error) *codedError {
	return &codedError{
		Code: "invalid_arguments", ExitCode: 2,
		Err: fmt.Errorf(c.tr("error.invalidArguments"), err),
	}
}

func (c CLI) resolvedInstallDependencies() installCommandDeps {
	deps := installCommandDeps{
		ResolvePaths:    config.ResolvePaths,
		Executable:      os.Executable,
		PreflightPort:   defaultInstallPreflightPort,
		InspectPrevious: defaultInspectPreviousState,
		RunLifecycle:    executeLifecycle,
	}
	if c.installDeps == nil {
		return deps
	}
	if c.installDeps.ResolvePaths != nil {
		deps.ResolvePaths = c.installDeps.ResolvePaths
	}
	if c.installDeps.Executable != nil {
		deps.Executable = c.installDeps.Executable
	}
	if c.installDeps.PreflightPort != nil {
		deps.PreflightPort = c.installDeps.PreflightPort
	}
	if c.installDeps.InspectPrevious != nil {
		deps.InspectPrevious = c.installDeps.InspectPrevious
	}
	if c.installDeps.RunLifecycle != nil {
		deps.RunLifecycle = c.installDeps.RunLifecycle
	}
	return deps
}

func newInstallConfirmation(paths config.Paths, cfg config.Config, homes []string) installConfirmation {
	return installConfirmation{
		Repository:  "https://github.com/edisoncccc/codex-usage",
		Source:      "source_build",
		InstallPath: paths.InstalledEXE, StatePath: paths.StateDir, DatabasePath: paths.Database,
		Service:      currentUserServiceDescription(),
		DashboardURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		ScanScope:    append([]string(nil), homes...), DataPreservedByDefault: true,
	}
}

func currentUserServiceDescription() string {
	if runtime.GOOS == "windows" {
		return "HKCU current-user startup"
	}
	return "systemd --user"
}

func writeHumanInstallConfirmation(c CLI, value installConfirmation, prompt bool) error {
	source := value.Source
	if source == install.SourceBuild {
		source = c.tr("install.source.sourceBuild")
	}
	lines := []string{
		c.tr("install.confirm.title"),
		c.tr("install.confirm.repository", value.Repository),
		c.tr("install.confirm.source", source),
		c.tr("install.confirm.installPath", value.InstallPath),
		c.tr("install.confirm.statePath", value.StatePath),
		c.tr("install.confirm.service", value.Service),
		c.tr("install.confirm.loopback", value.DashboardURL),
		c.tr("install.confirm.scanScope", strings.Join(value.ScanScope, ", ")),
		c.tr("install.confirm.preserve"),
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(c.Stdout, line); err != nil {
			return err
		}
	}
	if prompt {
		_, err := fmt.Fprint(c.Stdout, c.tr("install.confirm.prompt"))
		return err
	}
	return nil
}

func readInstallConfirmation(reader interface{ Read([]byte) (int, error) }) (bool, error) {
	if reader == nil {
		return false, errors.New("confirmation input is unavailable")
	}
	value, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) && len(value) == 0 {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes", "是", "确认":
		return true, nil
	default:
		return false, nil
	}
}

func absoluteInstallPaths(paths config.Paths) (config.Paths, error) {
	fields := []*string{
		&paths.StateDir, &paths.ConfigPath, &paths.InstallRecord, &paths.Database,
		&paths.BackupDir, &paths.InstallDir, &paths.InstalledEXE,
	}
	for _, field := range fields {
		if strings.TrimSpace(*field) == "" {
			return config.Paths{}, errors.New("install path is empty")
		}
		value, err := canonicalAbsolutePath(*field)
		if err != nil {
			return config.Paths{}, err
		}
		*field = value
	}
	return paths, nil
}

func canonicalAbsolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if !filepath.IsAbs(absolute) || strings.ContainsAny(absolute, "\x00\r\n") {
		return "", fmt.Errorf("path must be canonical and absolute: %q", path)
	}
	return absolute, nil
}

func probeInstallDirectoryWritable(path string) error {
	path, err := canonicalAbsolutePath(path)
	if err != nil {
		return err
	}
	probeDir := path
	for {
		info, statErr := os.Lstat(probeDir)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return &installPreflightFailure{code: "permission_required", err: fmt.Errorf("install directory is not a safe directory: %s", probeDir)}
			}
			resolved, resolveErr := filepath.EvalSymlinks(probeDir)
			if resolveErr != nil {
				return &installPreflightFailure{code: "existing_install_untrusted", err: resolveErr}
			}
			resolved, resolveErr = canonicalAbsolutePath(resolved)
			if resolveErr != nil || !sameFilePath(resolved, probeDir) {
				return &installPreflightFailure{code: "existing_install_untrusted", err: fmt.Errorf("install directory crosses a symlink or reparse point: %s", path)}
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(probeDir)
		if parent == probeDir {
			return fmt.Errorf("no writable ancestor for %s", path)
		}
		probeDir = parent
	}
	probe, err := os.CreateTemp(probeDir, ".codex-usage-write-probe-*")
	if err != nil {
		return &installPreflightFailure{code: "permission_required", err: err}
	}
	if err := closeAndRemoveInstallProbe(probe); err != nil {
		return &installPreflightFailure{code: "permission_required", err: err}
	}
	return nil
}

func closeAndRemoveInstallProbe(probe *os.File) (returnErr error) {
	path := probe.Name()
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(path)
		}
	}()
	if err := probe.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	removed = true
	return nil
}

func defaultInstallPreflightPort(ctx context.Context, serviceURL string, record *install.Record) error {
	if _, err := parseHealthEndpoint(serviceURL); err != nil {
		return &installPreflightFailure{code: "existing_install_untrusted", err: err}
	}
	parsed, _ := url.Parse(serviceURL)
	listener, listenErr := (&net.ListenConfig{}).Listen(ctx, "tcp4", parsed.Host)
	if listenErr == nil {
		return listener.Close()
	}
	if errors.Is(listenErr, os.ErrPermission) {
		return &installPreflightFailure{code: "permission_required", err: listenErr}
	}
	if record != nil {
		expected := buildIdentity{
			Version: record.Version, Commit: record.Commit, Dirty: record.Dirty,
			BuildDate: record.BuildDate, OS: record.OS, Arch: record.Arch,
		}
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		probeErr := probeIdentity(probeCtx, serviceURL, expected)
		cancel()
		if probeErr == nil {
			return nil
		}
	}
	return &installPreflightFailure{
		code: "existing_install_untrusted",
		err:  fmt.Errorf("loopback endpoint is occupied by an unknown service: %s", serviceURL),
	}
}

func defaultInspectPreviousState(paths config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
	previous, err := config.ResolvePreviousPaths()
	if err != nil {
		return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, err
	}
	plan, result, err := config.InspectPreviousState(paths, previous)
	if err != nil {
		return plan, result, platform.PreviousService{}, err
	}
	migrationFound, err := previousMigrationFileEvidence(paths, previous, result)
	if err != nil {
		return plan, result, platform.PreviousService{}, err
	}
	serviceFound, err := previousServiceFileEvidence(previous)
	if err != nil {
		return plan, result, platform.PreviousService{}, err
	}
	if !migrationFound && !serviceFound {
		return plan, result, platform.PreviousService{}, nil
	}
	service := platform.PreviousService{
		StateDir: previous.StateDir, Executable: previous.InstalledEXE, InstallDir: previous.InstallDir,
		PIDPath: previous.PIDPath, LauncherPath: previous.LauncherPath,
		StartupEntry: previous.StartupEntry, ServiceName: previous.ServiceName,
	}
	return plan, result, service, nil
}

func previousMigrationFileEvidence(
	paths config.Paths,
	previous config.PreviousPaths,
	result config.MigrationResult,
) (bool, error) {
	if result.DatabaseMoved || result.DatabaseConflict || result.ConfigMoved || result.BackupsMoved {
		return true, nil
	}
	for _, path := range []string{
		previous.MarkerPath,
		previous.Database,
		previous.ConfigPath,
		previous.BackupDir,
		filepath.Join(previous.StateDir, "daemon.log"),
		filepath.Join(paths.StateDir, filepath.Base(previous.Database)),
	} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func previousServiceFileEvidence(previous config.PreviousPaths) (bool, error) {
	for _, path := range []string{previous.InstalledEXE, previous.PIDPath, previous.LauncherPath} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func installCommandFailure(err error, fallbackCode string) error {
	if err == nil {
		err = errors.New(fallbackCode)
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded
	}
	var preflight *installPreflightFailure
	if errors.As(err, &preflight) {
		return &codedError{Code: preflight.code, ExitCode: 1, Err: err}
	}
	var preview *installConfigPreviewFailure
	if errors.As(err, &preview) {
		fallbackCode = "config_invalid"
		var pathError *os.PathError
		if errors.As(preview.err, &pathError) {
			fallbackCode = "permission_required"
		}
	}
	var permission *platform.PermissionError
	if errors.As(err, &permission) || errors.Is(err, os.ErrPermission) {
		fallbackCode = "permission_required"
	}
	if fallbackCode == "permission_required" {
		return &codedError{Code: fallbackCode, ExitCode: 1, Err: err}
	}
	var identity *identityProbeError
	if errors.As(err, &identity) {
		fallbackCode = "health_check_failed"
	}
	var persistence *installConfigPersistenceError
	if errors.As(err, &persistence) && fallbackCode != "permission_required" {
		return &codedError{Code: "config_write_failed", ExitCode: 1, Err: err}
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "identity health"):
		fallbackCode = "health_check_failed"
	case strings.Contains(lower, "existing_install_untrusted"),
		strings.Contains(lower, "untrusted existing install"),
		strings.Contains(lower, "downgrade is not allowed"):
		fallbackCode = "existing_install_untrusted"
	case strings.Contains(lower, "install service"), strings.Contains(lower, "repair service"):
		fallbackCode = "service_start_failed"
	}
	return &codedError{Code: fallbackCode, ExitCode: 1, Err: err}
}
