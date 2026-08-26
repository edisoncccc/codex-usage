package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/install"
	"github.com/zJay26/codex-usage/internal/platform"
)

type uninstallReceipt struct {
	InstallPath      string `json:"install_path"`
	StatePath        string `json:"state_path"`
	DatabasePath     string `json:"database_path"`
	ProgramRemoved   bool   `json:"program_removed"`
	RemovalScheduled bool   `json:"removal_scheduled"`
	DataPreserved    bool   `json:"data_preserved"`
	Purged           bool   `json:"purged"`
}

type uninstallCommandDeps struct {
	ResolvePaths              func() (config.Paths, error)
	ValidateRemoval           func(executable, stateDir, recordPath string, purge bool) error
	UninstallService          func(executable, stateDir string) error
	RemoveInstalledExecutable func(executable, stateDir string, purge bool) error
	RemoveInstallRecord       func(path string) error
	RemovalMode               func() platform.RemovalMode
}

func (c CLI) uninstallCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	yes := flags.Bool("yes", false, c.tr("flag.yes"))
	purge := flags.Bool("purge", false, c.tr("flag.purge"))
	if err := flags.Parse(args); err != nil {
		return commandResult{}, invalidLifecycleArguments(c, err)
	}
	if flags.NArg() != 0 {
		return commandResult{}, invalidLifecycleArguments(c, errors.New("unexpected positional arguments"))
	}

	deps := c.resolvedUninstallDependencies()
	paths, err := deps.ResolvePaths()
	if err != nil {
		return commandResult{}, uninstallCommandFailure(err, "existing_install_untrusted")
	}
	paths, err = absoluteInstallPaths(paths)
	if err != nil {
		return commandResult{}, uninstallCommandFailure(err, "existing_install_untrusted")
	}
	confirmation := uninstallReceipt{
		InstallPath: paths.InstalledEXE, StatePath: paths.StateDir, DatabasePath: paths.Database,
		DataPreserved: !*purge,
	}
	if emitter.enabled && !*yes {
		return commandResult{}, &codedError{
			Code: "confirmation_required", ExitCode: 2,
			Err: errors.New(c.tr("uninstall.confirm.required")), Details: confirmation,
		}
	}
	if !emitter.enabled {
		if err := writeHumanUninstallConfirmation(c, confirmation, *purge, !*yes); err != nil {
			return commandResult{}, uninstallCommandFailure(err, "internal_error")
		}
		if !*yes {
			confirmed, confirmErr := readInstallConfirmation(c.Stdin)
			if confirmErr != nil {
				return commandResult{}, uninstallCommandFailure(confirmErr, "confirmation_required")
			}
			if !confirmed {
				return commandResult{}, &codedError{
					Code: "confirmation_required", ExitCode: 1,
					Err: errors.New(c.tr("uninstall.confirm.declined")), Details: confirmation,
				}
			}
		}
	}

	if err := deps.ValidateRemoval(paths.InstalledEXE, paths.StateDir, paths.InstallRecord, *purge); err != nil {
		return commandResult{}, uninstallCommandFailure(err, "existing_install_untrusted")
	}
	if err := deps.UninstallService(paths.InstalledEXE, paths.StateDir); err != nil {
		return commandResult{}, uninstallCommandFailure(err, "uninstall_failed")
	}
	if err := deps.RemoveInstalledExecutable(paths.InstalledEXE, paths.StateDir, *purge); err != nil {
		return commandResult{}, uninstallCommandFailure(err, "uninstall_failed")
	}
	if err := deps.RemoveInstallRecord(paths.InstallRecord); err != nil {
		return commandResult{}, uninstallCommandFailure(err, "uninstall_failed")
	}

	mode := deps.RemovalMode()
	receipt := confirmation
	receipt.ProgramRemoved = mode == platform.RemovalModeRemoved
	receipt.RemovalScheduled = mode == platform.RemovalModeScheduled
	receipt.Purged = *purge && mode == platform.RemovalModeRemoved
	if !emitter.enabled {
		switch {
		case receipt.RemovalScheduled && *purge:
			fmt.Fprintln(c.Stdout, c.tr("uninstall.complete.purgeScheduled", paths.StateDir))
		case receipt.RemovalScheduled:
			fmt.Fprintln(c.Stdout, c.tr("uninstall.complete.scheduled", paths.Database))
		case *purge:
			fmt.Fprintln(c.Stdout, c.tr("uninstall.complete.purged"))
		default:
			fmt.Fprintln(c.Stdout, c.tr("uninstall.complete.preserved", paths.Database))
		}
	}
	return commandResult{Code: "uninstall_complete", Data: receipt}, nil
}

