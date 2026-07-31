package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePurgeStateDirRequiresExactMarker(t *testing.T) {
	dir := t.TempDir()
	if err := ValidatePurgeStateDir(dir); err == nil {
		t.Fatal("expected missing marker rejection")
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-usage-state"), []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePurgeStateDir(dir); err == nil {
		t.Fatal("expected invalid marker rejection")
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-usage-state"), []byte("codex-usage-state-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePurgeStateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePurgeStateDirRejectsBroadDirectories(t *testing.T) {
	root := filepath.Clean(filepath.VolumeName(t.TempDir()) + string(os.PathSeparator))
	if err := ValidatePurgeStateDir(root); err == nil {
		t.Fatal("expected filesystem root rejection")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := ValidatePurgeStateDir(home); err == nil {
			t.Fatal("expected user home rejection")
		}
	}
}
