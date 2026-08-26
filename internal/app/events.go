package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

type machineEvent struct {
	SchemaVersion string `json:"schema_version"`
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	Status        string `json:"status"`
	Timestamp     string `json:"timestamp"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	Progress      any    `json:"progress,omitempty"`
	Result        any    `json:"result,omitempty"`
}

type verificationStatus string

const (
	verificationVerified      verificationStatus = "verified"
	verificationNotApplicable verificationStatus = "not_applicable"
	verificationNotChecked    verificationStatus = "not_checked"
)

type commandResult struct {
	Code string
	Data any
}

type codedError struct {
	Code     string
	ExitCode int
	Err      error
	Details  any
}

func (e *codedError) Error() string { return e.Err.Error() }
func (e *codedError) Unwrap() error { return e.Err }

type structuredHandler func(args []string, emitter *eventEmitter) (commandResult, error)

type eventEmitter struct {
	mu       sync.Mutex
	writer   io.Writer
	flagOut  io.Writer
	enabled  bool
	now      func() time.Time
	terminal bool
	poisoned bool
}

type eventEmissionError struct {
	written       int
	streamCorrupt bool
	err           error
}

func (e *eventEmissionError) Error() string { return e.err.Error() }
func (e *eventEmissionError) Unwrap() error { return e.err }

func newEventEmitter(w io.Writer, enabled bool, now func() time.Time) *eventEmitter {
	if w == nil {
		w = io.Discard
	}
	if now == nil {
		now = time.Now
	}
	return &eventEmitter{writer: w, flagOut: io.Discard, enabled: enabled, now: now}
}

func (e *eventEmitter) Progress(phase, status, code, message string, progress any) error {
	return e.write(false, machineEvent{
		Event:    "progress",
		Phase:    phase,
		Status:   status,
		Code:     code,
		Message:  message,
		Progress: progress,
	})
}

func (e *eventEmitter) finish(event, phase, status, code, message string, result any) error {
	if event != "result" && event != "error" {
		return fmt.Errorf("invalid terminal event %q", event)
	}
	return e.write(true, machineEvent{
		Event:   event,
		Phase:   phase,
		Status:  status,
		Code:    code,
		Message: message,
		Result:  result,
	})
}

func (e *eventEmitter) write(terminal bool, event machineEvent) error {
	if !e.enabled {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.poisoned {
		return &eventEmissionError{streamCorrupt: true, err: errors.New("event stream is corrupt")}
	}
	if e.terminal {
		return &eventEmissionError{err: errors.New("terminal event already emitted")}
	}
	event.SchemaVersion = "1"
	event.Timestamp = e.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(event)
	if err != nil {
		return &eventEmissionError{err: err}
	}
	line = append(line, '\n')
	written, err := writeFull(e.writer, line)
	if err != nil {
		if written == len(line) && terminal {
			e.terminal = true
		}
		streamCorrupt := written > 0 && written < len(line)
		if streamCorrupt {
			e.poisoned = true
		}
		return &eventEmissionError{written: written, streamCorrupt: streamCorrupt, err: err}
	}
	if terminal {
		e.terminal = true
	}
	return nil
}

func (c CLI) runStructured(phase string, args []string, handler structuredHandler) int {
	c = c.withDefaults()
	enabled, handlerArgs := extractJSONArgument(args)
	emitter := newEventEmitter(c.Stdout, enabled, c.Now)
	emitter.flagOut = c.Stderr
	if enabled {
		emitter.flagOut = io.Discard
	}
	result, err := handler(handlerArgs, emitter)
	if emitter.streamIsPoisoned() {
		return 1
	}
	if err == nil {
		if !enabled {
			return 0
		}
		if emitErr := emitter.finish("result", phase, "success", result.Code, "", result.Data); emitErr != nil {
			if emissionFailedBeforeWrite(emitErr) {
				_ = emitter.finish("error", phase, "failed", "internal_error", "failed to emit terminal result", nil)
			}
			return 1
		}
		return 0
	}

	exitCode := 1
	code := "internal_error"
	var details any
	var typed *codedError
	if errors.As(err, &typed) {
		code = typed.Code
		exitCode = typed.ExitCode
		details = typed.Details
		if exitCode == 0 {
			exitCode = 1
		}
	}
	if !enabled {
		fmt.Fprintln(c.Stderr, c.tr("error.prefix"), err)
		return exitCode
	}
	if emitErr := emitter.finish("error", phase, "failed", code, err.Error(), details); emitErr != nil {
		if emissionFailedBeforeWrite(emitErr) {
			_ = emitter.finish("error", phase, "failed", "internal_error", "failed to emit terminal error", nil)
		}
		return 1
	}
	return exitCode
}

func writeFull(w io.Writer, value []byte) (int, error) {
	written := 0
	for written < len(value) {
		count, err := w.Write(value[written:])
		if count < 0 || count > len(value)-written {
			return written, io.ErrShortWrite
		}
		written += count
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func emissionFailedBeforeWrite(err error) bool {
	var emission *eventEmissionError
	return errors.As(err, &emission) && emission.written == 0 && !emission.streamCorrupt
}

func (e *eventEmitter) streamIsPoisoned() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.poisoned
}

func extractJSONArgument(args []string) (bool, []string) {
	enabled := false
	flagsEnded := false
	remaining := make([]string, 0, len(args))
	for _, argument := range args {
		if !flagsEnded && argument == "--json" {
			enabled = true
			continue
		}
		remaining = append(remaining, argument)
		if argument == "--" {
			flagsEnded = true
		}
	}
	return enabled, remaining
}
