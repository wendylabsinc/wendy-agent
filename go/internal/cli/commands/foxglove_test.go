package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
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

func TestWriteFoxgloveAppUnitreeMessages(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{
		domain:  0,
		rmw:     "rmw_cyclonedds_cpp",
		distro:  "humble",
		iface:   "enP8p1s0",
		unitree: true,
	}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	dfs := string(df)
	for _, want := range []string{
		"ARG UNITREE_ROS2_COMMIT=" + unitreeROS2Commit,
		"https://github.com/unitreerobotics/unitree_ros2.git",
		"--packages-select unitree_api unitree_go",
		"--install-base /opt/unitree_msgs",
		"source /opt/unitree_msgs/setup.bash",
	} {
		if !strings.Contains(dfs, want) {
			t.Fatalf("Unitree Dockerfile missing %q:\n%s", want, dfs)
		}
	}
}

func TestWriteFoxgloveAppDoesNotInstallUnitreeMessagesByDefault(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble"}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if strings.Contains(string(df), "unitree_ros2") || strings.Contains(string(df), "/opt/unitree_msgs") {
		t.Fatalf("generic Foxglove image unexpectedly includes Unitree messages:\n%s", df)
	}
}

func TestWriteFoxgloveAppRejectsUnsafeInterface(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble", iface: `eth0'; touch /tmp/pwned`}
	if err := writeFoxgloveApp(dir, opts); err == nil {
		t.Fatal("expected unsafe interface name to be rejected")
	}
}

func TestProbableUnitreeInterface(t *testing.T) {
	tests := []struct {
		name   string
		ifaces []*agentpb.NetworkInterface
		want   string
	}{
		{
			name: "unique Unitree network",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "enP8p1s0", IpAddresses: []string{"192.168.123.18"}},
				{Name: "wlan0", IpAddresses: []string{"192.168.0.107", "fd00::1"}},
			},
			want: "enP8p1s0",
		},
		{
			name: "no Unitree network",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"10.0.0.2"}},
			},
		},
		{
			name: "ambiguous Unitree network",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"192.168.123.18"}},
				{Name: "eth1", IpAddresses: []string{"192.168.123.19"}},
			},
		},
		{
			name: "invalid interface name",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "bad interface", IpAddresses: []string{"192.168.123.18"}},
			},
		},
		{
			name: "invalid address ignored",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"not-an-ip"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probableUnitreeInterface(tt.ifaces); got != tt.want {
				t.Fatalf("probableUnitreeInterface() = %q, want %q", got, tt.want)
			}
		})
	}
}
