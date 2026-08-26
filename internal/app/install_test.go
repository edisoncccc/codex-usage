package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/usage"
)

func TestInstallJSONRequiresExplicitConfirmation(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	var stdout, stderr bytes.Buffer
	exitCode := fixture.cli(&stdout, &stderr, panicReader{}).Run([]string{"install", "--json"})
	if exitCode == 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if fixture.runCalls != 0 {
		t.Fatalf("lifecycle ran %d times without confirmation", fixture.runCalls)
	}
	terminal := installTerminalEvent(t, stdout.String())
	if terminal["event"] != "error" || terminal["code"] != "confirmation_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
	details, ok := terminal["result"].(map[string]any)
	if !ok {
		t.Fatalf("confirmation details=%#v", terminal["result"])
	}
	for key, want := range map[string]string{
		"install_path":  fixture.paths.InstalledEXE,
		"state_path":    fixture.paths.StateDir,
		"database_path": fixture.paths.Database,
	} {
		got, ok := details[key].(string)
		if !ok || !filepath.IsAbs(got) || got != want {
			t.Fatalf("details[%q]=%#v want=%q", key, details[key], want)
		}
	}
}

func TestInstallJSONEmitsOrderedPhasesAndReceipt(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	events := decodeMachineEvents(t, stdout)
	var phases []string
	for _, event := range events {
		if event["event"] == "progress" {
			phases = append(phases, event["phase"].(string))
		}
	}
	wantPhases := []string{"preflight", "stop_service", "install", "scan", "start_service", "health_check", "complete"}
	if !reflect.DeepEqual(phases, wantPhases) {
		t.Fatalf("phases=%v want=%v output=%q", phases, wantPhases, stdout)
	}
	receipt := installResultReceipt(t, stdout)
	for key, want := range map[string]string{
		"install_path":      fixture.paths.InstalledEXE,
		"state_path":        fixture.paths.StateDir,
		"database_path":     fixture.paths.Database,
		"service_mode":      string(platform.ServiceModePersistent),
		"dashboard_url":     "http://127.0.0.1:43189",
		"uninstall_command": "codex-usage uninstall --yes --json",
		"purge_command":     "codex-usage uninstall --purge --yes --json",
	} {
		if receipt[key] != want {
			t.Fatalf("receipt[%q]=%#v want=%q", key, receipt[key], want)
		}
	}
	if receipt["data_preserved"] != false {
		t.Fatalf("data_preserved=%#v", receipt["data_preserved"])
	}
	identity, ok := receipt["identity"].(map[string]any)
	if !ok || identity["version"] != fixture.identity.Version || identity["os"] != fixture.identity.OS || identity["arch"] != fixture.identity.Arch {
		t.Fatalf("identity=%#v", receipt["identity"])
	}
	scan, ok := receipt["scan"].(map[string]any)
	if !ok || scan["files"] != float64(2) || scan["events_inserted"] != float64(3) {
		t.Fatalf("scan=%#v", receipt["scan"])
	}
	warnings, ok := receipt["scan_warnings"].([]any)
	if !ok || len(warnings) != 1 || warnings[0] != "synthetic recoverable warning" {
		t.Fatalf("scan_warnings=%#v", receipt["scan_warnings"])
	}
	statuses := verificationStatuses(t, receipt)
	wantStatuses := map[string]string{
		"release_immutable":     "not_applicable",
		"artifact_attestation":  "not_applicable",
		"authenticode":          "not_applicable",
		"candidate_copy_sha256": "verified",
	}
	if !reflect.DeepEqual(statuses, wantStatuses) {
		t.Fatalf("verifications=%#v want=%#v", statuses, wantStatuses)
	}
	if fixture.runCalls != 1 || fixture.lastRequest.Source != install.SourceBuild {
		t.Fatalf("runCalls=%d request=%+v", fixture.runCalls, fixture.lastRequest)
	}
}

func TestInstallJSONHeartbeatWhileScanBlocks(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fixture.deps.RunLifecycle = func(
		ctx context.Context,
		request lifecycleRequest,
		_ lifecycleOps,
		report lifecycleProgress,
	) (lifecycleResult, error) {
		report("stop_service", nil)
		report("install", nil)
		report("scan", usage.ScanProgress{HomesTotal: 1, FilesDiscovered: 2, FilesProcessed: 1})
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return lifecycleResult{}, ctx.Err()
		}
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
		report("start_service", nil)
		report("health_check", nil)
		return lifecycleResult{
			Decision: install.DecisionFresh, CandidateSHA256: digest,
			Service: platform.ServiceResult{Installed: true, Started: true, Mode: platform.ServiceModePersistent},
		}, nil
	}
	var stdout lockedBuffer
	var stderr bytes.Buffer
	cli := fixture.cli(&stdout, &stderr, strings.NewReader(""))
	cli.HeartbeatInterval = 10 * time.Millisecond
	done := make(chan int, 1)
	go func() { done <- cli.Run([]string{"install", "--yes", "--json"}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lifecycle scan phase did not start")
	}
	waitForInstallPhaseCopies(t, &stdout, "scan", 2)
	close(release)
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
		}
	case <-time.After(time.Second):
		t.Fatal("install did not finish after releasing scan")
	}
}

func TestInstallJSONSameBinaryIsIdempotent(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	fixture.deps.RunLifecycle = func(
		_ context.Context,
		request lifecycleRequest,
		_ lifecycleOps,
		report lifecycleProgress,
	) (lifecycleResult, error) {
		fixture.runCalls++
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
			Decision: install.DecisionSame, CandidateSHA256: digest,
			Service:       platform.ServiceResult{Installed: true, Started: true, Mode: platform.ServiceModePersistent},
			DataPreserved: true,
		}, nil
	}
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode != 0 || stderr != "" || fixture.runCalls != 1 {
		t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
	}
	receipt := installResultReceipt(t, stdout)
	if receipt["data_preserved"] != true {
		t.Fatalf("receipt=%#v", receipt)
	}
	statuses := verificationStatuses(t, receipt)
	if statuses["candidate_copy_sha256"] != "verified" {
		t.Fatalf("verifications=%#v", statuses)
	}
}

