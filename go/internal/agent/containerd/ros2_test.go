package containerd

import "testing"

func TestROS2ExecBinarySelection(t *testing.T) {
	if got := ros2ExecBinary(""); got != "ros2" {
		t.Fatalf("default = %q, want ros2", got)
	}
	if got := ros2ExecBinary("python3"); got != "python3" {
		t.Fatalf("python3 = %q, want python3", got)
	}
	// Any unlisted binary must not be honoured; it falls back to ros2 so a
	// caller can never run an arbitrary executable in the sidecar.
	for _, bad := range []string{"rm", "sh", "bash", "/bin/sh", "python", "ros2; rm -rf /"} {
		if got := ros2ExecBinary(bad); got != "ros2" {
			t.Fatalf("ros2ExecBinary(%q) = %q, want ros2 (fallback)", bad, got)
		}
	}
}
