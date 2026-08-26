package app

import (
	"errors"
	"sync"
	"time"

	"github.com/zJay26/codex-usage/internal/usage"
)

type scanProgressTracker struct {
	mu       sync.Mutex
	latest   usage.ScanProgress
	emit     func(usage.ScanProgress) error
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	err      error
}

func startScanProgressTracker(
	initial usage.ScanProgress,
	interval time.Duration,
	emit func(usage.ScanProgress) error,
) (*scanProgressTracker, error) {
	if emit == nil {
		return nil, errors.New("scan progress emitter is nil")
	}
	if interval <= 0 {
		interval = 4 * time.Second
	}
	if err := emit(initial); err != nil {
		return nil, err
	}
	tracker := &scanProgressTracker{
		latest: initial,
		emit:   emit,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go tracker.run(interval)
	return tracker, nil
}

func (t *scanProgressTracker) Update(progress usage.ScanProgress) {
	t.mu.Lock()
	t.latest = progress
	t.mu.Unlock()
}

func (t *scanProgressTracker) Stop() error {
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *scanProgressTracker) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(t.done)
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.mu.Lock()
			latest := t.latest
			t.mu.Unlock()
			if err := t.emit(latest); err != nil {
				t.mu.Lock()
				t.err = err
				t.mu.Unlock()
				return
			}
		}
	}
}