func TestInstallPreflightRejectsForeignLoopbackService(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.paths.ConfigPath, []byte(
		"{\n  \"listen_address\": \"127.0.0.1\",\n  \"port\": "+strconv.Itoa(port)+",\n  \"scan_interval_seconds\": 600\n}\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.deps.PreflightPort = nil
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode == 0 || stderr != "" || fixture.runCalls != 0 {
		t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["event"] != "error" || terminal["code"] != "existing_install_untrusted" {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestInstallPreflightReportsPermissionRequired(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	blocked := filepath.Join(fixture.root, "blocked-state")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.paths.StateDir = blocked
	fixture.paths.ConfigPath = filepath.Join(blocked, "config.json")
	fixture.paths.InstallRecord = filepath.Join(blocked, "install.json")
	fixture.paths.Database = filepath.Join(blocked, "usage.sqlite")
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode == 0 || stderr != "" || fixture.runCalls != 0 {
		t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["event"] != "error" || terminal["code"] != "permission_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestInstallMigrationConflictStopsBeforeReplacement(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	fixture.deps.InspectPrevious = func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
		return config.MigrationPlan{}, config.MigrationResult{Found: true, DatabaseConflict: true}, platform.PreviousService{}, nil
	}
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode == 0 || stderr != "" || fixture.runCalls != 0 {
		t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
	}
	if _, err := os.Stat(fixture.paths.InstalledEXE); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was replaced before conflict rejection: %v", err)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["event"] != "error" || terminal["code"] != "existing_install_untrusted" {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestInstallJSONFailureEmitsStableTerminalCode(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	fixture.deps.RunLifecycle = func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error) {
		return lifecycleResult{}, &identityProbeError{Code: "health_identity_mismatch", Field: "commit", Err: errors.New("synthetic mismatch")}
	}
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode == 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["event"] != "error" || terminal["code"] != "health_check_failed" {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestInstallJSONStdoutContainsNoHumanProse(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	for _, language := range []string{"zh-CN", "en"} {
		exitCode, stdout, stderr := runInstallJSON(t, fixture, "--lang", language, "install", "--yes", "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("language=%s exit=%d stderr=%q stdout=%q", language, exitCode, stderr, stdout)
		}
		for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
			if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
				t.Fatalf("language=%s non-JSON prose line=%q output=%q", language, line, stdout)
			}
		}
		_ = decodeMachineEvents(t, stdout)
	}
}

func TestHumanInstallPromptsAndRemainsLocalized(t *testing.T) {
	for _, test := range []struct {
		language string
		input    string
		want     []string
		reject   string
	}{
		{
			language: "zh-CN", input: "是\n",
			want:   []string{"规范仓库", "源码构建", "安装目录", "状态目录", "当前用户", "127.0.0.1:43189", "扫描范围", "默认卸载保留", "是否继续", "安装完成"},
			reject: "Canonical repository",
		},
		{
			language: "en", input: "yes\n",
			want:   []string{"Canonical repository", "source build", "Install directory", "State directory", "current user", "127.0.0.1:43189", "Scan scope", "keeps", "Continue", "Installation complete"},
			reject: "规范仓库",
		},
	} {
		t.Run(test.language, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			stdin := newCountedReader(test.input)
			var stdout, stderr bytes.Buffer
			exitCode := fixture.cli(&stdout, &stderr, stdin).Run([]string{"--lang", test.language, "install"})
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
			}
			if stdin.readCount() != 1 {
				t.Fatalf("confirmation input read %d times", stdin.readCount())
			}
			for _, fragment := range test.want {
				if !strings.Contains(stdout.String(), fragment) {
					t.Fatalf("missing %q in %q", fragment, stdout.String())
				}
			}
			if strings.Contains(stdout.String(), test.reject) || strings.Contains(stdout.String(), `{"schema_version"`) {
				t.Fatalf("human output leaked another locale or JSON: %q", stdout.String())
			}
		})
	}
}

func TestInstallJSONStopServiceProgressTracksLifecycleSideEffects(t *testing.T) {
	t.Run("fresh install without previous service skips stop", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		var calls []string
		fixture.deps.RunLifecycle = realInstallLifecycleWithFakeServices(&calls, nil, nil)

		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if !reflect.DeepEqual(calls, []string{"install_service", "health"}) {
			t.Fatalf("lifecycle calls=%v", calls)
		}
		assertInstallStopProgress(t, stdout, []string{"skipped"})
		assertInstallProgressOrder(t, stdout,
			"preflight:running", "stop_service:skipped", "install:running")
	})

	t.Run("fresh install suspends previous service", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		previous := platform.PreviousService{StateDir: filepath.Join(fixture.root, "legacy-state")}
		fixture.deps.InspectPrevious = func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
			return config.MigrationPlan{}, config.MigrationResult{Found: true}, previous, nil
		}
		var calls []string
		fixture.deps.RunLifecycle = realInstallLifecycleWithFakeServices(&calls, nil, nil)

		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if !reflect.DeepEqual(calls, []string{"suspend_previous", "install_service", "health", "remove_previous"}) {
			t.Fatalf("lifecycle calls=%v", calls)
		}
		assertInstallStopProgress(t, stdout, []string{"running", "completed"})
		assertInstallProgressOrder(t, stdout,
			"preflight:running", "stop_service:running", "stop_service:completed", "install:running")
	})

	t.Run("failed previous service suspension", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		previous := platform.PreviousService{StateDir: filepath.Join(fixture.root, "legacy-state")}
		fixture.deps.InspectPrevious = func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
			return config.MigrationPlan{}, config.MigrationResult{Found: true}, previous, nil
		}
		var calls []string
		fixture.deps.RunLifecycle = realInstallLifecycleWithFakeServices(
			&calls, nil, errors.New("synthetic previous service stop failure"),
		)

		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode == 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if !reflect.DeepEqual(calls, []string{"suspend_previous", "resume_previous"}) {
			t.Fatalf("lifecycle calls=%v", calls)
		}
		assertInstallStopProgress(t, stdout, []string{"running", "failed"})
		terminal := installTerminalEvent(t, stdout)
		if terminal["event"] != "error" || terminal["code"] != "install_failed" {
			t.Fatalf("terminal=%#v", terminal)
		}
	})

	t.Run("upgrade stops current before previous service", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		writeExistingInstallFixture(t, fixture, "2.3.4", "synthetic-old-binary")
		previous := platform.PreviousService{StateDir: filepath.Join(fixture.root, "legacy-state")}
		fixture.deps.InspectPrevious = func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
			return config.MigrationPlan{}, config.MigrationResult{Found: true}, previous, nil
		}
		var calls []string
		fixture.deps.RunLifecycle = realInstallLifecycleWithFakeServices(&calls, nil, nil)

		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if !reflect.DeepEqual(calls, []string{"stop_current", "suspend_previous", "install_service", "health", "remove_previous"}) {
			t.Fatalf("lifecycle calls=%v", calls)
		}
		assertInstallStopProgress(t, stdout, []string{"running", "completed"})
		assertInstallProgressOrder(t, stdout,
			"preflight:running", "stop_service:running", "stop_service:completed", "install:running")
	})

	t.Run("failed current service stop", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		writeExistingInstallFixture(t, fixture, "2.3.4", "synthetic-old-binary")
		var calls []string
		fixture.deps.RunLifecycle = realInstallLifecycleWithFakeServices(
			&calls, errors.New("synthetic current service stop failure"), nil,
		)

		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode == 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		if !reflect.DeepEqual(calls, []string{"stop_current", "install_service"}) {
			t.Fatalf("lifecycle calls=%v", calls)
		}
		assertInstallStopProgress(t, stdout, []string{"running", "failed"})
		terminal := installTerminalEvent(t, stdout)
		if terminal["event"] != "error" || terminal["code"] != "install_failed" {
			t.Fatalf("terminal=%#v", terminal)
		}
	})
}

