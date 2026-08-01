package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFoxgloveDirectCommandsStayOnLANDevice(t *testing.T) {
	opts := foxgloveServeOpts{device: "wendy-box.local", localPort: 9001}

	wantRemove := []string{"device", "apps", "remove", foxgloveAppID, "--force", "--cleanup", "--device", "wendy-box.local"}
	if got := foxgloveRemoveArgs(opts); !reflect.DeepEqual(got, wantRemove) {
		t.Fatalf("remove args = %q, want %q", got, wantRemove)
	}
	wantRun := []string{"run", "--detach", "--device", "wendy-box.local", "--lan"}
	if got := foxgloveRunArgs(opts); !reflect.DeepEqual(got, wantRun) {
		t.Fatalf("run args = %q, want %q", got, wantRun)
	}
}

func TestFoxgloveCloudCommandsPinSameAsset(t *testing.T) {
	opts := foxgloveServeOpts{
		device:    "323",
		localPort: 9001,
		cloud:     true,
		cloudCfg: cloudDeviceConfig{
			CloudGRPC: "cloud.example:443",
			BrokerURL: "broker.example:443",
		},
	}
	cloudFlags := []string{"--cloud-grpc", "cloud.example:443", "--broker-url", "broker.example:443"}

	wantRemove := append([]string{"cloud", "device", "apps", "remove", foxgloveAppID, "--force", "--cleanup", "--device", "323"}, cloudFlags...)
	if got := foxgloveRemoveArgs(opts); !reflect.DeepEqual(got, wantRemove) {
		t.Fatalf("remove args = %q, want %q", got, wantRemove)
	}
	wantRun := append([]string{"cloud", "run", "--detach", "--device", "323"}, cloudFlags...)
	if got := foxgloveRunArgs(opts); !reflect.DeepEqual(got, wantRun) {
		t.Fatalf("run args = %q, want %q", got, wantRun)
	}
	wantTunnel := append([]string{"cloud", "tunnel", "9001:8765", "--device", "323"}, cloudFlags...)
	if got := foxgloveTunnelArgs(opts); !reflect.DeepEqual(got, wantTunnel) {
		t.Fatalf("tunnel args = %q, want %q", got, wantTunnel)
	}
}

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
		"export ROS_LOCALHOST_ONLY=0",
		"ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=8765 address:=0.0.0.0 include_hidden:=true",
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
		`"discoveryScope": "host"`,
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

func TestWriteFoxgloveAppCycloneDDSInterface(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble", iface: "enP8p1s0"}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if !strings.Contains(string(df), `NetworkInterface name=\"enP8p1s0\"`) {
		t.Fatalf("CycloneDDS interface not templated into Dockerfile:\n%s", df)
	}
}

func TestWriteFoxgloveAppRejectsUnsafeInterface(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble", iface: `eth0'; touch /tmp/pwned`}
	if err := writeFoxgloveApp(dir, opts); err == nil {
		t.Fatal("expected unsafe interface name to be rejected")
	}
}
