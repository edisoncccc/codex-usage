package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestAssessFreshInstall(t *testing.T) {
	root := t.TempDir()
	candidatePath := writeFixture(t, root, "candidate", "fresh candidate")
	decision, err := Assess(
		filepath.Join(root, "state", "install.json"),
		filepath.Join(root, "bin", executableName()),
		Candidate{Version: "2.3.6", ExecutablePath: candidatePath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionFresh {
		t.Fatalf("decision=%q want=%q", decision, DecisionFresh)
	}
}

func TestAssessDigestUsesAlreadyBoundCandidateBytes(t *testing.T) {
	root := t.TempDir()
	candidatePath := writeFixture(t, root, "candidate", "bound candidate")
	boundDigest := mustFileSHA256(t, candidatePath)
	if err := os.WriteFile(candidatePath, []byte("later path contents"), 0o700); err != nil {
		t.Fatal(err)
	}

	decision, err := AssessDigest(
		filepath.Join(root, "state", "install.json"),
		filepath.Join(root, "bin", executableName()),
		Candidate{Version: "2.3.6", ExecutablePath: candidatePath},
		boundDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionFresh {
		t.Fatalf("decision=%q want=%q", decision, DecisionFresh)
	}
}

func TestAssessDigestRejectsInvalidBoundDigest(t *testing.T) {
	root := t.TempDir()
	candidatePath := filepath.Join(root, "candidate")
	for _, digest := range []string{"", strings.Repeat("0", 64), strings.Repeat("A", 64), "not-a-digest"} {
		if _, err := AssessDigest(
			filepath.Join(root, "state", "install.json"),
			filepath.Join(root, "bin", executableName()),
			Candidate{Version: "2.3.6", ExecutablePath: candidatePath},
			digest,
		); err == nil {
			t.Fatalf("digest=%q was accepted", digest)
		}
	}
}

func TestAssessSameBinaryIsIdempotent(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "same binary")
	digest := mustFileSHA256(t, destination)
	recordPath := filepath.Join(root, "state", "install.json")
	mustSave(t, recordPath, validRecord(destination, "2.3.6", digest))
	candidate := writeFixture(t, root, "candidate", "same binary")

	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.3.6", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionSame {
		t.Fatalf("decision=%q want=%q", decision, DecisionSame)
	}
}

func TestAssessTrustedOlderVersionIsUpgrade(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "state", "install.json")
	mustSave(t, recordPath, validRecord(destination, "2.3.5", mustFileSHA256(t, destination)))
	candidate := writeFixture(t, root, "candidate", "new binary")

	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.3.6", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUpgrade {
		t.Fatalf("decision=%q want=%q", decision, DecisionUpgrade)
	}
}

func TestAssessRejectsDowngrade(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "state", "install.json")
	mustSave(t, recordPath, validRecord(destination, "2.3.6", mustFileSHA256(t, destination)))
	candidate := writeFixture(t, root, "candidate", "older binary")

	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.3.5", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionDowngrade {
		t.Fatalf("decision=%q want=%q", decision, DecisionDowngrade)
	}
}

func TestAssessRejectsUnrecordedExistingExecutable(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "unknown binary")
	candidate := writeFixture(t, root, "candidate", "candidate")

	decision, err := Assess(
		filepath.Join(root, "state", "install.json"),
		destination,
		Candidate{Version: "2.3.6", ExecutablePath: candidate},
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUntrusted {
		t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
	}
}

func TestAssessRejectsDigestMismatch(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "tampered binary")
	recordPath := filepath.Join(root, "state", "install.json")
	expected := writeFixture(t, root, "expected", "expected binary")
	mustSave(t, recordPath, validRecord(destination, "2.3.6", mustFileSHA256(t, expected)))

	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.3.6", ExecutablePath: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUntrusted {
		t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
	}
}

