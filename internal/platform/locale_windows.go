//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// UserLocale returns the Windows UI locale without starting another process.
func UserLocale() string {
	const localeNameMaxLength = 85
	buffer := make([]uint16, localeNameMaxLength)
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	languageID, _, _ := kernel32.NewProc("GetUserDefaultUILanguage").Call()
	if languageID != 0 {
		result, _, _ := kernel32.NewProc("LCIDToLocaleName").Call(languageID, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		if result != 0 {
			return windows.UTF16ToString(buffer)
		}
	}
	result, _, _ := kernel32.NewProc("GetUserDefaultLocaleName").Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if result == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer)
}
