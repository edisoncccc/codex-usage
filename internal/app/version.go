package app

import (
	"errors"
	"flag"
	"fmt"
	"runtime"
	"strconv"
)

var (
	Version    = "2.3.5"
	Commit     = "dev"
	BuildDirty = "true"
	BuildDate  = "unknown"
)

type buildIdentity struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	BuildDate string `json:"build_date"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func currentBuildIdentity() (buildIdentity, error) {
	dirty, err := strconv.ParseBool(BuildDirty)
	if err != nil {
		return buildIdentity{}, &codedError{
			Code:     "build_metadata_invalid",
			ExitCode: 1,
			Err:      fmt.Errorf("parse BuildDirty %q: %w", BuildDirty, err),
		}
	}
	return buildIdentity{
		Version:   Version,
		Commit:    Commit,
		Dirty:     dirty,
		BuildDate: BuildDate,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}, nil
}

func (c CLI) versionCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	if err := flags.Parse(args); err != nil {
		return commandResult{}, &codedError{
			Code:     "invalid_arguments",
			ExitCode: 2,
			Err:      fmt.Errorf(c.tr("error.invalidArguments"), err),
		}
	}
	if flags.NArg() != 0 {
		return commandResult{}, &codedError{
			Code:     "invalid_arguments",
			ExitCode: 2,
			Err:      fmt.Errorf(c.tr("error.invalidArguments"), errors.New("unexpected positional arguments")),
		}
	}
	identity, err := currentBuildIdentity()
	if err != nil {
		return commandResult{}, &codedError{
			Code:     "build_metadata_invalid",
			ExitCode: 1,
			Err:      fmt.Errorf(c.tr("error.buildMetadata"), BuildDirty),
		}
	}
	if !emitter.enabled {
		c.printHumanVersion()
	}
	return commandResult{Code: "version", Data: identity}, nil
}

func (c CLI) printHumanVersion() {
	fmt.Fprintf(c.Stdout, "codex-usage %s (%s, dirty=%s, %s) %s/%s\n", Version, Commit, BuildDirty, BuildDate, runtime.GOOS, runtime.GOARCH)
}