func TestInstallJSONConfirmationUsesReadOnlyResolvedConfiguration(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	extraHome := filepath.Join(fixture.root, "extra-codex-home")
	if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configBytes := []byte("{\n  \"listen_address\": \"127.0.0.1\",\n  \"port\": 47654,\n  \"scan_interval_seconds\": 600,\n  \"extra_codex_homes\": [" + strconv.Quote(extraHome) + "]\n}\n")
	if err := os.WriteFile(fixture.paths.ConfigPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotInstallFixtureTree(t, fixture.root)

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--json")
	if exitCode == 0 || stderr != "" || fixture.runCalls != 0 {
		t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
	}
	details, ok := installTerminalEvent(t, stdout)["result"].(map[string]any)
	if !ok {
		t.Fatalf("confirmation details missing: %q", stdout)
	}
	if details["dashboard_url"] != "http://127.0.0.1:47654" {
		t.Fatalf("dashboard_url=%#v", details["dashboard_url"])
	}
	gotHomes := stringSliceFromJSON(t, details["scan_scope"])
	wantHomes := []string{filepath.Join(fixture.root, "codex-home"), extraHome}
	if !reflect.DeepEqual(gotHomes, wantHomes) {
		t.Fatalf("scan_scope=%v want=%v", gotHomes, wantHomes)
	}
	after := snapshotInstallFixtureTree(t, fixture.root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("confirmation-only install changed files:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestInstallMalformedConfigReturnsStableConfigError(t *testing.T) {
	for _, language := range []string{"zh-CN", "en"} {
		t.Run(language, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.paths.ConfigPath, []byte("{not-json\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			exitCode, stdout, stderr := runInstallJSON(t, fixture, "--lang", language, "install", "--json")
			if exitCode == 0 || stderr != "" || fixture.runCalls != 0 {
				t.Fatalf("exit=%d stderr=%q calls=%d stdout=%q", exitCode, stderr, fixture.runCalls, stdout)
			}
			terminal := installTerminalEvent(t, stdout)
			if terminal["event"] != "error" || terminal["code"] != "config_invalid" {
				t.Fatalf("terminal=%#v", terminal)
			}
		})
	}
}

func TestInstallFailureClassificationPrefersUntrusted(t *testing.T) {
	for _, message := range []string{
		"install service: existing_install_untrusted: synthetic conflict",
		"repair service: untrusted existing install cannot be replaced",
		"install service: downgrade is not allowed without an explicit trusted workflow",
	} {
		err := installCommandFailure(errors.New(message), "install_failed")
		var coded *codedError
		if !errors.As(err, &coded) || coded.Code != "existing_install_untrusted" {
			t.Fatalf("message=%q error=%T %v", message, err, err)
		}
	}
}

func TestHumanInstallStopsWhenConfirmationCannotBeWritten(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	stdin := newCountedReader("yes\n")
	var stderr bytes.Buffer
	exitCode := fixture.cli(errorWriter{err: errors.New("synthetic stdout failure")}, &stderr, stdin).Run([]string{"install"})
	if exitCode == 0 || fixture.runCalls != 0 {
		t.Fatalf("exit=%d lifecycle calls=%d stderr=%q", exitCode, fixture.runCalls, stderr.String())
	}
	if stdin.readCount() != 0 {
		t.Fatalf("confirmation stdin read %d times after writer failure", stdin.readCount())
	}
}

func TestHumanInstallYesDoesNotPrintInteractivePrompt(t *testing.T) {
	for _, test := range []struct {
		language string
		prompt   string
	}{
		{language: "zh-CN", prompt: "输入 yes"},
		{language: "en", prompt: "Enter yes"},
	} {
		t.Run(test.language, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			var stdout, stderr bytes.Buffer
			exitCode := fixture.cli(&stdout, &stderr, panicReader{}).Run([]string{"--lang", test.language, "install", "--yes", "--skip-scan"})
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
			}
			if strings.Contains(stdout.String(), test.prompt) {
				t.Fatalf("non-interactive install printed prompt %q: %q", test.prompt, stdout.String())
			}
		})
	}
}

func TestInstallWriteProbeCleanupSurvivesCloseFailure(t *testing.T) {
	probe, err := os.CreateTemp(t.TempDir(), ".probe-*")
	if err != nil {
		t.Fatal(err)
	}
	path := probe.Name()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closeAndRemoveInstallProbe(probe); err == nil {
		t.Fatal("expected the second close to fail")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe remained after close failure: %v", err)
	}
}

func TestInstallPersistsExplicitCodexHomeAcrossLifecycleDecisions(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *installCommandFixture)
	}{
		{name: "fresh"},
		{
			name: "same",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				candidate, err := os.ReadFile(fixture.candidatePath)
				if err != nil {
					t.Fatal(err)
				}
				writeExistingInstallFixture(t, fixture, fixture.identity.Version, string(candidate))
			},
		},
		{
			name: "upgrade",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				writeExistingInstallFixture(t, fixture, "2.3.4", "synthetic-old-binary")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			extraHome := filepath.Join(fixture.root, "already-configured-home")
			seedInstallConfig(t, fixture.paths, []string{extraHome})
			if test.setup != nil {
				test.setup(t, fixture)
			}
			var calls []string
			fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{})

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode != 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
			}
			cfg, err := config.Load(fixture.paths)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{extraHome, filepath.Join(fixture.root, "codex-home")}
			if !samePathSet(cfg.ExtraCodexHomes, want) {
				t.Fatalf("decision=%s extra_codex_homes=%v want=%v", test.name, cfg.ExtraCodexHomes, want)
			}
		})
	}
}

