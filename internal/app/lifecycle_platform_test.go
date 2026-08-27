package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/usage"
)

func TestLifecycleUpgradeAndRollbackOnHostFilesystem(t *testing.T) {
	for _, failurePhase := range []string{"scan", "health"} {
		t.Run(failurePhase, func(t *testing.T) {
			harness := newHostFilesystemLifecycleHarness(t)
			harness.assertUpgradeRollback(failurePhase)
		})
	}
}

type hostFilesystemLifecycleHarness struct {
	t       *testing.T
	fixture *lifecycleFixture
	service *hostFilesystemService
}

type hostFilesystemService struct {
	t           *testing.T
	oldIdentity buildIdentity
	newIdentity buildIdentity
	running     bool
	identity    buildIdentity
	executable  string
}

func newHostFilesystemLifecycleHarness(t *testing.T) *hostFilesystemLifecycleHarness {
	t.Helper()
	fixture := newLifecycleFixture(t, "2.3.5", "old-host-binary")
	newIdentity := fixture.identity
	newIdentity.Version = "2.3.6"
	return &hostFilesystemLifecycleHarness{
		t:       t,
		fixture: fixture,
		service: &hostFilesystemService{
			t:           t,
			oldIdentity: fixture.identity,
			newIdentity: newIdentity,
		},
	}
}

func (h *hostFilesystemLifecycleHarness) assertUpgradeRollback(failurePhase string) {
	h.t.Helper()
	if failurePhase != "scan" && failurePhase != "health" {
		h.t.Fatalf("unsupported failure phase %q", failurePhase)
	}

	freshOps := h.operations("", nil)
	result, err := executeLifecycle(context.Background(), h.fixture.request(), freshOps, nil)
	if err != nil {
		h.t.Fatalf("install old host identity: %v", err)
	}
	if result.Decision != install.DecisionFresh || !h.service.running || h.service.identity != h.fixture.identity {
		h.t.Fatalf("old host install did not become healthy: result=%+v service=%+v", result, h.service)
	}

	oldBytes, err := os.ReadFile(h.fixture.destinationPath)
	if err != nil {
		h.t.Fatal(err)
	}
	oldDigest, err := install.FileSHA256(h.fixture.destinationPath)
	if err != nil {
		h.t.Fatal(err)
	}
	oldRecord := *mustLoadInstallRecord(h.t, h.fixture.installRecord)

	if err := os.WriteFile(h.fixture.candidatePath, []byte("new-host-binary"), 0o700); err != nil {
		h.t.Fatal(err)
	}
	h.fixture.identity = h.service.newIdentity
	injected := errors.New("injected post-activate " + failurePhase + " failure")
	result, err = executeLifecycle(
		context.Background(),
		h.fixture.request(),
		h.operations(failurePhase, injected),
		nil,
	)
	if !errors.Is(err, injected) || result.Decision != install.DecisionUpgrade {
		h.t.Fatalf("upgrade %s failure result=%+v err=%v", failurePhase, result, err)
	}

	currentBytes, err := os.ReadFile(h.fixture.destinationPath)
	if err != nil {
		h.t.Fatal(err)
	}
	if !reflect.DeepEqual(currentBytes, oldBytes) {
		h.t.Fatalf("old executable bytes were not restored: got=%q want=%q", currentBytes, oldBytes)
	}
	currentDigest, err := install.FileSHA256(h.fixture.destinationPath)
	if err != nil {
		h.t.Fatal(err)
	}
	if currentDigest != oldDigest {
		h.t.Fatalf("old executable digest was not restored: got=%s want=%s", currentDigest, oldDigest)
	}
	if currentRecord := mustLoadInstallRecord(h.t, h.fixture.installRecord); !reflect.DeepEqual(*currentRecord, oldRecord) {
		h.t.Fatalf("old install record was not restored:\ngot  %+v\nwant %+v", *currentRecord, oldRecord)
	}
	if !h.service.running || h.service.identity != h.service.oldIdentity || h.service.executable != h.fixture.destinationPath {
		h.t.Fatalf("old service was not restored: %+v", h.service)
	}
	for _, recoveryPath := range []string{h.fixture.destinationPath + ".stage", h.fixture.destinationPath + ".backup"} {
		if _, statErr := os.Lstat(recoveryPath); !errors.Is(statErr, os.ErrNotExist) {
			h.t.Fatalf("rollback left recovery path %s: %v", recoveryPath, statErr)
		}
	}
}

func (h *hostFilesystemLifecycleHarness) operations(failurePhase string, injected error) lifecycleOps {
	return lifecycleOps{
		StopService: func(executable, _ string) error {
			if executable != h.fixture.destinationPath || !h.service.running {
				return errors.New("old host service is not running at the expected executable")
			}
			h.service.running = false
			return nil
		},
		InstallService: h.service.install,
		UninstallService: func(string, string) error {
			h.service.running = false
			return nil
		},
		SuspendPrevious: func(platform.PreviousService) error { return nil },
		ResumePrevious:  func(platform.PreviousService) error { return nil },
		RemovePrevious:  func(platform.PreviousService) error { return nil },
		ProbeIdentity: func(_ context.Context, _ string, expected buildIdentity) error {
			if !h.service.running || h.service.identity != expected {
				return errors.New("fake host service identity mismatch")
			}
			if failurePhase == "health" && expected == h.service.newIdentity {
				return injected
			}
			return nil
		},
		Scan: func(context.Context, usage.ProgressObserver) (installScanOutcome, error) {
			active, err := os.ReadFile(h.fixture.destinationPath)
			if err != nil {
				return installScanOutcome{}, err
			}
			if failurePhase == "scan" && string(active) == "new-host-binary" {
				return installScanOutcome{}, injected
			}
			return installScanOutcome{Result: usage.ScanResult{Files: 1, EventsInserted: 1}}, nil
		},
		Now: func() time.Time { return lifecycleTestNow },
	}
}

func (s *hostFilesystemService) install(executable, _ string) (platform.ServiceResult, error) {
	s.t.Helper()
	contents, err := os.ReadFile(executable)
	if err != nil {
		return platform.ServiceResult{}, err
	}
	switch string(contents) {
	case "old-host-binary":
		s.identity = s.oldIdentity
	case "new-host-binary":
		s.identity = s.newIdentity
	default:
		return platform.ServiceResult{}, errors.New("fake host service received unknown executable bytes")
	}
	s.executable = executable
	s.running = true
	return platform.ServiceResult{
		Installed: true,
		Started:   true,
		Mode:      platform.ServiceModePersistent,
	}, nil
}
