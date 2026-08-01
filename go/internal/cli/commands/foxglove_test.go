package commands

import (
	"os"
	"os/exec"
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
		"ARG UNITREE_SDK2_COMMIT=" + unitreeSDK2Commit,
		"ARG UNITREE_SDK2_PYTHON_COMMIT=" + unitreeSDK2PythonCommit,
		"https://github.com/unitreerobotics/unitree_ros2.git",
		"https://github.com/unitreerobotics/unitree_sdk2.git",
		"https://github.com/unitreerobotics/unitree_sdk2_python.git",
		"python3 /tmp/unitree_sdk2_to_ros.py",
		"python3 /opt/wendy/go2_camera_bridge.py --interface enP8p1s0",
		"CYCLONEDDS_HOME=/opt/cyclonedds",
		"/opt/ros/humble/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH)",
		"pip3 install --no-cache-dir cyclonedds==0.10.2",
		"pip3 install --no-cache-dir --no-deps .",
		"ros-humble-grid-map-msgs",
		"--base-paths /tmp/unitree_ros2/cyclonedds_ws/src/unitree",
		"--packages-select unitree_api unitree_go unitree_hg",
		"--install-base /opt/unitree_msgs",
		"source /opt/unitree_msgs/setup.bash",
	} {
		if !strings.Contains(dfs, want) {
			t.Fatalf("Unitree Dockerfile missing %q:\n%s", want, dfs)
		}
	}
	converter, err := os.ReadFile(filepath.Join(dir, "unitree_sdk2_to_ros.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ConfigChangeStatus", "AgvBmsState", "SportModeState", "unsupported Unitree SDK2 field type"} {
		if !strings.Contains(string(converter), want) {
			t.Fatalf("Unitree converter missing %q", want)
		}
	}
	cameraBridge, err := os.ReadFile(filepath.Join(dir, "go2_camera_bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ChannelFactoryInitialize",
		"VideoClient",
		"GetImageSample",
		"retry_delay = min(retry_delay * 2.0, 5.0)",
		"time.sleep(retry_delay)",
		"next_warning_at = now + 30.0",
		"/front_camera/image/compressed",
		`message.format = "jpeg"`,
	} {
		if !strings.Contains(string(cameraBridge), want) {
			t.Fatalf("Go2 camera bridge missing %q", want)
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
	if _, err := os.Stat(filepath.Join(dir, "unitree_sdk2_to_ros.py")); !os.IsNotExist(err) {
		t.Fatal("generic Foxglove app unexpectedly includes Unitree schema converter")
	}
	if _, err := os.Stat(filepath.Join(dir, "go2_camera_bridge.py")); !os.IsNotExist(err) {
		t.Fatal("generic Foxglove app unexpectedly includes Go2 camera bridge")
	}
}

func TestUnitreeSDK2SchemaConverter(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to exercise the generated image converter")
	}

	createFixture := func(t *testing.T, configFieldType string) (string, string, string) {
		t.Helper()
		root := t.TempDir()
		sdkRoot := filepath.Join(root, "sdk")
		rosRoot := filepath.Join(root, "ros")
		headers := map[string]string{
			"go2/ConfigChangeStatus_.hpp": "class ConfigChangeStatus_ {\nprivate:\n " + configFieldType + " name_;\n std::string content_;\npublic:\n};\n",
			"hg/AgvBmsState_.hpp":         "class AgvBmsState_ {\nprivate:\n std::array<int16_t, 3> temperature_ = { };\n bool is_charging_ = false;\npublic:\n};\n",
			"hg/SportModeState_.hpp":      "class SportModeState_ {\nprivate:\n uint32_t fsm_id_ = 0;\n float task_time_ = 0.0f;\npublic:\n};\n",
		}
		for relative, contents := range headers {
			path := filepath.Join(sdkRoot, "include", "unitree", "idl", relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, pkg := range []string{"unitree_go", "unitree_hg"} {
			pkgRoot := filepath.Join(rosRoot, pkg)
			if err := os.MkdirAll(filepath.Join(pkgRoot, "msg"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmake := "rosidl_generate_interfaces(${PROJECT_NAME}\n)\n"
			if err := os.WriteFile(filepath.Join(pkgRoot, "CMakeLists.txt"), []byte(cmake), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		script := filepath.Join(root, "converter.py")
		if err := os.WriteFile(script, []byte(unitreeSDK2ToROSScript), 0o644); err != nil {
			t.Fatal(err)
		}
		return sdkRoot, rosRoot, script
	}

	t.Run("generates schemas from SDK headers", func(t *testing.T) {
		sdkRoot, rosRoot, script := createFixture(t, "std::string")
		cmd := exec.Command(python, script, "--sdk-root", sdkRoot, "--ros-root", rosRoot, "--source-commit", "test-commit")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("converter failed: %v\n%s", err, output)
		}
		checks := map[string]string{
			filepath.Join(rosRoot, "unitree_go", "msg", "ConfigChangeStatus.msg"): "string name\nstring content",
			filepath.Join(rosRoot, "unitree_hg", "msg", "AgvBmsState.msg"):        "int16[3] temperature\nbool is_charging",
			filepath.Join(rosRoot, "unitree_hg", "msg", "SportModeState.msg"):     "uint32 fsm_id\nfloat32 task_time",
		}
		for path, want := range checks {
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), want) {
				t.Fatalf("%s missing %q:\n%s", path, want, contents)
			}
		}
	})

	t.Run("rejects unknown SDK field types", func(t *testing.T) {
		sdkRoot, rosRoot, script := createFixture(t, "unitree_private::Unknown")
		cmd := exec.Command(python, script, "--sdk-root", sdkRoot, "--ros-root", rosRoot, "--source-commit", "test-commit")
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "unsupported Unitree SDK2 field type") {
			t.Fatalf("expected unsupported type failure, got %v:\n%s", err, output)
		}
	})
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