func TestInstallRestoresCodexHomeConfigWhenLifecycleFails(t *testing.T) {
	for _, test := range []struct {
		name    string
		options installLifecycleFakeOptions
	}{
		{
			name: "install service fails",
			options: installLifecycleFakeOptions{
				installServiceErr: errors.New("synthetic install service failure"),
			},
		},
		{
			name: "health check fails",
			options: installLifecycleFakeOptions{
				healthErr: &identityProbeError{Code: "health_identity_mismatch", Field: "commit", Err: errors.New("synthetic health failure")},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			original := seedInstallConfig(t, fixture.paths, []string{filepath.Join(fixture.root, "existing-home")})
			sawPersistedHome := false
			options := test.options
			options.beforeInstallService = func() {
				cfg, err := config.Load(fixture.paths)
				if err == nil && samePathSet(cfg.ExtraCodexHomes, []string{
					filepath.Join(fixture.root, "existing-home"),
					filepath.Join(fixture.root, "codex-home"),
				}) {
					sawPersistedHome = true
				}
			}
			var calls []string
			fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, options)

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode == 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
			}
			if !sawPersistedHome {
				t.Fatalf("CODEX_HOME was not persisted before InstallService; calls=%v", calls)
			}
			after, err := os.ReadFile(fixture.paths.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, original) {
				t.Fatalf("config was not restored exactly:\nwant=%q\ngot=%q", original, after)
			}
		})
	}
}

func TestInstallConfigSaveErrorAfterReplacementRestoresBytes(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	original := seedInstallConfig(t, fixture.paths, []string{filepath.Join(fixture.root, "existing-home")})
	explicitHome := filepath.Join(fixture.root, "codex-home")
	persistence := newInstallConfigPersistence(
		fixture.paths,
		explicitHome,
		func(paths config.Paths, data []byte, mode os.FileMode) error {
			if err := writeInstallConfig(paths, data, mode); err != nil {
				return err
			}
			return errors.New("synthetic error after config replacement")
		},
	)
	installCalled := false
	ops := persistence.Wrap(lifecycleOps{
		InstallService: func(string, string) (platform.ServiceResult, error) {
			installCalled = true
			return platform.ServiceResult{}, nil
		},
		UninstallService: func(string, string) error { return nil },
	})
	if _, err := ops.InstallService(fixture.paths.InstalledEXE, fixture.paths.StateDir); err == nil {
		t.Fatal("expected persistence error")
	}
	if installCalled {
		t.Fatal("InstallService ran after persistence failed")
	}
	after, err := os.ReadFile(fixture.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("config was not restored exactly:\nwant=%q\ngot=%q", original, after)
	}
}

func TestInstallPersistsExplicitHomeWithoutDroppingUnknownConfigFields(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existingHome := filepath.Join(fixture.root, "existing-home")
	original := []byte("{\n" +
		`  "listen_address": "127.0.0.1",` + "\n" +
		`  "port": 43189,` + "\n" +
		`  "scan_interval_seconds": 600,` + "\n" +
		`  "extra_codex_homes": [` + strconv.Quote(existingHome) + `],` + "\n" +
		`  "third_party": {"keep": "unknown-field"}` + "\n" +
		"}\n")
	if err := os.WriteFile(fixture.paths.ConfigPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{})

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, calls)
	}
	after, err := os.ReadFile(fixture.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte(`"third_party"`)) || !bytes.Contains(after, []byte(`"unknown-field"`)) {
		t.Fatalf("unknown config fields were dropped: %q", after)
	}
	cfg, err := config.Load(fixture.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !samePathSet(cfg.ExtraCodexHomes, []string{existingHome, filepath.Join(fixture.root, "codex-home")}) {
		t.Fatalf("extra_codex_homes=%v", cfg.ExtraCodexHomes)
	}
}

func TestAtomicWriteInstallConfigDoesNotUseRollbackGap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows replacement semantics")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	rollbackPath := path + ".install-config-rollback"
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollbackPath, []byte("foreign sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := atomicWriteInstallConfig(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("single replacement was blocked by unrelated rollback-shaped path: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "new\n" {
		t.Fatalf("config=%q err=%v", contents, err)
	}
	sentinel, err := os.ReadFile(rollbackPath)
	if err != nil || string(sentinel) != "foreign sentinel\n" {
		t.Fatalf("rollback-shaped foreign file changed: %q err=%v", sentinel, err)
	}
}

func TestEnsureInstallStateMarkerIsIdempotentAndNeverReplacesExisting(t *testing.T) {
	for _, test := range []struct {
		name      string
		contents  string
		wantError bool
	}{
		{name: "owned marker", contents: "codex-usage-state-v1\n"},
		{name: "foreign marker", contents: "foreign\n", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.paths.StateDir, ".codex-usage-state")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			err = ensureInstallStateMarker(fixture.paths)
			if test.wantError && err == nil {
				t.Fatal("foreign marker was accepted")
			}
			if !test.wantError && err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || string(contents) != test.contents {
				t.Fatalf("marker was replaced or changed: same=%v contents=%q", os.SameFile(before, after), contents)
			}
		})
	}
}

