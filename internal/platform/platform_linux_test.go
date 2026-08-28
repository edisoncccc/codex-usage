//go:build linux

package platform

import "testing"

func TestClassifyLinuxSystemdSnapshotFailureOnlyAllowsKnownUnavailableBusDiagnostics(t *testing.T) {
	tests := []struct {
		diagnostic string
		want       linuxSystemdSnapshotFailureKind
	}{
		{diagnostic: "Failed to connect to bus: No medium found", want: linuxSystemdSnapshotUserBusUnavailable},
		{diagnostic: "Failed to connect to bus: No such file or directory", want: linuxSystemdSnapshotUserBusUnavailable},
		{diagnostic: "Failed to get D-Bus connection: No such file or directory", want: linuxSystemdSnapshotUserBusUnavailable},
		{
			diagnostic: "Failed to connect to bus: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined (consider using --machine=<user>@.host --user to connect to bus of other user)",
			want:       linuxSystemdSnapshotUserBusUnavailable,
		},
		{
			diagnostic: "Failed to connect to user scope bus via local transport: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined (consider using --machine=<user>@.host --user to connect to bus of other user)",
			want:       linuxSystemdSnapshotUserBusUnavailable,
		},
		{
			diagnostic: "Failed to connect to bus: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined (consider using --machine=<user>@.host --user)",
			want:       linuxSystemdSnapshotFailureUnknown,
		},
		{diagnostic: "Failed to connect to bus: Permission denied", want: linuxSystemdSnapshotFailureUnknown},
		{diagnostic: "user bus permission denied", want: linuxSystemdSnapshotFailureUnknown},
		{diagnostic: "malformed manager response", want: linuxSystemdSnapshotFailureUnknown},
	}
	for _, tt := range tests {
		if got := classifyLinuxSystemdSnapshotFailure(tt.diagnostic); got != tt.want {
			t.Errorf("diagnostic %q classified as %q want %q", tt.diagnostic, got, tt.want)
		}
	}
}
