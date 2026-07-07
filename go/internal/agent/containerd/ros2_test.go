package containerd

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
)

func TestROS2ExecCommandBridge(t *testing.T) {
	got := ros2ExecCommand(services.ROS2ExecOptions{BridgeBinary: true}, "jazzy")
	want := "/var/wendy/ros2-bridge/jazzy/wendy-ros2-bridge"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if ros2ExecCommand(services.ROS2ExecOptions{Binary: "python3"}, "jazzy") != "python3" {
		t.Fatal("python3 allowlist regressed")
	}
}

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