func TestInstallRecordRoundTripIsAtomic(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "state", "install.json")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))

	if err := Save(recordPath, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !reflect.DeepEqual(*loaded, record) {
		t.Fatalf("loaded=%#v want=%#v", loaded, record)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || strings.Contains(string(data), "stale") {
		t.Fatalf("record is not a stable replacement: %q", data)
	}
	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%#o want=0600", info.Mode().Perm())
	}
	temporaryRecords, err := filepath.Glob(filepath.Join(filepath.Dir(recordPath), ".install-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryRecords) != 0 {
		t.Fatalf("successful save left temporary records: %v", temporaryRecords)
	}
}

func TestLoadRejectsUnknownDuplicateTrailingAndMissingFields(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	canonical, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data string
	}{
		{name: "top-level array", data: `[]`},
		{name: "unknown", data: strings.Replace(string(canonical), `"product":"codex-usage"`, `"unknown":true,"product":"codex-usage"`, 1)},
		{name: "duplicate", data: strings.Replace(string(canonical), `"product":"codex-usage"`, `"product":"codex-usage","product":"codex-usage"`, 1)},
		{name: "unicode duplicate", data: strings.Replace(string(canonical), `"product":"codex-usage"`, `"product":"codex-usage","\u0070roduct":"codex-usage"`, 1)},
		{name: "trailing", data: string(canonical) + ` {}`},
		{name: "truncated", data: string(canonical[:len(canonical)-1])},
		{name: "missing safety field", data: strings.Replace(string(canonical), `,"source":"source_build"`, "", 1)},
		{name: "null safety field", data: strings.Replace(string(canonical), `"dirty":false`, `"dirty":null`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("Load error=%v, want path-qualified rejection", err)
			}
		})
	}
}

func TestRecordValidationRejectsUnsafeMetadata(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	valid := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	nonHostOS := "windows"
	if runtime.GOOS == "windows" {
		nonHostOS = "linux"
	}
	nonHostArch := "amd64"
	if runtime.GOARCH == "amd64" {
		nonHostArch = "arm64"
	}
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "schema", mutate: func(record *Record) { record.SchemaVersion = "2" }},
		{name: "product", mutate: func(record *Record) { record.Product = "lookalike" }},
		{name: "source", mutate: func(record *Record) { record.Source = "download" }},
		{name: "os", mutate: func(record *Record) { record.OS = "darwin" }},
		{name: "other supported os", mutate: func(record *Record) { record.OS = nonHostOS }},
		{name: "arch", mutate: func(record *Record) { record.Arch = "386" }},
		{name: "other supported arch", mutate: func(record *Record) { record.Arch = nonHostArch }},
		{name: "relative executable", mutate: func(record *Record) { record.ExecutablePath = executableName() }},
		{name: "digest", mutate: func(record *Record) { record.ExecutableSHA256 = strings.ToUpper(record.ExecutableSHA256) }},
		{name: "zero digest", mutate: func(record *Record) { record.ExecutableSHA256 = strings.Repeat("0", 64) }},
		{name: "build date", mutate: func(record *Record) { record.BuildDate = "not-a-date" }},
		{name: "zero build date", mutate: func(record *Record) { record.BuildDate = "0001-01-01T00:00:00Z" }},
		{name: "installed at", mutate: func(record *Record) { record.InstalledAt = "yesterday" }},
		{name: "zero installed at", mutate: func(record *Record) { record.InstalledAt = "0001-01-01T00:00:00Z" }},
		{name: "clean dev source", mutate: func(record *Record) { record.Commit = "dev"; record.Dirty = false }},
		{name: "zero commit", mutate: func(record *Record) { record.Commit = strings.Repeat("0", 40) }},
		{name: "dirty trusted release", mutate: func(record *Record) { record.Source = "trusted_release"; record.Dirty = true }},
		{name: "untraceable trusted release", mutate: func(record *Record) { record.Source = "trusted_release"; record.Commit = "dev" }},
		{name: "unknown trusted build date", mutate: func(record *Record) { record.Source = "trusted_release"; record.BuildDate = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			path := filepath.Join(root, test.name, "install.json")
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected unsafe record rejection")
			}
		})
	}
}

func TestAssessRejectsInvalidCandidateMetadata(t *testing.T) {
	root := t.TempDir()
	candidatePath := writeFixture(t, root, "candidate", "candidate")
	tests := []Candidate{
		{Version: "v2.3.6", ExecutablePath: candidatePath},
		{Version: "2.3.6-beta", ExecutablePath: candidatePath},
		{Version: "2..6", ExecutablePath: candidatePath},
		{Version: "2.a.6", ExecutablePath: candidatePath},
		{Version: "+2.3.6", ExecutablePath: candidatePath},
		{Version: "02.3.6", ExecutablePath: candidatePath},
		{Version: "18446744073709551616.3.6", ExecutablePath: candidatePath},
		{Version: "2.3.6", ExecutablePath: "relative-candidate"},
	}
	for _, candidate := range tests {
		if decision, err := Assess(
			filepath.Join(root, "state", "install.json"),
			filepath.Join(root, "bin", executableName()),
			candidate,
		); err == nil || decision != "" {
			t.Fatalf("candidate=%#v decision=%q err=%v", candidate, decision, err)
		}
	}
}

