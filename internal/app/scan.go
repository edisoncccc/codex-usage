package app

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/zJay26/codex-usage/internal/usage"
)

type scanRunner interface {
	ScanWithProgress(context.Context, []string, bool, usage.ProgressObserver) (usage.ScanResult, error)
}

type scanRuntime struct {
	scanner scanRunner
	homes   []string
	close   func() error
}

type scanCommandResult struct {
	Scan usage.ScanResult `json:"scan"`
}

func defaultOpenScanState() (*scanRuntime, error) {
	state, err := openState()
	if err != nil {
		return nil, err
	}
	return &scanRuntime{
		scanner: state.scanner,
		homes:   state.homes,
		close:   state.store.Close,
	}, nil
}

func (s *scanRuntime) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

func (c CLI) scanCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	rebuild := flags.Bool("rebuild", false, c.tr("flag.rebuild"))
	if err := flags.Parse(args); err != nil {
		return commandResult{}, c.scanInvalidArguments(err)
	}
	if flags.NArg() != 0 {
		return commandResult{}, c.scanInvalidArguments(errors.New("unexpected positional arguments"))
	}

	state, err := c.openScanState()
	if err != nil {
		return commandResult{}, scanFailure(err)
	}
	if state == nil || state.scanner == nil {
		if state != nil {
			_ = state.Close()
		}
		return commandResult{}, scanFailure(errors.New("scan runtime is unavailable"))
	}
	closed := false
	defer func() {
		if !closed {
			_ = state.Close()
		}
	}()

	emitProgress := func(progress usage.ScanProgress) error {
		if emitter.enabled {
			return emitter.Progress("scan", "running", "scan_progress", "", progress)
		}
		_, err := fmt.Fprintf(c.Stdout, c.tr("scan.progress"),
			progress.HomesDiscovered, progress.HomesTotal,
			progress.FilesDiscovered, progress.FilesProcessed,
			progress.RecordsProcessed, progress.EventsInserted, progress.Warnings)
		return err
	}
	tracker, err := startScanProgressTracker(
		usage.ScanProgress{HomesTotal: len(state.homes)},
		c.HeartbeatInterval,
		emitProgress,
	)
	if err != nil {
		return commandResult{}, scanFailure(err)
	}

	result, scanErr := state.scanner.ScanWithProgress(
		context.Background(), state.homes, *rebuild, tracker.Update,
	)
	stopErr := tracker.Stop()
	closeErr := state.Close()
	closed = true
	if scanErr != nil {
		return commandResult{}, scanFailure(scanErr)
	}
	if stopErr != nil {
		return commandResult{}, scanFailure(stopErr)
	}
	if closeErr != nil {
		return commandResult{}, scanFailure(fmt.Errorf("close scan state: %w", closeErr))
	}

	if !emitter.enabled {
		fmt.Fprintf(c.Stdout, c.tr("scan.complete"),
			result.Homes, result.Files, result.Records, result.EventsInserted,
			result.Duplicates, result.Warnings, float64(result.ElapsedMillis)/1000)
		if result.Unattributed > 0 {
			fmt.Fprintf(c.Stdout, c.tr("scan.unattributed"), result.Unattributed)
		}
	}
	return commandResult{
		Code: "scan_complete",
		Data: scanCommandResult{Scan: result},
	}, nil
}

func (c CLI) scanInvalidArguments(err error) error {
	return &codedError{
		Code:     "invalid_arguments",
		ExitCode: 2,
		Err:      fmt.Errorf(c.tr("error.invalidArguments"), err),
	}
}

func scanFailure(err error) error {
	return &codedError{Code: "scan_failed", ExitCode: 1, Err: err}
}