func TestInstallCreatesStateMarkerWhenConfigIsUnchangedAndScanSkipped(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	configBytes := writeCompleteInstallConfigWithoutMarker(t, fixture)
	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{})

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, calls)
	}
	markerPath := filepath.Join(fixture.paths.StateDir, ".codex-usage-state")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read install state marker: %v", err)
	}
	if string(marker) != "codex-usage-state-v1\n" {
		t.Fatalf("marker=%q", marker)
	}
	afterConfig, err := os.ReadFile(fixture.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterConfig, configBytes) {
		t.Fatalf("unchanged config was rewritten:\nwant=%q\ngot=%q", configBytes, afterConfig)
	}
}

func TestInstallRollsBackNewStateMarkerWhenLifecycleFails(t *testing.T) {
	for _, test := range []struct {
		name    string
		options installLifecycleFakeOptions
	}{
		{name: "install service fails", options: installLifecycleFakeOptions{installServiceErr: errors.New("synthetic install service failure")}},
		{name: "health check fails", options: installLifecycleFakeOptions{healthErr: errors.New("synthetic health failure")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			writeCompleteInstallConfigWithoutMarker(t, fixture)
			observed := false
			options := test.options
			options.beforeInstallService = func() {
				observed = installStateMarkerEquals(fixture.paths, installStateMarkerContent)
			}
			var calls []string
			fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, options)

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode == 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
			}
			if !observed {
				t.Fatalf("lifecycle did not observe the exact install marker: %q", stdout)
			}
			markerPath := filepath.Join(fixture.paths.StateDir, ".codex-usage-state")
			if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new marker remained after rollback: %v", err)
			}
		})
	}
}

