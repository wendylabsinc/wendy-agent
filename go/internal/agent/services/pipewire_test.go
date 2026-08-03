package services

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/camera"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// pwDump is a trimmed pw-dump capture from an AGX Thor with a Logitech BRIO:
// two V4L2 sources, one libcamera device, and the ALSA node that must not match.
const pwDump = `[
  {"id": 31, "type": "PipeWire:Interface:Device",
   "info": {"props": {"media.class": "Video/Device", "api.v4l2.path": "/dev/video0"}}},
  {"id": 32, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
                      "node.name": "v4l2_input.platform-a80aa10000.usb-usb-0_3.1_1.0"}}},
  {"id": 64, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video2",
                      "node.name": "v4l2_input.platform-a80aa10000.usb-usb-0_3.1_1.0.3"}}},
  {"id": 51, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Audio/Source",
                      "node.name": "alsa_input.platform-sound.analog-stereo"}}}
]`

func TestFindPipeWireCamera(t *testing.T) {
	tests := []struct {
		name       string
		devicePath string
		want       string
	}{
		{"first camera", "/dev/video0", "v4l2_input.platform-a80aa10000.usb-usb-0_3.1_1.0"},
		{"second camera", "/dev/video2", "v4l2_input.platform-a80aa10000.usb-usb-0_3.1_1.0.3"},
		// /dev/video1 is a Video/Device entry only: the kernel exposes metadata
		// nodes alongside capture nodes, and only the latter can be streamed.
		{"device without a source node", "/dev/video1", ""},
		{"absent device", "/dev/video9", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findPipeWireCamera([]byte(pwDump), tt.devicePath); got.node != tt.want {
				t.Errorf("findPipeWireCamera(%q).node = %q, want %q", tt.devicePath, got.node, tt.want)
			}
		})
	}
}

func TestFindPipeWireCamera_MalformedDumpYieldsNoNode(t *testing.T) {
	for _, dump := range []string{"", "not json", "{}", "[]"} {
		if got := findPipeWireCamera([]byte(dump), "/dev/video0").node; got != "" {
			t.Errorf("dump %q: got %q, want empty so the caller stays on direct V4L2", dump, got)
		}
	}
}

// A node.name is daemon-supplied, but it is interpolated into a pipeline string
// that is later split on whitespace — so a name carrying pipeline tokens must be
// dropped rather than passed through.
func TestFindPipeWireCamera_RejectsInjectableNodeName(t *testing.T) {
	hostile := `[{"info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
	                                 "node.name": "cam ! filesink location=/etc/passwd"}}}]`
	if got := findPipeWireCamera([]byte(hostile), "/dev/video0").node; got != "" {
		t.Errorf("hostile node name must be rejected, got %q", got)
	}
}

func TestBuildSourceElement_PrefersPipeWireForUSB(t *testing.T) {
	src := buildSourceElement(captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportUSB,
		pipewireNode: "v4l2_input.usb-046d_085e",
	}, defaultElements())
	if src != "pipewiresrc target-object=v4l2_input.usb-046d_085e" {
		t.Errorf("USB camera with a PipeWire node must use pipewiresrc, got %q", src)
	}
}

func TestBuildSourceElement_FallsBackToV4L2WithoutPipeWire(t *testing.T) {
	src := buildSourceElement(captureSource{
		devicePath: "/dev/video0",
		transport:  camera.TransportUSB,
	}, defaultElements())
	if src != "v4l2src device=/dev/video0" {
		t.Errorf("no PipeWire node must leave the direct V4L2 source in place, got %q", src)
	}
}

// On CSI the ISP source is the only one that produces usable frames, so a
// PipeWire node for the same device must not displace it.
func TestBuildSourceElement_CSIKeepsLibcamerasrc(t *testing.T) {
	src := buildSourceElement(captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportCSI,
		libcameraID:  "/base/soc/i2c0mux/imx708@1a",
		pipewireNode: "v4l2_input.platform-csi",
	}, defaultElements())
	if !strings.HasPrefix(src, "libcamerasrc") {
		t.Errorf("CSI must stay on libcamerasrc, got %q", src)
	}
}

func TestBuildGStreamerArgs_PipeWireSourceReplacesV4L2(t *testing.T) {
	args, err := buildGStreamerArgs("gst", captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportUSB,
		pipewireNode: "v4l2_input.usb-046d_085e",
	}, &agentpb.StreamVideoRequest{}, "x264enc", true, defaultElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "pipewiresrc target-object=v4l2_input.usb-046d_085e") {
		t.Errorf("pipeline must source from PipeWire: %v", args)
	}
	if strings.Contains(joined, "v4l2src") {
		t.Errorf("pipeline must not also open the device directly: %v", args)
	}
}

