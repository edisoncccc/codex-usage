//go:build !windows

package platform

import "os"

// UserLocale follows the conventional locale environment precedence.
func UserLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