func TestInstallRejectsUnsafeStateMarkerBeforeLifecycle(t *testing.T) {
	for _, test := range []struct {
		name     string
		setup    func(*testing.T, *installCommandFixture)
		wantCode string
	}{
		{
			name: "foreign content",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				if err := os.WriteFile(filepath.Join(fixture.paths.StateDir, ".codex-usage-state"), []byte("foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "config_write_failed",
		},
		{
			name: "symlink",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				target := filepath.Join(fixture.root, "outside-marker")
				if err := os.WriteFile(target, []byte("codex-usage-state-v1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(fixture.paths.StateDir, ".codex-usage-state")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			wantCode: "config_write_failed",
		},
		{
			name: "non-regular",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				if err := os.Mkdir(filepath.Join(fixture.paths.StateDir, ".codex-usage-state"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "config_write_failed",
		},
		{
			name: "linked state ancestor",
			setup: func(t *testing.T, fixture *installCommandFixture) {
				realState := filepath.Join(fixture.root, "real-state")
				if err := os.Rename(fixture.paths.StateDir, realState); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realState, fixture.paths.StateDir); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
			wantCode: "permission_required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			writeCompleteInstallConfigWithoutMarker(t, fixture)
			test.setup(t, fixture)
			var calls []string
			fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{})

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode == 0 || stderr != "" || len(calls) != 0 {
				t.Fatalf("exit=%d stderr=%q service_calls=%v stdout=%q", exitCode, stderr, calls, stdout)
			}
			terminal := installTerminalEvent(t, stdout)
			if terminal["code"] != test.wantCode {
				t.Fatalf("terminal=%#v", terminal)
			}
		})
	}
}

func TestInstallMarkerCreationFailureCleansOnlyOwnedExactFile(t *testing.T) {
	for _, test := range []struct {
		name          string
		contents      string
		creationError string
		wantMarker    bool
		wantRefusal   bool
	}{
		{name: "creation failure", creationError: "synthetic create failure"},
		{name: "partial write", contents: "codex-usage-state-", creationError: "synthetic write failure", wantMarker: true, wantRefusal: true},
		{name: "parent sync failure", contents: "codex-usage-state-v1\n", creationError: "synthetic parent sync failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			persistence := newInstallMarkerPersistence(fixture.paths)
			persistence.create = func(path string, _ []byte) (os.FileInfo, error) {
				if test.contents == "" {
					return nil, errors.New(test.creationError)
				}
				if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
					return nil, err
				}
				info, err := os.Lstat(path)
				if err != nil {
					return nil, err
				}
				return info, errors.New(test.creationError)
			}

			err := persistence.Prepare()
			if err == nil || !strings.Contains(err.Error(), test.creationError) {
				t.Fatalf("Prepare error=%v", err)
			}
			if test.wantRefusal != strings.Contains(strings.ToLower(err.Error()), "rollback refused") {
				t.Fatalf("Prepare error=%v want rollback refusal=%v", err, test.wantRefusal)
			}
			markerPath := filepath.Join(fixture.paths.StateDir, ".codex-usage-state")
			_, statErr := os.Lstat(markerPath)
			if test.wantMarker && statErr != nil {
				t.Fatalf("drifted partial marker was removed: %v", statErr)
			}
			if !test.wantMarker && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("owned exact marker remained after failed creation: %v", statErr)
			}
		})
	}
}

func writeCompleteInstallConfigWithoutMarker(t *testing.T, fixture *installCommandFixture) []byte {
	t.Helper()
	if err := os.MkdirAll(fixture.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ExtraCodexHomes = []string{filepath.Join(fixture.root, "codex-home")}
	data, err := marshalInstallConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.paths.ConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func installStateMarkerEquals(paths config.Paths, want string) bool {
	data, err := os.ReadFile(filepath.Join(paths.StateDir, ".codex-usage-state"))
	return err == nil && string(data) == want
}

func TestInstallRemovesNewConfigWhenLifecycleFails(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{
		healthErr: &identityProbeError{
			Code: "health_identity_mismatch", Field: "commit", Err: errors.New("synthetic health failure"),
		},
	})

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
	if exitCode == 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	if _, err := os.Lstat(fixture.paths.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new config remained after rollback: %v", err)
	}
}

func TestInstallScanUsesConfirmedHomes(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	confirmedHome := filepath.Join(fixture.root, "confirmed-home")
	rolloutDir := filepath.Join(confirmedHome, "sessions")
	if err := os.MkdirAll(rolloutDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := `{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"confirmed","cwd":"/p","originator":"codex_cli_rs"}}` + "\n"
	if err := os.WriteFile(filepath.Join(rolloutDir, "confirmed.jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	changedHome := filepath.Join(fixture.root, "changed-after-confirmation")
	t.Setenv("CODEX_HOME", changedHome)

	outcome, err := fixture.cli(io.Discard, io.Discard, strings.NewReader("")).
		installLifecycleOperations(fixture.paths, []string{confirmedHome}).Scan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Files != 1 || outcome.Result.Homes != 1 {
		t.Fatalf("scan result=%+v; confirmed home was not used", outcome.Result)
	}
}

func TestInstallConfirmationAndLifecycleUseSameLegacyHomes(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	previous, _ := seedPreviousInstallConfig(t, fixture)
	fixture.deps.InspectPrevious = defaultInspectPreviousState
	legacyConfigJSON, err := os.ReadFile(previous.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyConfigJSON = bytes.Replace(legacyConfigJSON, []byte(`"port": 43189`), []byte(`"port": 47655`), 1)
	if err := os.WriteFile(previous.ConfigPath, legacyConfigJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	legacyHome := filepath.Join(fixture.root, "legacy-extra-home")
	explicitHome := filepath.Join(fixture.root, "codex-home")
	wantHomes := []string{legacyHome, explicitHome}
	legacyConfig := strings.Join([]string{
		`model = "gpt-5"`,
		`# BEGIN codex-usage managed`,
		`log_user_prompt = false`,
		`# END codex-usage managed`,
		``,
	}, "\n")
	configPaths := []string{
		writeLegacyOTelFixture(t, legacyHome, legacyConfig),
		writeLegacyOTelFixture(t, explicitHome, legacyConfig),
	}
	for index, home := range wantHomes {
		rolloutDir := filepath.Join(home, "sessions")
		if err := os.MkdirAll(rolloutDir, 0o700); err != nil {
			t.Fatal(err)
		}
		rollout := fmt.Sprintf(
			`{"timestamp":"2026-07-30T01:00:00Z","type":"session_meta","payload":{"id":"confirmed-%d","cwd":"/p","originator":"codex_cli_rs"}}`+"\n",
			index,
		)
		if err := os.WriteFile(filepath.Join(rolloutDir, fmt.Sprintf("confirmed-%d.jsonl", index)), []byte(rollout), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	before := snapshotInstallFixtureTree(t, fixture.root)
	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--json")
	if exitCode == 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	terminal := installTerminalEvent(t, stdout)
	if terminal["code"] != "confirmation_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
	details, ok := terminal["result"].(map[string]any)
	if !ok {
		t.Fatalf("confirmation details=%#v", terminal["result"])
	}
	if got := stringSliceFromJSON(t, details["scan_scope"]); !samePathSet(got, wantHomes) {
		t.Fatalf("confirmation scan_scope=%v want=%v", got, wantHomes)
	}
	if details["dashboard_url"] != "http://127.0.0.1:47655" {
		t.Fatalf("confirmation dashboard_url=%#v", details["dashboard_url"])
	}
	if after := snapshotInstallFixtureTree(t, fixture.root); !reflect.DeepEqual(after, before) {
		t.Fatalf("unconfirmed install wrote files:\nbefore=%#v\nafter=%#v", before, after)
	}

	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{useRealScan: true})
	exitCode, stdout, stderr = runInstallJSON(t, fixture, "install", "--yes", "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q calls=%v", exitCode, stderr, stdout, calls)
	}
	receipt := installResultReceipt(t, stdout)
	if receipt["dashboard_url"] != details["dashboard_url"] {
		t.Fatalf("receipt dashboard_url=%#v confirmation=%#v", receipt["dashboard_url"], details["dashboard_url"])
	}
	scan, ok := receipt["scan"].(map[string]any)
	if !ok || scan["homes"] != float64(len(wantHomes)) || scan["files"] != float64(len(wantHomes)) {
		t.Fatalf("receipt scan=%#v want homes/files=%d", scan, len(wantHomes))
	}
	for _, path := range configPaths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte("codex-usage managed")) {
			t.Fatalf("legacy OTel marker remained in %s: %q", path, contents)
		}
	}
	if _, err := os.Lstat(previous.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous config remained after successful migration: %v", err)
	}
}

func TestInstallRejectsConfigPreviewChangeAfterConfirmation(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *installCommandFixture) (string, string, func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error))
	}{
		{
			name: "previous config changes",
			setup: func(t *testing.T, fixture *installCommandFixture) (string, string, func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error)) {
				previous, _ := seedPreviousInstallConfig(t, fixture)
				return previous.ConfigPath, filepath.Join(fixture.root, "legacy-extra-home"), defaultInspectPreviousState
			},
		},
		{
			name: "current config changes",
			setup: func(t *testing.T, fixture *installCommandFixture) (string, string, func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error)) {
				t.Setenv("CODEX_METER_HOME", filepath.Join(fixture.root, "absent-legacy-state"))
				oldHome := filepath.Join(fixture.root, "current-extra-home")
				seedInstallConfig(t, fixture.paths, []string{oldHome})
				return fixture.paths.ConfigPath, oldHome, func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
					return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, nil
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			configPath, oldHome, inspect := test.setup(t, fixture)
			newHome := filepath.Join(fixture.root, "changed-after-confirmation")
			inspectCalls := 0
			fixture.deps.InspectPrevious = func(paths config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error) {
				inspectCalls++
				if inspectCalls == 2 {
					contents, err := os.ReadFile(configPath)
					if err != nil {
						return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, err
					}
					updated := bytes.Replace(contents, []byte(strconv.Quote(oldHome)), []byte(strconv.Quote(newHome)), 1)
					if bytes.Equal(updated, contents) {
						return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, errors.New("test config home was not found")
					}
					if err := os.WriteFile(configPath, updated, 0o600); err != nil {
						return config.MigrationPlan{}, config.MigrationResult{}, platform.PreviousService{}, err
					}
				}
				return inspect(paths)
			}

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode == 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q inspectCalls=%d", exitCode, stderr, stdout, inspectCalls)
			}
			terminal := installTerminalEvent(t, stdout)
			if terminal["code"] != "preflight_changed" {
				t.Fatalf("terminal=%#v inspectCalls=%d", terminal, inspectCalls)
			}
			if fixture.runCalls != 0 {
				t.Fatalf("lifecycle ran %d times after preview drift", fixture.runCalls)
			}
		})
	}
}