func TestAssessComparesVersionsNumerically(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "state", "install.json")
	mustSave(t, recordPath, validRecord(destination, "2.10.0", mustFileSHA256(t, destination)))
	candidate := writeFixture(t, root, "candidate", "candidate")
	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.9.0", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionDowngrade {
		t.Fatalf("decision=%q want=%q", decision, DecisionDowngrade)
	}
}

func TestAssessRejectsStateMismatch(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	digest := mustFileSHA256(t, destination)
	candidatePath := writeFixture(t, root, "candidate", "installed binary")
	candidate := Candidate{Version: "2.3.6", ExecutablePath: candidatePath}
	tests := []struct {
		name        string
		record      Record
		destination string
		candidate   Candidate
	}{
		{
			name:        "recorded executable is missing",
			record:      validRecord(filepath.Join(root, "missing", executableName()), "2.3.6", digest),
			destination: filepath.Join(root, "missing", executableName()),
			candidate:   candidate,
		},
		{
			name:        "recorded path differs",
			record:      validRecord(filepath.Join(root, "other", executableName()), "2.3.6", digest),
			destination: destination,
			candidate:   candidate,
		},
		{
			name:        "same version candidate differs",
			record:      validRecord(destination, "2.3.6", digest),
			destination: destination,
			candidate:   Candidate{Version: "2.3.6", ExecutablePath: writeFixture(t, root, "different", "different")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recordPath := filepath.Join(root, test.name, "install.json")
			mustSave(t, recordPath, test.record)
			decision, err := Assess(recordPath, test.destination, test.candidate)
			if err != nil {
				t.Fatal(err)
			}
			if decision != DecisionUntrusted {
				t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
			}
		})
	}
}

