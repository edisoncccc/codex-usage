package app

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/usage"
)

type installCommandFixture struct {
	t             *testing.T
	root          string
	paths         config.Paths
	candidatePath string
	identity      buildIdentity
	deps          installCommandDeps
	runCalls      int
	lastRequest   lifecycleRequest
	lastOps       lifecycleOps
}

func newInstallCommandFixture(t *testing.T) *installCommandFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	installDir := filepath.Join(root, "program")
	executableName := "codex-usage"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	candidateDir := filepath.Join(root, "candidate")
	if err := os.MkdirAll(candidateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(candidateDir, executableName)
	if err := os.WriteFile(candidatePath, []byte("synthetic-candidate-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := buildIdentity{
		Version: "2.3.5", Commit: "dev", Dirty: true, BuildDate: "unknown",
		OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
	restoreBuildMetadata(t, identity.Version, identity.Commit, "true", identity.BuildDate)
	fixture := &installCommandFixture{
		t:             t,
		root:          root,
		candidatePath: candidatePath,
		identity:      identity,
		paths: config.Paths{
			StateDir:      stateDir,
			ConfigPath:    filepath.Join(stateDir, "config.json"),
			InstallRecord: filepath.Join(stateDir, "install.json"),
			Database:      filepath.Join(stateDir, "usage.sqlite"),
			BackupDir:     filepath.Join(stateDir, "backups"),
			InstallDir:    installDir,
			InstalledEXE:  filepath.Join(installDir, executableName),
		},
	}
	fixture.deps = installCommandDeps{
		ResolvePaths: func() (config.Paths, error) { return fixture.paths, nil },
		Executable:   func() (string, error) { return fixture.candidatePath, nil },
		PreflightPort: func(context.Context, string, *install.Record) error {
			return nil
		},
		InspectPrevious: func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
			return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, nil
		},
		RunLifecycle: fixture.successfulLifecycle,
	}
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex-home"))
	return fixture
}

func (f *installCommandFixture) successfulLifecycle(
	_ context.Context,
	request lifecycleRequest,
	ops lifecycleOps,
	report lifecycleProgress,
) (lifecycleResult, error) {
	f.runCalls++
	f.lastRequest = request
	f.lastOps = ops
	for _, phase := range []string{"stop_service", "install"} {
		report(phase, nil)
	}
	progress := usage.ScanProgress{
		HomesTotal: 1, HomesDiscovered: 1, FilesDiscovered: 2,
		FilesProcessed: 2, RecordsProcessed: 7, EventsInserted: 3, Warnings: 1,
	}
	report("scan", progress)
	report("start_service", nil)
	report("health_check", nil)
	if err := os.MkdirAll(filepath.Dir(request.DestinationPath), 0o700); err != nil {
		return lifecycleResult{}, err
	}
	candidate, err := os.ReadFile(request.CandidatePath)
	if err != nil {
		return lifecycleResult{}, err
	}
	if err := os.WriteFile(request.DestinationPath, candidate, 0o700); err != nil {
		return lifecycleResult{}, err
	}
	digest, err := install.FileSHA256(request.DestinationPath)
	if err != nil {
		return lifecycleResult{}, err
	}
	return lifecycleResult{
		Decision:        install.DecisionFresh,
		CandidateSHA256: digest,
		Service: platform.ServiceResult{
			Installed: true, Started: true, Mode: platform.ServiceModePersistent,
		},
		Scan: usage.ScanResult{
			Homes: 1, Files: 2, Records: 7, EventsInserted: 3, Warnings: 1,
		},
		ScanWarnings:  []string{"synthetic recoverable warning"},
		DataPreserved: false,
	}, nil
}

func (f *installCommandFixture) cli(stdout io.Writer, stderr io.Writer, stdin io.Reader) CLI {
	return CLI{
		Stdout: stdout, Stderr: stderr, Stdin: stdin,
		Now:               func() time.Time { return time.Date(2026, time.August, 27, 1, 2, 3, 0, time.UTC) },
		HeartbeatInterval: time.Hour,
		installDeps:       &f.deps,
	}
}

func runInstallJSON(t *testing.T, fixture *installCommandFixture, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := fixture.cli(&stdout, &stderr, strings.NewReader("")).Run(args)
	return code, stdout.String(), stderr.String()
}

func installTerminalEvent(t *testing.T, output string) map[string]any {
	t.Helper()
	events := decodeMachineEvents(t, output)
	var terminal map[string]any
	for _, event := range events {
		assertMachineEventEnvelope(t, event)
		if event["event"] == "result" || event["event"] == "error" {
			if terminal != nil {
				t.Fatalf("multiple terminal events: %#v", events)
			}
			terminal = event
		}
	}
	if terminal == nil {
		t.Fatalf("missing terminal event: %#v", events)
	}
	return terminal
}

func installResultReceipt(t *testing.T, output string) map[string]any {
	t.Helper()
	terminal := installTerminalEvent(t, output)
	if terminal["event"] != "result" || terminal["code"] != "install_complete" {
		t.Fatalf("unexpected terminal event: %#v", terminal)
	}
	receipt, ok := terminal["result"].(map[string]any)
	if !ok {
		t.Fatalf("install receipt is not an object: %#v", terminal["result"])
	}
	return receipt
}

func verificationStatuses(t *testing.T, receipt map[string]any) map[string]string {
	t.Helper()
	raw, ok := receipt["verifications"].([]any)
	if !ok {
		t.Fatalf("verifications=%#v", receipt["verifications"])
	}
	result := make(map[string]string, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("verification=%#v", value)
		}
		name, nameOK := item["name"].(string)
		status, statusOK := item["status"].(string)
		if !nameOK || !statusOK || name == "" || status == "" {
			t.Fatalf("verification=%#v", item)
		}
		result[name] = status
	}
	return result
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("stdin must not be read")
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type countedReader struct {
	mu    sync.Mutex
	reads int
	data  *strings.Reader
}

func newCountedReader(value string) *countedReader {
	return &countedReader{data: strings.NewReader(value)}
}

func (r *countedReader) Read(target []byte) (int, error) {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.data.Read(target)
}

func (r *countedReader) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func waitForInstallPhaseCopies(t *testing.T, output *lockedBuffer, phase string, copies int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, err := parseCompleteMachineEvents(output.String())
		if err == nil {
			matched := 0
			for _, event := range events {
				if event["event"] == "progress" && event["phase"] == phase {
					matched++
				}
			}
			if matched >= copies {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase %q was not emitted %d times: %q", phase, copies, output.String())
}

func realInstallLifecycleWithFakeServices(
	calls *[]string,
	stopErr error,
	suspendErr error,
) func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error) {
	return realInstallLifecycleWithFakeOptions(calls, installLifecycleFakeOptions{
		stopErr: stopErr, suspendErr: suspendErr,
	})
}

type installLifecycleFakeOptions struct {
	stopErr              error
	suspendErr           error
	installServiceErr    error
	healthErr            error
	beforeInstallService func()
	useRealScan          bool
}

func realInstallLifecycleWithFakeOptions(
	calls *[]string,
	options installLifecycleFakeOptions,
) func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error) {
	return func(
		ctx context.Context,
		request lifecycleRequest,
		ops lifecycleOps,
		report lifecycleProgress,
	) (lifecycleResult, error) {
		ops.StopService = func(string, string) error {
			*calls = append(*calls, "stop_current")
			return options.stopErr
		}
		ops.InstallService = func(string, string) (platform.ServiceResult, error) {
			*calls = append(*calls, "install_service")
			if options.beforeInstallService != nil {
				options.beforeInstallService()
			}
			if options.installServiceErr != nil {
				return platform.ServiceResult{}, options.installServiceErr
			}
			return platform.ServiceResult{Installed: true, Started: true, Mode: platform.ServiceModePersistent}, nil
		}
		ops.UninstallService = func(string, string) error {
			*calls = append(*calls, "uninstall_service")
			return nil
		}
		ops.SuspendPrevious = func(platform.PreviousService) error {
			*calls = append(*calls, "suspend_previous")
			return options.suspendErr
		}
		ops.ResumePrevious = func(platform.PreviousService) error {
			*calls = append(*calls, "resume_previous")
			return nil
		}
		ops.RemovePrevious = func(platform.PreviousService) error {
			*calls = append(*calls, "remove_previous")
			return nil
		}
		ops.ProbeIdentity = func(context.Context, string, buildIdentity) error {
			*calls = append(*calls, "health")
			return options.healthErr
		}
		if !options.useRealScan {
			ops.Scan = func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
				*calls = append(*calls, "scan")
				return installScanOutcome{}, nil
			}
		}
		ops = withInstallStopProgress(ops, hasPreviousService(request.PreviousService), report)
		explicitHome, err := resolvedExplicitCodexHome()
		if err != nil {
			return lifecycleResult{}, err
		}
		persistence := newInstallConfigPersistence(config.Paths{
			StateDir: request.StateDir, ConfigPath: filepath.Join(request.StateDir, "config.json"),
		}, explicitHome, writeInstallConfig)
		ops = persistence.Wrap(ops)
		result, err := executeLifecycle(ctx, request, ops, report)
		if err == nil {
			persistence.Commit()
		}
		return result, err
	}
}

