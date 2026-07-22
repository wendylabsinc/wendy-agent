package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFoxgloveApp(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 3, rmw: "rmw_cyclonedds_cpp", distro: "humble"}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatalf("writeFoxgloveApp: %v", err)
	}

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dfs := string(df)
	for _, want := range []string{
		"FROM ros:humble",
		"ros-humble-foxglove-bridge",
		"ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=8765 address:=0.0.0.0",
	} {
		if !strings.Contains(dfs, want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, dfs)
		}
	}

	wj, err := os.ReadFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		t.Fatal(err)
	}
	wjs := string(wj)
	for _, want := range []string{
		`"domainId": 3`,
		`"rmw": "rmw_cyclonedds_cpp"`,
		`"distro": "humble"`,
		`"type": "network", "mode": "host"`,
	} {
		if !strings.Contains(wjs, want) {
			t.Fatalf("wendy.json missing %q:\n%s", want, wjs)
		}
	}
}

func TestWriteFoxgloveAppDistroTemplated(t *testing.T) {
	dir := t.TempDir()
	if err := writeFoxgloveApp(dir, foxgloveServeOpts{domain: 0, rmw: "rmw_fastrtps_cpp", distro: "jazzy"}); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if !strings.Contains(string(df), "FROM ros:jazzy") || !strings.Contains(string(df), "ros-jazzy-foxglove-bridge") {
		t.Fatalf("distro not templated into Dockerfile:\n%s", df)
	}
}
