package app

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/usage"
)

func TestHeartbeatStopsWithoutWritingAfterCompletion(t *testing.T) {
	var writes atomic.Int64
	tracker, err := startScanProgressTracker(
		usage.ScanProgress{HomesTotal: 1},
		10*time.Millisecond,
		func(usage.ScanProgress) error {
			writes.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tracker.Update(usage.ScanProgress{HomesTotal: 1, HomesDiscovered: 1})

	deadline := time.After(500 * time.Millisecond)
	for writes.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("heartbeat did not fire: writes=%d", writes.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := tracker.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Stop(); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
	completedWrites := writes.Load()
	time.Sleep(35 * time.Millisecond)
	if got := writes.Load(); got != completedWrites {
		t.Fatalf("heartbeat wrote after Stop returned: before=%d after=%d", completedWrites, got)
	}
}