func seedInstallConfig(t *testing.T, paths config.Paths, homes []string) []byte {
	t.Helper()
	cfg := config.Default()
	cfg.ExtraCodexHomes = append([]string(nil), homes...)
	if err := config.Save(paths, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func samePathSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	normalize := func(values []string) []string {
		result := make([]string, len(values))
		for index, value := range values {
			value = filepath.Clean(value)
			if runtime.GOOS == "windows" {
				value = strings.ToLower(value)
			}
			result[index] = value
		}
		sort.Strings(result)
		return result
	}
	return reflect.DeepEqual(normalize(got), normalize(want))
}

func writeExistingInstallFixture(t *testing.T, fixture *installCommandFixture, version, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fixture.paths.InstalledEXE), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.paths.InstalledEXE, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := install.FileSHA256(fixture.paths.InstalledEXE)
	if err != nil {
		t.Fatal(err)
	}
	record := install.Record{
		SchemaVersion: install.RecordSchemaVersion, Product: install.ProductName,
		Version: version, Commit: "dev", Dirty: true, BuildDate: "unknown",
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		ExecutablePath: fixture.paths.InstalledEXE, ExecutableSHA256: digest,
		Source: install.SourceBuild, InstalledAt: "2026-08-26T01:02:03Z",
	}
	if err := install.Save(fixture.paths.InstallRecord, record); err != nil {
		t.Fatal(err)
	}
}