func TestPipeWireEnv_SetsRuntimeDirUnlessOverridden(t *testing.T) {
	t.Setenv("PIPEWIRE_RUNTIME_DIR", "")
	if !contains(pipewireEnv(), "PIPEWIRE_RUNTIME_DIR="+pipewireRuntimeDir) {
		t.Error("agent-spawned clients must be pointed at the system-wide socket")
	}

	t.Setenv("PIPEWIRE_RUNTIME_DIR", "/run/user/1000")
	if contains(pipewireEnv(), "PIPEWIRE_RUNTIME_DIR="+pipewireRuntimeDir) {
		t.Error("an operator override of PIPEWIRE_RUNTIME_DIR must win")
	}
}

func contains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// The node's state is what says whether anything else is reading the camera, and
// so whether the graph is worth the software re-encode it costs.
func TestFindPipeWireCamera_InUseFromNodeState(t *testing.T) {
	dump := func(state string) string {
		return `[{"info": {"state": "` + state + `",
		           "props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
		                     "node.name": "v4l2_input.usb"}}}]`
	}
	tests := []struct {
		state string
		want  bool
	}{
		{"running", true},
		{"idle", false},
		{"suspended", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			if got := findPipeWireCamera([]byte(dump(tt.state)), "/dev/video0"); got.inUse != tt.want {
				t.Errorf("state %q: inUse = %v, want %v", tt.state, got.inUse, tt.want)
			}
		})
	}
}

// pipewiresrc lives in a separate package from the daemon, so a host can have a
// node and no element. Building a pipeline around it would fail outright.
func TestBuildSourceElement_NoPipeWirePluginFallsBackToV4L2(t *testing.T) {
	elements := defaultElements()
	delete(elements, "pipewiresrc")

	src := buildSourceElement(captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportUSB,
		pipewireNode: "v4l2_input.usb-046d_085e",
	}, elements)
	if src != "v4l2src device=/dev/video0" {
		t.Errorf("missing pipewiresrc must degrade to v4l2src, got %q", src)
	}
}

// A framerate in the source capsfilter kills the PipeWire path: with an encoder
// downstream also constraining negotiation, pipewiresrc asks PipeWire for a
// format the camera cannot deliver ("set output format: -22"). Reproduced on an
// AGX Thor with a Logitech BRIO — the dashboard sends a framerate, the CLI does
// not, which is why only the dashboard stream failed.
func TestBuildGStreamerArgs_PipeWireRateGoesThroughVideorate(t *testing.T) {
	args, err := buildGStreamerArgs("gst", captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportUSB,
		pipewireNode: "v4l2_input.usb-046d_085e",
	}, &agentpb.StreamVideoRequest{Framerate: 30}, "x264enc", true, defaultElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "videorate max-rate=30") {
		t.Errorf("requested rate must be capped downstream: %v", args)
	}
	if strings.Contains(joined, "framerate=30/1") {
		t.Errorf("framerate must not reach the source capsfilter: %v", args)
	}
}

// The direct V4L2 path negotiates a framerate fine and must keep doing so.
func TestBuildGStreamerArgs_V4L2KeepsFramerateInCaps(t *testing.T) {
	args, err := buildGStreamerArgs("gst", captureSource{
		devicePath: "/dev/video0",
		transport:  camera.TransportUSB,
	}, &agentpb.StreamVideoRequest{Framerate: 30}, "x264enc", true, defaultElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "framerate=30/1") {
		t.Errorf("v4l2src must still pin the framerate in caps: %v", args)
	}
	if strings.Contains(joined, "videorate") {
		t.Errorf("v4l2src needs no videorate stage: %v", args)
	}
}

// Dimensions still belong in the source caps on the PipeWire path.
func TestBuildGStreamerArgs_PipeWireKeepsDimensionsInCaps(t *testing.T) {
	args, err := buildGStreamerArgs("gst", captureSource{
		devicePath:   "/dev/video0",
		transport:    camera.TransportUSB,
		pipewireNode: "v4l2_input.usb-046d_085e",
	}, &agentpb.StreamVideoRequest{Width: 640, Height: 480, Framerate: 30}, "x264enc", true, defaultElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "video/x-raw,width=640,height=480") {
		t.Errorf("dimensions must stay in the source caps: %v", args)
	}
	if !strings.Contains(joined, "videorate max-rate=30") {
		t.Errorf("rate must still go through videorate: %v", args)
	}
}
