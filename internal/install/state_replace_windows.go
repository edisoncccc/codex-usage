//go:build windows

package install

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceInstallRecord(temporaryPath, destinationPath string) error {
	if err := requireSameParentDirectory(temporaryPath, destinationPath); err != nil {
		return err
	}
	temporaryUTF16, err := syscall.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return fmt.Errorf("encode temporary install record path %s: %w", temporaryPath, err)
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destinationPath)
	if err != nil {
		return fmt.Errorf("encode destination install record path %s: %w", destinationPath, err)
	}
	for attempt := 0; attempt < 25; attempt++ {
		result, _, callErr := moveFileExW.Call(
			uintptr(unsafe.Pointer(temporaryUTF16)),
			uintptr(unsafe.Pointer(destinationUTF16)),
			uintptr(moveFileReplaceExisting|moveFileWriteThrough),
		)
		if result != 0 {
			return nil
		}
		if errors.Is(callErr, syscall.Errno(0)) {
			callErr = errors.New("MoveFileExW returned failure without an error code")
		}
		if attempt < 24 && retryableMoveFileError(callErr) {
			time.Sleep(4 * time.Millisecond)
			continue
		}
		return fmt.Errorf("MoveFileExW %s to %s: %w", temporaryPath, destinationPath, callErr)
	}
	return errors.New("MoveFileExW retry loop ended unexpectedly")
}

func retryableMoveFileError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ERROR_ACCESS_DENIED ||
		errno == syscall.Errno(32) ||
		errno == syscall.Errno(33)
}

func isSafeRegularFile(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
