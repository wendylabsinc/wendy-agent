package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
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
		"ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=8765 address:=127.0.0.1 include_hidden:=true",
		"message_backlog_size:=32",
		`^/front_camera/image/compressed$`,
		`^/hesai/points/preview$`,
		`^/collie/raw_scan$`,
		`^/lowstate$`,
		`^/uslam/frontend/odom$`,
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

func TestWriteFoxgloveAppCustomTopicWhitelist(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{
		domain:  0,
		rmw:     "rmw_cyclonedds_cpp",
		distro:  "humble",
		topics:  []string{`^/tf$`, `^/camera/.*$`, `^/quoted'path$`},
		backlog: 3,
	}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dfs := string(df)
	for _, want := range []string{`^/tf$`, `^/camera/.*$`, `^/quoted`, `message_backlog_size:=3`} {
		if !strings.Contains(dfs, want) {
			t.Fatalf("custom Foxglove Dockerfile missing %q:\n%s", want, dfs)
		}
	}
	if strings.Contains(dfs, `^/front_camera/image/compressed$`) {
		t.Fatalf("custom topics should replace bandwidth-safe defaults:\n%s", dfs)
	}
}

func TestWriteFoxgloveAppAllTopics(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble", allTopics: true}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), `topic_whitelist:='[\".*\"]'`) {
		t.Fatalf("--all-topics Dockerfile does not expose every topic:\n%s", df)
	}
}

func TestWriteFoxgloveAppRejectsConflictingTopicFlags(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{topics: []string{`^/tf$`}, allTopics: true}
	if err := writeFoxgloveApp(dir, opts); err == nil {
		t.Fatal("expected --topic with --all-topics to fail")
	}
}

func TestWriteFoxgloveAppBindsLoopback(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{domain: 0, rmw: "rmw_cyclonedds_cpp", distro: "humble", cloud: true}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dfs := string(df)
	if !strings.Contains(dfs, "address:=127.0.0.1") {
		t.Fatalf("Foxglove WebSocket is not bound to loopback:\n%s", dfs)
	}
	if strings.Contains(dfs, "address:=0.0.0.0") {
		t.Fatalf("Foxglove WebSocket unexpectedly exposed on every interface:\n%s", dfs)
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
		"--fps 8 --jpeg-quality 65 --max-width 960",
		"python3 /opt/wendy/hesai_preview_bridge.py",
		"CYCLONEDDS_HOME=/opt/cyclonedds",
		"/opt/ros/humble/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH)",
		"pip3 install --no-cache-dir cyclonedds==0.10.2",
		"pip3 install --no-cache-dir --no-deps .",
		"ros-humble-grid-map-msgs",
		"python3-pil",
		"--base-paths /tmp/unitree_ros2/cyclonedds_ws/src/unitree",
		"--packages-select unitree_api unitree_go unitree_hg",
		"--install-base /opt/unitree_msgs",
		"source /opt/unitree_msgs/setup.bash",
		"COPY hesai_preview_bridge.py /opt/wendy/hesai_preview_bridge.py",
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
		"1.0 / fps",
		"transcode_jpeg",
		"ImageFile.LOAD_TRUNCATED_IMAGES = True",
		"Could not transcode Go2 JPEG; dropping frame",
		`image.resize((max_width, height), resampling.BILINEAR)`,
		`quality=quality`,
	} {
		if !strings.Contains(string(cameraBridge), want) {
			t.Fatalf("Go2 camera bridge missing %q", want)
		}
	}
	hesaiBridge, err := os.ReadFile(filepath.Join(dir, "hesai_preview_bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`INPUT_TOPIC = "/hesai/points"`,
		`OUTPUT_TOPIC = "/hesai/points/preview"`,
		"MAX_FPS = 5.0",
		"POINT_STRIDE = 4",
		"numpy.frombuffer",
		"points[::POINT_STRIDE].tobytes()",
		"ReliabilityPolicy.BEST_EFFORT",
	} {
		if !strings.Contains(string(hesaiBridge), want) {
			t.Fatalf("Hesai preview bridge missing %q", want)
		}
	}
}

func TestWriteFoxgloveAppCustomCameraEncoding(t *testing.T) {
	dir := t.TempDir()
	opts := foxgloveServeOpts{
		domain:            0,
		rmw:               "rmw_cyclonedds_cpp",
		distro:            "humble",
		iface:             "enP8p1s0",
		unitree:           true,
		cameraFPS:         6,
		cameraJPEGQuality: 50,
		cameraMaxWidth:    640,
	}
	if err := writeFoxgloveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if !strings.Contains(string(df), "--fps 6 --jpeg-quality 50 --max-width 640") {
		t.Fatalf("custom camera encoding missing from Dockerfile:\n%s", df)
	}
}

func TestWriteFoxgloveAppRejectsInvalidCameraEncoding(t *testing.T) {
	for _, opts := range []foxgloveServeOpts{
		{cameraFPS: 31},
		{cameraJPEGQuality: 101},
		{cameraMaxWidth: 100},
	} {
		if err := writeFoxgloveApp(t.TempDir(), opts); err == nil {
			t.Fatalf("expected invalid camera encoding options to fail: %+v", opts)
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
	if _, err := os.Stat(filepath.Join(dir, "hesai_preview_bridge.py")); !os.IsNotExist(err) {
		t.Fatal("generic Foxglove app unexpectedly includes Hesai preview bridge")
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