func TestInstallConfigPersistenceFailureHasStableCode(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{err: &installConfigPersistenceError{err: errors.New("localized save failure")}, code: "config_write_failed"},
		{err: fmt.Errorf("install service: %w", &installConfigPersistenceError{err: errors.New("localized save failure")}), code: "config_write_failed"},
		{err: &installConfigPersistenceError{err: &installPreflightFailure{code: "permission_required", err: os.ErrPermission}}, code: "permission_required"},
		{err: &installConfigPersistenceError{err: &installPreflightFailure{code: "existing_install_untrusted", err: errors.New("linked install directory")}}, code: "existing_install_untrusted"},
		{err: &installConfigPersistenceError{err: &installPreflightFailure{code: "existing_install_untrusted", err: fmt.Errorf("install service: %w", os.ErrPermission)}}, code: "existing_install_untrusted"},
	} {
		classified := installCommandFailure(test.err, "install_failed")
		var coded *codedError
		if !errors.As(classified, &coded) {
			t.Fatalf("error=%T %v", classified, classified)
		}
		if coded.Code != test.code {
			t.Fatalf("code=%q want=%q error=%T %v", coded.Code, test.code, classified, classified)
		}
	}
}

func TestInstallServicePermissionFailureKeepsPermissionCode(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{
		installServiceErr: fmt.Errorf("service access denied: %w", os.ErrPermission),
	})

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
	if exitCode == 0 || stderr != "" || !containsString(calls, "install_service") {
		t.Fatalf("exit=%d stderr=%q calls=%v stdout=%q", exitCode, stderr, calls, stdout)
	}
	if terminal := installTerminalEvent(t, stdout); terminal["code"] != "permission_required" {
		t.Fatalf("terminal=%#v", terminal)
	}
}

func TestInstallLegacyOTelCleanupRunsOnlyAfterLifecycleSuccess(t *testing.T) {
	legacyConfig := strings.Join([]string{
		`model = "gpt-5"`,
		`[otel]`,
		`metrics_exporter = { otlp-http = { endpoint = "http://collector:4318", protocol = "binary" } }`,
		`# BEGIN codex-usage managed`,
		`log_user_prompt = false`,
		`# END codex-usage managed`,
		``,
	}, "\n")

	t.Run("success removes only managed marker block", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		path := writeLegacyOTelFixture(t, filepath.Join(fixture.root, "codex-home"), legacyConfig)
		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(after)
		if strings.Contains(text, "codex-usage managed") || strings.Contains(text, "log_user_prompt") {
			t.Fatalf("managed block remained: %q", text)
		}
		if !strings.Contains(text, "http://collector:4318") || !strings.Contains(text, `model = "gpt-5"`) {
			t.Fatalf("third-party configuration was changed: %q", text)
		}
	})

	t.Run("lifecycle failure leaves config byte-identical", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		path := writeLegacyOTelFixture(t, filepath.Join(fixture.root, "codex-home"), legacyConfig)
		fixture.deps.RunLifecycle = func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error) {
			return lifecycleResult{}, errors.New("synthetic lifecycle failure")
		}
		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode == 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != legacyConfig {
			t.Fatalf("failed lifecycle cleaned OTel config:\nwant=%q\ngot=%q", legacyConfig, after)
		}
	})
}

