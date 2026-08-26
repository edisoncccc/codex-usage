package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zJay26/codex-usage/internal/store"
)

func TestScannerObserverReportsDiscoveredAndProcessedFiles(t *testing.T) {
	scanner, homes := newProgressScannerFixture(t, [][]string{
		{progressRollout("session-a", 100)},
		{progressRollout("session-b", 200)},
	})
	var snapshots []ScanProgress

	result, err := scanner.ScanWithProgress(context.Background(), homes, false, func(progress ScanProgress) {
		snapshots = append(snapshots, progress)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Homes != 2 || result.Files != 2 || result.Records != 4 || result.EventsInserted != 2 {
		t.Fatalf("unexpected scan result: %+v", result)
	}
	if len(snapshots) == 0 {
		t.Fatal("observer received no progress snapshots")
	}
	wantFinal := ScanProgress{
		HomesTotal:       2,
		HomesDiscovered:  2,
		FilesDiscovered:  2,
		FilesProcessed:   2,
		RecordsProcessed: 4,
		EventsInserted:   2,
	}
	if got := snapshots[len(snapshots)-1]; got != wantFinal {
		t.Fatalf("unexpected final snapshot: want=%+v got=%+v", wantFinal, got)
	}
	foundDiscoveredBeforeProcessed := false
	for _, snapshot := range snapshots {
		if snapshot.FilesDiscovered > snapshot.FilesProcessed {
			foundDiscoveredBeforeProcessed = true
			break
		}
	}
	if !foundDiscoveredBeforeProcessed {
		t.Fatalf("file discovery was not observable before processing: %+v", snapshots)
	}
}

func TestScannerObserverProgressIsMonotonic(t *testing.T) {
	scanner, homes := newProgressScannerFixture(t, [][]string{
		{progressRollout("session-a", 100), progressRollout("session-b", 200)},
	})
	var snapshots []ScanProgress

	if _, err := scanner.ScanWithProgress(context.Background(), homes, false, func(progress ScanProgress) {
		snapshots = append(snapshots, progress)
	}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("want multiple progress snapshots, got %+v", snapshots)
	}
	for index := 1; index < len(snapshots); index++ {
		previous := snapshots[index-1]
		current := snapshots[index]
		if current.HomesTotal < previous.HomesTotal ||
			current.HomesDiscovered < previous.HomesDiscovered ||
			current.FilesDiscovered < previous.FilesDiscovered ||
			current.FilesProcessed < previous.FilesProcessed ||
			current.RecordsProcessed < previous.RecordsProcessed ||
			current.EventsInserted < previous.EventsInserted ||
			current.Warnings < previous.Warnings {
			t.Fatalf("snapshot %d regressed: previous=%+v current=%+v", index, previous, current)
		}
	}
}

func TestScannerObserverReportsInsertedEventsAndWarnings(t *testing.T) {
	content := strings.Join([]string{
		`{"timestamp":"2026-08-26T01:00:00Z","type":"session_meta","payload":{"id":"warning-session","cwd":"/project"}}`,
		progressTokenLine("2026-08-26T01:00:01Z", 100),
		`{"timestamp":"2026-08-26T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":BROKEN}}`,
	}, "\n") + "\n"
	scanner, homes := newProgressScannerFixture(t, [][]string{{content}})
	var snapshots []ScanProgress

	result, err := scanner.ScanWithProgress(context.Background(), homes, false, func(progress ScanProgress) {
		snapshots = append(snapshots, progress)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsInserted != 1 || result.Warnings != 1 || result.Records != 3 {
		t.Fatalf("unexpected scan result: %+v", result)
	}
	foundInsertedBeforeFileCompletion := false
	foundWarningBeforeFileCompletion := false
	for _, snapshot := range snapshots {
		foundInsertedBeforeFileCompletion = foundInsertedBeforeFileCompletion ||
			snapshot.EventsInserted == 1 && snapshot.FilesProcessed == 0
		foundWarningBeforeFileCompletion = foundWarningBeforeFileCompletion ||
			snapshot.Warnings == 1 && snapshot.FilesProcessed == 0
	}
	if !foundInsertedBeforeFileCompletion || !foundWarningBeforeFileCompletion {
		t.Fatalf("event and warning progress were not observable during file processing: %+v", snapshots)
	}
	final := snapshots[len(snapshots)-1]
	if final.EventsInserted != result.EventsInserted || final.Warnings != result.Warnings ||
		final.RecordsProcessed != result.Records {
		t.Fatalf("final progress did not match scan result: progress=%+v result=%+v", final, result)
	}
}

func TestScanWithoutObserverRemainsCompatible(t *testing.T) {
	legacyScanner, legacyHomes := newProgressScannerFixture(t, [][]string{
		{progressRollout("session-a", 100), progressRollout("session-b", 200)},
	})
	progressScanner, progressHomes := newProgressScannerFixture(t, [][]string{
		{progressRollout("session-a", 100), progressRollout("session-b", 200)},
	})

	legacy, legacyErr := legacyScanner.Scan(context.Background(), legacyHomes, false)
	withoutObserver, progressErr := progressScanner.ScanWithProgress(context.Background(), progressHomes, false, nil)
	if legacyErr != nil || progressErr != nil {
		t.Fatalf("scan errors differ: legacy=%v progress=%v", legacyErr, progressErr)
	}
	legacy.ElapsedMillis = 0
	withoutObserver.ElapsedMillis = 0
	if !reflect.DeepEqual(legacy, withoutObserver) {
		t.Fatalf("nil observer changed scan result: legacy=%+v progress=%+v", legacy, withoutObserver)
	}
}

func newProgressScannerFixture(t *testing.T, homeFiles [][]string) (*Scanner, []string) {
	t.Helper()
	root := t.TempDir()
	homes := make([]string, 0, len(homeFiles))
	for homeIndex, files := range homeFiles {
		home := filepath.Join(root, fmt.Sprintf("home-%d", homeIndex))
		sessions := filepath.Join(home, "sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		for fileIndex, content := range files {
			path := filepath.Join(sessions, fmt.Sprintf("rollout-%d.jsonl", fileIndex))
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		homes = append(homes, home)
	}
	st, err := store.Open(filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return &Scanner{Store: st}, homes
}

func progressRollout(sessionID string, total int64) string {
	return strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-08-26T01:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":"/project"}}`, sessionID),
		progressTokenLine("2026-08-26T01:00:01Z", total),
	}, "\n") + "\n"
}

func progressTokenLine(timestamp string, total int64) string {
	input := total * 4 / 5
	output := total - input
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}}}}`,
		timestamp, input, output, total, input, output, total)
}