func assertInstallStopProgress(t *testing.T, output string, want []string) {
	t.Helper()
	var statuses []string
	for _, event := range decodeMachineEvents(t, output) {
		if event["event"] == "progress" && event["phase"] == "stop_service" {
			status, _ := event["status"].(string)
			statuses = append(statuses, status)
		}
	}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("stop_service statuses=%v want=%v output=%q", statuses, want, output)
	}
}

func assertInstallProgressOrder(t *testing.T, output string, want ...string) {
	t.Helper()
	var got []string
	for _, event := range decodeMachineEvents(t, output) {
		if event["event"] != "progress" {
			continue
		}
		phase, _ := event["phase"].(string)
		status, _ := event["status"].(string)
		got = append(got, phase+":"+status)
	}
	position := 0
	for _, value := range got {
		if position < len(want) && value == want[position] {
			position++
		}
	}
	if position != len(want) {
		t.Fatalf("progress order=%v missing ordered subsequence=%v", got, want)
	}
}

type installFixtureFile struct {
	Mode fs.FileMode
	Data string
}

func snapshotInstallFixtureTree(t *testing.T, root string) map[string]installFixtureFile {
	t.Helper()
	snapshot := map[string]installFixtureFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := installFixtureFile{Mode: info.Mode()}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.Data = string(data)
		}
		snapshot[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func stringSliceFromJSON(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value is not a JSON array: %#v", value)
	}
	result := make([]string, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("value[%d] is not a string: %#v", index, item)
		}
		result[index] = text
	}
	return result
}

func writeLegacyOTelFixture(t *testing.T, home, contents string) string {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path := config.CodexConfigPath(home)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsInstallWarningCode(warnings []string, code string) bool {
	for _, warning := range warnings {
		if strings.HasPrefix(warning, code) {
			return true
		}
	}
	return false
}

func seedPreviousInstallConfig(t *testing.T, fixture *installCommandFixture) (config.PreviousPaths, []byte) {
	t.Helper()
	legacyState := filepath.Join(fixture.root, "legacy-state")
	t.Setenv("CODEX_METER_HOME", legacyState)
	previous, err := config.ResolvePreviousPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous.MarkerPath, []byte(previous.MarkerValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyExtraHome := filepath.Join(fixture.root, "legacy-extra-home")
	original := []byte("{\n" +
		`  "listen_address": "127.0.0.1",` + "\n" +
		`  "port": 43189,` + "\n" +
		`  "scan_interval_seconds": 600,` + "\n" +
		`  "extra_codex_homes": [` + strconv.Quote(legacyExtraHome) + `],` + "\n" +
		`  "third_party": {"keep": "byte-identical"}` + "\n" +
		"}\n")
	if err := os.WriteFile(previous.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return previous, original
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