func (c CLI) resolvedUninstallDependencies() uninstallCommandDeps {
	deps := uninstallCommandDeps{
		ResolvePaths:              config.ResolvePaths,
		ValidateRemoval:           validateUninstallRemoval,
		UninstallService:          platform.UninstallService,
		RemoveInstalledExecutable: platform.RemoveInstalledExecutable,
		RemoveInstallRecord:       removeInstallRecord,
		RemovalMode:               platform.InstalledExecutableRemovalMode,
	}
	if c.uninstallDeps == nil {
		return deps
	}
	if c.uninstallDeps.ResolvePaths != nil {
		deps.ResolvePaths = c.uninstallDeps.ResolvePaths
	}
	if c.uninstallDeps.ValidateRemoval != nil {
		deps.ValidateRemoval = c.uninstallDeps.ValidateRemoval
	}
	if c.uninstallDeps.UninstallService != nil {
		deps.UninstallService = c.uninstallDeps.UninstallService
	}
	if c.uninstallDeps.RemoveInstalledExecutable != nil {
		deps.RemoveInstalledExecutable = c.uninstallDeps.RemoveInstalledExecutable
	}
	if c.uninstallDeps.RemoveInstallRecord != nil {
		deps.RemoveInstallRecord = c.uninstallDeps.RemoveInstallRecord
	}
	if c.uninstallDeps.RemovalMode != nil {
		deps.RemovalMode = c.uninstallDeps.RemovalMode
	}
	return deps
}

func validateUninstallRemoval(executable, stateDir, recordPath string, purge bool) error {
	if err := platform.ValidateInstalledRemoval(executable, stateDir, purge); err != nil {
		return err
	}
	recordAbsolute, err := canonicalAbsolutePath(recordPath)
	if err != nil {
		return err
	}
	if !sameFilePath(filepath.Dir(recordAbsolute), stateDir) || filepath.Base(recordAbsolute) != "install.json" {
		return fmt.Errorf("existing_install_untrusted: install record is outside the state directory")
	}
	record, err := install.Load(recordAbsolute)
	if err != nil {
		return err
	}
	info, err := os.Lstat(executable)
	if errors.Is(err, os.ErrNotExist) {
		if record != nil && !sameFilePath(record.ExecutablePath, executable) {
			return fmt.Errorf("existing_install_untrusted: install record targets another executable")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || record == nil {
		return fmt.Errorf("existing_install_untrusted: executable is not owned by a valid install record")
	}
	if !sameFilePath(record.ExecutablePath, executable) {
		return fmt.Errorf("existing_install_untrusted: install record targets another executable")
	}
	digest, err := install.FileSHA256(executable)
	if err != nil {
		return err
	}
	if digest != record.ExecutableSHA256 {
		return fmt.Errorf("existing_install_untrusted: executable digest does not match install record")
	}
	return nil
}

func removeInstallRecord(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("install record is not a safe regular file: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return platform.SyncParent(path)
}

func writeHumanUninstallConfirmation(c CLI, receipt uninstallReceipt, purge, prompt bool) error {
	lines := []string{
		c.tr("uninstall.confirm.title"),
		c.tr("uninstall.confirm.installPath", receipt.InstallPath),
		c.tr("uninstall.confirm.statePath", receipt.StatePath),
	}
	if purge {
		lines = append(lines, c.tr("uninstall.confirm.purge", receipt.StatePath))
	} else {
		lines = append(lines, c.tr("uninstall.confirm.preserve", receipt.DatabasePath))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(c.Stdout, line); err != nil {
			return err
		}
	}
	if prompt {
		_, err := fmt.Fprint(c.Stdout, c.tr("uninstall.confirm.prompt"))
		return err
	}
	return nil
}

func uninstallCommandFailure(err error, fallbackCode string) error {
	if err == nil {
		err = errors.New(fallbackCode)
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded
	}
	var permission *platform.PermissionError
	if errors.As(err, &permission) || errors.Is(err, os.ErrPermission) {
		fallbackCode = "permission_required"
	}
	if strings.Contains(strings.ToLower(err.Error()), "existing_install_untrusted") {
		fallbackCode = "existing_install_untrusted"
	}
	return &codedError{Code: fallbackCode, ExitCode: 1, Err: err}
}