func TestAssessRejectsDanglingFinalSymlinkWithoutRecord(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "bin", executableName())
	mustSymlinkOrSkip(t, filepath.Join(root, "missing-target"), destination)
	candidate := writeFixture(t, root, "candidate", "candidate")
	decision, err := Assess(filepath.Join(root, "state", "install.json"), destination, Candidate{
		Version: "2.3.6", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUntrusted {
		t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
	}
}

func TestAssessRejectsLiveFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := writeFixture(t, root, "actual", "installed binary")
	destination := filepath.Join(root, "bin", executableName())
	mustSymlinkOrSkip(t, target, destination)
	candidate := writeFixture(t, root, "candidate", "installed binary")
	decision, err := Assess(filepath.Join(root, "state", "install.json"), destination, Candidate{
		Version: "2.3.6", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUntrusted {
		t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
	}
}

func TestAssessRejectsRecordedFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := writeFixture(t, root, "actual", "installed binary")
	destination := filepath.Join(root, "bin", executableName())
	mustSymlinkOrSkip(t, target, destination)
	recordPath := filepath.Join(root, "state", "install.json")
	mustSave(t, recordPath, validRecord(destination, "2.3.6", mustFileSHA256(t, target)))
	candidate := writeFixture(t, root, "candidate", "installed binary")
	decision, err := Assess(recordPath, destination, Candidate{
		Version: "2.3.6", ExecutablePath: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != DecisionUntrusted {
		t.Fatalf("decision=%q want=%q", decision, DecisionUntrusted)
	}
}

func TestAssessRejectsNonRegularCandidate(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "bin", executableName())
	candidates := []string{filepath.Join(root, "candidate-dir")}
	if err := os.MkdirAll(candidates[0], 0o700); err != nil {
		t.Fatal(err)
	}
	target := writeFixture(t, root, "candidate-target", "candidate")
	symlink := filepath.Join(root, "candidate-link")
	if err := os.Symlink(target, symlink); err == nil {
		candidates = append(candidates, symlink)
	}
	for _, candidate := range candidates {
		if decision, err := Assess(
			filepath.Join(root, "state", "install.json"), destination,
			Candidate{Version: "2.3.6", ExecutablePath: candidate},
		); err == nil || decision != "" {
			t.Fatalf("candidate=%q decision=%q err=%v", candidate, decision, err)
		}
	}
}

func TestSaveFailurePreservesPreviousRecord(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "state", "install.json")
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	mustSave(t, recordPath, record)
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := record
	invalid.Product = "lookalike"
	if err := Save(recordPath, invalid); err == nil {
		t.Fatal("expected invalid replacement to fail")
	}
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed save changed previous record:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestSourceBuildMayRecordDirtyDevelopmentIdentity(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	record.Commit = "dev"
	record.Dirty = true
	record.BuildDate = "unknown"
	recordPath := filepath.Join(root, "state", "install.json")
	if err := Save(recordPath, record); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(recordPath); err != nil || loaded == nil || !loaded.Dirty || loaded.Commit != "dev" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestLoadAndSaveRejectRecordSymlink(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	target := filepath.Join(root, "actual-install.json")
	mustSave(t, target, record)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state", "install.json")
	mustSymlinkOrSkip(t, target, link)
	if _, err := Load(link); err == nil {
		t.Fatal("Load accepted a symlinked install record")
	}
	newRecord := record
	newRecord.Version = "2.3.7"
	if err := Save(link, newRecord); err == nil {
		t.Fatal("Save accepted a symlinked install record")
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected symlink save changed the symlink target")
	}
}

func TestLoadAndSaveRejectNonRegularRecord(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	record := validRecord(executable, "2.3.6", mustFileSHA256(t, executable))
	recordPath := filepath.Join(root, "install.json")
	if err := os.Mkdir(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(recordPath); err == nil {
		t.Fatal("Load accepted a directory as an install record")
	}
	if err := Save(recordPath, record); err == nil {
		t.Fatal("Save accepted a directory as an install record")
	}
}

func TestLoadRejectsGroupOrWorldWritableRecordOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not authoritative on Windows")
	}
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	recordPath := filepath.Join(root, "install.json")
	mustSave(t, recordPath, validRecord(executable, "2.3.6", mustFileSHA256(t, executable)))
	if err := os.Chmod(recordPath, 0o622); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(recordPath); err == nil {
		t.Fatal("Load accepted a group/world-writable install record")
	}
}

func TestReplaceInstallRecordRejectsCrossDirectoryAndPreservesDestination(t *testing.T) {
	root := t.TempDir()
	destination := writeFixture(t, filepath.Join(root, "state"), "install.json", "old\n")
	temporary := writeFixture(t, filepath.Join(root, "other"), "install.tmp", "new\n")
	if err := replaceInstallRecord(temporary, destination); err == nil {
		t.Fatal("cross-directory replacement was accepted")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("failed replacement changed destination: %q", data)
	}
}

func TestConcurrentReadersObserveOnlyCompleteInstallRecords(t *testing.T) {
	root := t.TempDir()
	executable := writeFixture(t, filepath.Join(root, "bin"), executableName(), "installed binary")
	digest := mustFileSHA256(t, executable)
	recordPath := filepath.Join(root, "state", "install.json")
	oldRecord := validRecord(executable, "2.3.6", digest)
	newRecord := validRecord(executable, "2.3.7", digest)
	newRecord.InstalledAt = "2026-08-26T01:02:04Z"
	mustSave(t, recordPath, oldRecord)

	stop := make(chan struct{})
	readErrors := make(chan error, 1)
	var readers sync.WaitGroup
	var observations atomic.Int64
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			record, err := Load(recordPath)
			if err != nil {
				if transientWindowsSharingError(err) {
					continue
				}
				select {
				case readErrors <- fmt.Errorf("concurrent Load: %w", err):
				default:
				}
				return
			}
			if record == nil || (!reflect.DeepEqual(*record, oldRecord) && !reflect.DeepEqual(*record, newRecord)) {
				select {
				case readErrors <- fmt.Errorf("observed partial or unexpected record: %#v", record):
				default:
				}
				return
			}
			observations.Add(1)
		}
	}()
	for index := 0; index < 40; index++ {
		record := oldRecord
		if index%2 == 0 {
			record = newRecord
		}
		if err := Save(recordPath, record); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case err := <-readErrors:
		t.Fatal(err)
	default:
	}
	if observations.Load() == 0 {
		t.Fatal("concurrent reader did not observe any complete record")
	}
}

func validRecord(executablePath, version, digest string) Record {
	return Record{
		SchemaVersion:    RecordSchemaVersion,
		Product:          ProductName,
		Version:          version,
		Commit:           "0123456789abcdef0123456789abcdef01234567",
		Dirty:            false,
		BuildDate:        "2026-08-26T00:00:00Z",
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		ExecutablePath:   executablePath,
		ExecutableSHA256: digest,
		Source:           SourceBuild,
		InstalledAt:      time.Date(2026, time.August, 26, 1, 2, 3, 0, time.UTC).Format(time.RFC3339),
	}
}

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustFileSHA256(t *testing.T, path string) string {
	t.Helper()
	digest, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func mustSave(t *testing.T, path string, record Record) {
	t.Helper()
	if err := Save(path, record); err != nil {
		t.Fatal(err)
	}
}

func executableName() string {
	if runtime.GOOS == "windows" {
		return "codex-usage.exe"
	}
	return "codex-usage"
}

func mustSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink is unavailable on this host: %v", err)
	}
}

func transientWindowsSharingError(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == 32 || errno == 33)
}
