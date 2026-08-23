package stagefile

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestApplyROS2RuntimeAddsResolvedMiddlewareToFinalStage(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "builder", From: "ros:humble-ros-base"},
		{Name: "app", From: "ros:humble-ros-base", Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"python3-pip"}},
		}},
	}}

	applyROS2Runtime(f, "humble", "rmw_cyclonedds_cpp")
	if f.Stages[0].Install != nil {
		t.Fatal("framework runtime must not modify build-only stages")
	}
	got := f.Stages[1].Install.Apt.Packages
	if len(got) != 2 || got[0] != "python3-pip" || got[1] != "ros-humble-rmw-cyclonedds-cpp" {
		t.Fatalf("final APT packages = %v", got)
	}

	applyROS2Runtime(f, "humble", "rmw_cyclonedds_cpp")
	if len(f.Stages[1].Install.Apt.Packages) != 2 {
		t.Fatal("framework package injection must be idempotent")
	}
}

func TestApplyROS2RuntimeCreatesInstallBlock(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{{Name: "app", From: "ros:jazzy-ros-base"}}}
	applyROS2Runtime(f, "jazzy", "rmw_fastrtps_cpp")
	got := f.Stages[0].Install.Apt.Packages
	if len(got) != 1 || got[0] != "ros-jazzy-rmw-fastrtps-cpp" {
		t.Fatalf("APT packages = %v", got)
	}
}

func TestApplyROS2RuntimeRejectsUnvalidatedCompilerInputs(t *testing.T) {
	for _, tc := range []struct {
		distro string
		rmw    string
	}{
		{distro: "humble;touch-pwned", rmw: "rmw_cyclonedds_cpp"},
		{distro: "humble", rmw: "rmw_evil_cpp"},
	} {
		f := &spec.File{Version: 1, Stages: []spec.Stage{{Name: "app", From: "ros:humble-ros-base"}}}
		applyROS2Runtime(f, tc.distro, tc.rmw)
		if f.Stages[0].Install != nil {
			t.Fatalf("applyROS2Runtime(%q, %q) mutated the stage", tc.distro, tc.rmw)
		}
	}
}
