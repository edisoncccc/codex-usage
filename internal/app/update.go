package app

import (
	"errors"
	"flag"
	"fmt"

	"github.com/zJay26/codex-usage/internal/installpolicy"
)

type updateReceipt struct {
	ChannelEnabled bool   `json:"channel_enabled"`
	Checked        bool   `json:"checked"`
	Modified       bool   `json:"modified"`
	PolicyPath     string `json:"policy_path"`
}

func (c CLI) updateCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	check := flags.Bool("check", false, c.tr("flag.updateCheck"))
	yes := flags.Bool("yes", false, c.tr("flag.yes"))
	if err := flags.Parse(args); err != nil {
		return commandResult{}, invalidLifecycleArguments(c, err)
	}
	if flags.NArg() != 0 || *check == *yes {
		return commandResult{}, invalidLifecycleArguments(c, errors.New("use exactly one of --check or --yes"))
	}

	receipt := updateReceipt{
		ChannelEnabled: installpolicy.BinaryReleaseEnabled,
		Checked:        false,
		Modified:       false,
		PolicyPath:     "install-policy.json",
	}
	return commandResult{}, &codedError{
		Code:     "release_channel_disabled",
		ExitCode: 1,
		Err:      errors.New(c.tr("update.disabled")),
		Details:  receipt,
	}
}

func invalidLifecycleArguments(c CLI, err error) *codedError {
	return &codedError{
		Code:     "invalid_arguments",
		ExitCode: 2,
		Err:      fmt.Errorf(c.tr("error.invalidArguments"), err),
	}
}
