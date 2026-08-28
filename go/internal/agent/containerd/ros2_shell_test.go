package containerd

import (
	"strings"
	"testing"
)

// The sidecar ran commands through `/bin/bash`. Every non-FastRTPS RMW — which
// includes the CycloneDDS default, i.e. the common case — reuses the *app's own
// image* as the sidecar image, and a slim ROS image (the natural target for a C++
// or Swift node) frequently ships no bash. That made the ros2cli probe fail with
// an opaque setup error, after which every `wendy device ros2` command failed with
// a bare exec error and no hint about the cause.

func TestROS2ShellArgs_UsesPOSIXShell(t *testing.T) {
	args := ros2ShellArgs("humble", ros2SourceAndExec("humble"), []string{"topic", "list"})
	if args[0] != "/bin/sh" {
		t.Errorf("shell = %q, want /bin/sh (a slim app image may ship no bash)", args[0])
	}
	if args[1] != "-c" {
		t.Errorf("args[1] = %q, want -c", args[1])
	}
}

func TestROS2SourceAndExec_UsesPOSIXSourceBuiltin(t *testing.T) {
	script := ros2SourceAndExec("humble")
	if strings.Contains(script, "source ") {
		t.Errorf("`source` is a bashism; use `.` so the script runs under /bin/sh: %q", script)
	}
	if !strings.HasPrefix(script, ". /opt/ros/humble/setup.sh") {
		t.Errorf("script should dot-source the distro's setup.sh, got %q", script)
	}
	if !strings.Contains(script, `exec ros2 "$@"`) {
		t.Errorf(`script must keep caller args out of shell interpretation via "$@", got %q`, script)
	}
}

// The "$@" indirection is the whole reason user arguments cannot be interpreted
// by the shell: they arrive as separate argv entries, never spliced into the
// script text.
func TestROS2ShellArgs_UserArgsStayOutOfTheScript(t *testing.T) {
	nasty := []string{"topic", "echo", "/chatter; rm -rf /", "$(id)", "`whoami`", "&& curl evil"}
	args := ros2ShellArgs("humble", ros2SourceAndExec("humble"), nasty)

	script := args[2]
	for _, arg := range nasty {
		if strings.Contains(script, arg) {
			t.Errorf("user argument %q was spliced into the script text: %q", arg, script)
		}
	}
	// argv[0] for the script, then the caller's arguments verbatim as separate
	// entries.
	if args[3] != "ros2" {
		t.Errorf(`args[3] = %q, want "ros2" ($0 for the script)`, args[3])
	}
	got := args[4:]
	if len(got) != len(nasty) {
		t.Fatalf("forwarded %d args, want %d: %q", len(got), len(nasty), got)
	}
	for i, want := range nasty {
		if got[i] != want {
			t.Errorf("arg %d = %q, want %q (verbatim, unmodified)", i, got[i], want)
		}
	}
}

func TestROS2ShellArgs_NilExtraOmitsPositionalArgv(t *testing.T) {
	// The ros2cli probe runs a self-contained script with no positional args, so
	// it must not get a stray "ros2" argv entry.
	args := ros2ShellArgs("humble", "command -v ros2 >/dev/null 2>&1", nil)
	if len(args) != 3 {
		t.Fatalf("got %d args, want 3: %q", len(args), args)
	}
	if args[0] != "/bin/sh" || args[1] != "-c" {
		t.Errorf("unexpected shell invocation: %q", args)
	}
}

func TestROS2ShellArgs_EmptyExtraStillGetsArgvZero(t *testing.T) {
	// A ros2 invocation with no arguments is still a ros2 invocation.
	args := ros2ShellArgs("humble", ros2SourceAndExec("humble"), []string{})
	if len(args) != 4 || args[3] != "ros2" {
		t.Errorf("got %q, want the script plus an argv[0]", args)
	}
}

func TestROS2SourceAndExecUsesTheGivenDistro(t *testing.T) {
	for _, distro := range []string{"humble", "jazzy", "iron"} {
		script := ros2SourceAndExec(distro)
		if !strings.Contains(script, "/opt/ros/"+distro+"/setup.sh") {
			t.Errorf("distro %q not reflected in the script: %q", distro, script)
		}
	}
}

// Dot-sourcing setup.bash under /bin/sh is the bug this guards. ament's
// setup.bash resolves its own path via ${BASH_SOURCE[0]} and sets
// AMENT_SHELL=bash, so dash aborts on it. Because both call sites silence the
// sourcing with >/dev/null 2>&1, the failure surfaced only as `command -v ros2`
// returning non-zero — which the sidecar reported as the app image lacking the
// ros2 CLI, for images that shipped it. setup.sh is ament's POSIX variant and
// bakes its prefix in at generation time.
func TestROS2SetupScript_IsThePOSIXVariantNotBash(t *testing.T) {
	for _, distro := range []string{"humble", "jazzy", "iron"} {
		got := ros2SetupScript(distro)
		if strings.HasSuffix(got, ".bash") {
			t.Errorf("ros2SetupScript(%q) = %q; setup.bash cannot be sourced by dash", distro, got)
		}
		if want := "/opt/ros/" + distro + "/setup.sh"; got != want {
			t.Errorf("ros2SetupScript(%q) = %q, want %q", distro, got, want)
		}
	}
}

// Both the probe and the exec path must agree on the script, or the probe can
// pass while real commands fail (or the reverse).
func TestROS2SetupScript_ProbeAndExecAgree(t *testing.T) {
	script := ros2SourceAndExec("humble")
	if !strings.Contains(script, ros2SetupScript("humble")) {
		t.Errorf("exec path %q does not use ros2SetupScript(%q) = %q",
			script, "humble", ros2SetupScript("humble"))
	}
}

func TestROS2BagDirModeIsNotWorldReadable(t *testing.T) {
	// Bags hold raw sensor data; 0o755 let every local account read them.
	if ROS2BagDirMode&0o007 != 0 {
		t.Errorf("ROS2BagDirMode = %#o, want no world bits", ROS2BagDirMode)
	}
	if ROS2BagDirMode&0o700 != 0o700 {
		t.Errorf("ROS2BagDirMode = %#o, owner must retain rwx", ROS2BagDirMode)
	}
}
