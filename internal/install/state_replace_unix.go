//go:build !windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceInstallRecord(temporaryPath, destinationPath string) error {
	if err := requireSameParentDirectory(temporaryPath, destinationPath); err != nil {
		return err
	}
	parentPath := filepath.Dir(destinationPath)
	parent, err := os.Open(parentPath)
	if err != nil {
		return fmt.Errorf("open install record directory %s for sync: %w", parentPath, err)
	}
	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		_ = parent.Close()
		return fmt.Errorf("rename install record %s to %s: %w", temporaryPath, destinationPath, err)
	}
	if err := parent.Sync(); err != nil {
		_ = parent.Close()
		return fmt.Errorf("sync install record directory %s: %w", parentPath, err)
	}
	if err := parent.Close(); err != nil {
		return fmt.Errorf("close install record directory %s after sync: %w", parentPath, err)
	}
	return nil
}

func isSafeRegularFile(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
