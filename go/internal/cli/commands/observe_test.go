package commands

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveCommandShape(t *testing.T) {
	cmd := newObserveCmd()
	if cmd.RunE == nil {
		t.Fatal("wendy observe should start the default Observe session")
	}
	serve, _, err := cmd.Find([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if serve.RunE == nil {
		t.Fatal("wendy observe serve should be runnable")
	}
	for _, flag := range []string{
		"port", "domain", "rmw", "distro", "interface", "max-hz",
		"max-bandwidth", "point-stride", "jpeg-quality", "max-image-width",
	} {
		if cmd.PersistentFlags().Lookup(flag) == nil {
			t.Fatalf("observe command is missing --%s", flag)
		}
	}
}

func TestObserveRegisteredAtTopLevelAndDevice(t *testing.T) {
	root := NewRootCmd()
	if found, _, err := root.Find([]string{"observe"}); err != nil || found.Name() != "observe" {
		t.Fatalf("top-level observe command not registered: found=%v err=%v", found, err)
	}
	device := newDeviceCmd()
	if found, _, err := device.Find([]string{"observe"}); err != nil || found.Name() != "observe" {
		t.Fatalf("device observe command not registered: found=%v err=%v", found, err)
	}
}

func TestValidateObserveOpts(t *testing.T) {
	valid := observeOpts{
		localPort:       8780,
		maxHz:           10,
		bandwidthMbps:   8,
		pointStride:     4,
		jpegQuality:     65,
		maxWidth:        960,
		snapshotTimeout: 5,
	}
	if err := validateObserveOpts(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*observeOpts)
		want   string
	}{
		{"bandwidth", func(o *observeOpts) { o.bandwidthMbps = 0 }, "max-bandwidth"},
		{"rate", func(o *observeOpts) { o.maxHz = 121 }, "max-hz"},
		{"stride", func(o *observeOpts) { o.pointStride = 0 }, "point-stride"},
		{"quality", func(o *observeOpts) { o.jpegQuality = 101 }, "jpeg-quality"},
		{"interface", func(o *observeOpts) { o.iface = "bad interface" }, "network interface"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := valid
			test.mutate(&opts)
			if err := validateObserveOpts(opts); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateObserveOpts() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWriteObserveApp(t *testing.T) {
	dir := t.TempDir()
	opts := observeOpts{
		localPort:       8780,
		domain:          3,
		rmw:             "rmw_cyclonedds_cpp",
		distro:          "humble",
		iface:           "enP8p1s0",
		maxHz:           12,
		bandwidthMbps:   8,
		pointStride:     6,
		jpegQuality:     55,
		maxWidth:        800,
		snapshotTimeout: 7,
	}
	if err := writeObserveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, want := range []string{
		"COPY observe_gateway.py /opt/wendy/observe_gateway.py",
		"python3-aiohttp",
		"python3-numpy",
		"openssl req -x509",
		"ROS_LOCALHOST_ONLY=0",
		`NetworkInterface name=\"enP8p1s0\"`,
		"--max-hz 12",
		"--max-bytes-per-second 1000000",
		"--point-stride 6",
		"--jpeg-quality 55",
		"--max-width 800",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Observe Dockerfile missing %q:\n%s", want, text)
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("invalid Observe manifest: %v", err)
	}
	if manifest["appId"] != observeAppID {
		t.Fatalf("appId = %v", manifest["appId"])
	}
	if _, err := os.Stat(filepath.Join(dir, "observe_gateway.py")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteObserveAppUnitreeIncludesDemandCamera(t *testing.T) {
	dir := t.TempDir()
	opts := observeOpts{
		localPort:       8780,
		domain:          0,
		rmw:             "rmw_cyclonedds_cpp",
		distro:          "humble",
		iface:           "enP8p1s0",
		maxHz:           10,
		bandwidthMbps:   8,
		pointStride:     4,
		jpegQuality:     65,
		maxWidth:        960,
		snapshotTimeout: 5,
		unitree:         true,
	}
	if err := writeObserveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"UNITREE_ROS2_COMMIT",
		"--packages-select unitree_api unitree_go unitree_hg",
		"COPY go2_camera_bridge.py /opt/wendy/go2_camera_bridge.py",
		"source /opt/unitree_msgs/setup.bash",
		"python3 /opt/wendy/go2_camera_bridge.py",
	} {
		if !strings.Contains(string(dockerfile), want) {
			t.Fatalf("Unitree Observe Dockerfile missing %q", want)
		}
	}
	cameraBridge, err := os.ReadFile(filepath.Join(dir, "go2_camera_bridge.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"publisher.get_subscription_count() > 0",
		"Stopped Go2 VideoClient",
		"restarting VideoClient",
	} {
		if !strings.Contains(string(cameraBridge), want) {
			t.Fatalf("Unitree Observe camera bridge missing %q", want)
		}
	}
}

func TestObserveGatewayProtocolHelpers(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	script := filepath.Join("observe_gateway.py")
	program := `
import argparse, importlib.util, json, struct, sys
spec = importlib.util.spec_from_file_location("observe_gateway", sys.argv[1])
module = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = module
spec.loader.exec_module(module)
limits = argparse.Namespace(max_hz=10.0, max_bytes_per_second=1000000, point_stride=4, jpeg_quality=65, max_width=960)
stream = module.clamp_spec({"id":"cloud","topic":"/hesai/points","profile":"pointcloud","max_hz":50,"point_stride":1}, limits)
assert stream.max_hz == 10.0
assert stream.point_stride == 4
frame = module.ProcessedFrame(b"payload", "cdr", "pointcloud", 123)
packed = module.pack_frame(stream, "sensor_msgs/msg/PointCloud2", frame, 2)
body_len, header_len = struct.unpack(">II", packed[:8])
assert body_len == len(packed) - 4
header = json.loads(packed[8:8+header_len])
assert header["stream_id"] == "cloud" and header["dropped"] == 2
assert packed[8+header_len:] == b"payload"
print("ok")
`
	cmd := exec.Command(python, "-c", program, script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Observe gateway helper test failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("unexpected helper output %q", output)
	}
}

func TestObserveGatewayDemandAndTransportContractPresent(t *testing.T) {
	for _, want := range []string{
		`add_get("/v1/live", websocket_handler)`,
		`add_get("/v1/stream", https_stream_handler)`,
		`add_get("/v1/snapshot", snapshot_handler)`,
		"asyncio.Queue(maxsize=1)",
		"destroy_subscription",
		"session_budget",
		"downsample_pointcloud",
		"image_to_jpeg",
		`"transports": ["websocket", "https"]`,
	} {
		if !strings.Contains(observeGatewayScript, want) {
			t.Fatalf("Observe gateway missing %q", want)
		}
	}
}

// Opt-in because it pulls the ROS base image and Ubuntu packages. This catches
// package-name and generated-Dockerfile drift without making ordinary unit
// tests depend on a container engine or the network.
func TestObserveGeneratedImageBuilds(t *testing.T) {
	if os.Getenv("WENDY_TEST_OBSERVE_IMAGE") != "1" {
		t.Skip("set WENDY_TEST_OBSERVE_IMAGE=1 to build the generated image")
	}
	var buildCommand func(string) *exec.Cmd
	if _, err := exec.LookPath("docker"); err == nil && exec.Command("docker", "info").Run() == nil {
		buildCommand = func(dir string) *exec.Cmd {
			cmd := exec.Command("docker", "build", "-t", "wendy-observe-test", ".")
			cmd.Dir = dir
			return cmd
		}
	} else if _, err := exec.LookPath("container"); err == nil && exec.Command("container", "system", "status").Run() == nil {
		buildCommand = func(dir string) *exec.Cmd {
			cmd := exec.Command("container", "build", "--progress", "plain", "-t", "wendy-observe-test", ".")
			cmd.Dir = dir
			return cmd
		}
	} else {
		t.Skip("no running Docker or Apple Container builder")
	}
	dir := t.TempDir()
	opts := observeOpts{
		localPort:       8780,
		domain:          0,
		rmw:             "rmw_cyclonedds_cpp",
		distro:          "humble",
		maxHz:           10,
		bandwidthMbps:   8,
		pointStride:     4,
		jpegQuality:     65,
		maxWidth:        960,
		snapshotTimeout: 5,
	}
	if err := writeObserveApp(dir, opts); err != nil {
		t.Fatal(err)
	}
	cmd := buildCommand(dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building generated Observe image: %v\n%s", err, output)
	}
}