func TestInstallLegacyOTelCleanupFailureIsWarning(t *testing.T) {
	incomplete := "# BEGIN codex-usage managed\nlog_user_prompt = false\n"

	t.Run("json receipt", func(t *testing.T) {
		fixture := newInstallCommandFixture(t)
		writeLegacyOTelFixture(t, filepath.Join(fixture.root, "codex-home"), incomplete)
		exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
		}
		warnings := stringSliceFromJSON(t, installResultReceipt(t, stdout)["scan_warnings"])
		if !containsInstallWarningCode(warnings, "legacy_otel_cleanup_failed:") {
			t.Fatalf("warnings=%v", warnings)
		}
	})

	for _, test := range []struct {
		language string
		prefix   string
	}{
		{language: "zh-CN", prefix: "警告:"},
		{language: "en", prefix: "Warning:"},
	} {
		t.Run("human "+test.language, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			writeLegacyOTelFixture(t, filepath.Join(fixture.root, "codex-home"), incomplete)
			var stdout, stderr bytes.Buffer
			exitCode := fixture.cli(&stdout, &stderr, panicReader{}).Run([]string{
				"--lang", test.language, "install", "--yes", "--skip-scan",
			})
			if exitCode != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
			}
			if !strings.Contains(stderr.String(), test.prefix) ||
				!strings.Contains(stderr.String(), "legacy_otel_cleanup_failed:") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

func TestDefaultInspectPreviousStateOmitsServiceWithoutLegacyEvidence(t *testing.T) {
	for _, test := range []struct {
		name        string
		createEmpty bool
	}{
		{name: "absent"},
		{name: "empty directory", createEmpty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			legacyState := filepath.Join(fixture.root, "legacy-state")
			t.Setenv("CODEX_METER_HOME", legacyState)
			if test.createEmpty {
				if err := os.MkdirAll(legacyState, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			_, _, previousService, err := defaultInspectPreviousState(fixture.paths)
			if err != nil {
				t.Fatal(err)
			}
			if previousService != (platform.PreviousService{}) {
				t.Fatalf("phantom previous service=%+v", previousService)
			}
		})
	}
}

func TestDefaultInspectPreviousStateReturnsServiceWithMigrationEvidence(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	legacyState := filepath.Join(fixture.root, "legacy-state")
	t.Setenv("CODEX_METER_HOME", legacyState)
	if err := os.MkdirAll(legacyState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(legacyState, ".codex-meter-state"),
		[]byte("codex-meter-state-v1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	_, result, previousService, err := defaultInspectPreviousState(fixture.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || previousService == (platform.PreviousService{}) {
		t.Fatalf("result=%+v previous service=%+v", result, previousService)
	}
	if previousService.StateDir != legacyState {
		t.Fatalf("previous state dir=%q want=%q", previousService.StateDir, legacyState)
	}
}

func TestInstallMigratedConfigRollbackPreservesOriginalFile(t *testing.T) {
	for _, test := range []struct {
		name     string
		options  installLifecycleFakeOptions
		wantCode string
	}{
		{
			name: "install service failure",
			options: installLifecycleFakeOptions{
				installServiceErr: errors.New("synthetic migrated install service failure"),
			},
			wantCode: "service_start_failed",
		},
		{
			name: "health failure",
			options: installLifecycleFakeOptions{
				healthErr: &identityProbeError{
					Code: "health_identity_mismatch", Field: "commit", Err: errors.New("synthetic migrated health failure"),
				},
			},
			wantCode: "health_check_failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallCommandFixture(t)
			previous, original := seedPreviousInstallConfig(t, fixture)
			originalInfo, err := os.Lstat(previous.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			plan, result, previousService, err := defaultInspectPreviousState(fixture.paths)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Found || previousService == (platform.PreviousService{}) {
				t.Fatalf("plan=%+v result=%+v previousService=%+v", plan, result, previousService)
			}
			if _, err := os.Lstat(fixture.paths.ConfigPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("current config existed before lifecycle: %v", err)
			}

			fixture.deps.InspectPrevious = defaultInspectPreviousState
			var calls []string
			migrationObserved := false
			options := test.options
			options.beforeInstallService = func() {
				_, previousErr := os.Lstat(previous.ConfigPath)
				_, currentErr := os.Lstat(fixture.paths.ConfigPath)
				migrationObserved = errors.Is(previousErr, os.ErrNotExist) && currentErr == nil
			}
			fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, options)

			exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
			if exitCode == 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
			}
			terminal := installTerminalEvent(t, stdout)
			if terminal["code"] != test.wantCode {
				t.Fatalf("terminal=%#v", terminal)
			}
			message, _ := terminal["message"].(string)
			if strings.Contains(message, "rollback:") {
				t.Fatalf("rollback failed: %q", message)
			}
			if !migrationObserved {
				t.Fatalf("real migration rename was not observed; calls=%v", calls)
			}
			if !containsString(calls, "resume_previous") {
				t.Fatalf("previous service was not resumed: %v", calls)
			}
			after, err := os.ReadFile(previous.ConfigPath)
			if err != nil {
				t.Fatalf("read restored previous config: %v; calls=%v", err, calls)
			}
			if !bytes.Equal(after, original) || !bytes.Contains(after, []byte(`"third_party"`)) {
				t.Fatalf("previous config was not restored byte-identically:\nwant=%q\ngot=%q", original, after)
			}
			restoredInfo, err := os.Lstat(previous.ConfigPath)
			if err != nil || !os.SameFile(originalInfo, restoredInfo) {
				t.Fatalf("previous config identity changed: original=%v restored=%v err=%v", originalInfo, restoredInfo, err)
			}
			if _, err := os.Lstat(fixture.paths.ConfigPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("current config remained after rollback: %v", err)
			}
		})
	}
}

func TestInstallMigratedConfigSuccessPersistsExplicitHome(t *testing.T) {
	fixture := newInstallCommandFixture(t)
	previous, _ := seedPreviousInstallConfig(t, fixture)
	fixture.deps.InspectPrevious = defaultInspectPreviousState
	var calls []string
	fixture.deps.RunLifecycle = realInstallLifecycleWithFakeOptions(&calls, installLifecycleFakeOptions{})

	exitCode, stdout, stderr := runInstallJSON(t, fixture, "install", "--yes", "--json", "--skip-scan")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	cfg, err := config.Load(fixture.paths)
	if err != nil {
		t.Fatal(err)
	}
	wantHome := filepath.Join(fixture.root, "codex-home")
	if !samePathSet(cfg.ExtraCodexHomes, []string{filepath.Join(fixture.root, "legacy-extra-home"), wantHome}) {
		t.Fatalf("extra_codex_homes=%v", cfg.ExtraCodexHomes)
	}
	current, err := os.ReadFile(fixture.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(current, []byte(`"third_party"`)) || !bytes.Contains(current, []byte(`"byte-identical"`)) {
		t.Fatalf("migrated third-party config was lost: %q", current)
	}
	if _, err := os.Lstat(previous.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous config remained after commit: %v", err)
	}
	if !containsString(calls, "remove_previous") {
		t.Fatalf("previous service was not committed away: %v", calls)
	}
	if !installStateMarkerEquals(fixture.paths, installStateMarkerContent) {
		t.Fatal("migration did not leave the exact current install marker")
	}
}

func TestInstallConfigTemporaryPrefixRemainsDedicated(t *testing.T) {
	for _, test := range []struct {
		name      string
		fileName  func() string
		wantError bool
	}{
		{
			name: "managed interrupted write",
			fileName: func() string {
				return strings.TrimSuffix(installConfigTemporaryPattern, "*") + "interrupted"
			},
		},
		{
			name: "managed interrupted atomic replacement",
			fileName: func() string {
				return strings.TrimSuffix(installConfigAtomicTemporaryPattern, "*") + "interrupted"
			},
		},
		{
			name: "managed interrupted marker creation",
			fileName: func() string {
				return strings.TrimSuffix(installMarkerTemporaryPattern, "*") + "interrupted"
			},
		},
		{
			name:      "foreign prefix remains rejected",
			fileName:  func() string { return ".install-config-new-interrupted" },
			wantError: true,
		},
		{
			name:      "old restore prefix remains rejected",
			fileName:  func() string { return ".install-config-restore-interrupted" },
			wantError: true,
		},
		{
			name:      "old rollback path remains rejected",
			fileName:  func() string { return "config.json.install-config-rollback" },
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(stateDir, test.fileName()), []byte("interrupted"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_USAGE_HOME", stateDir)
			_, err := config.ResolvePaths()
			if test.wantError && err == nil {
				t.Fatal("foreign temporary prefix was accepted")
			}
			if !test.wantError && err != nil {
				t.Fatalf("managed interrupted temporary file blocked ResolvePaths: %v", err)
			}
		})
	}
}
