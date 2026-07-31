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

func TestFindPipeWireCameraNode(t *testing.T) {
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
			if got := findPipeWireCameraNode([]byte(pwDump), tt.devicePath); got != tt.want {
				t.Errorf("findPipeWireCameraNode(%q) = %q, want %q", tt.devicePath, got, tt.want)
			}
		})
	}
}

func TestFindPipeWireCameraNode_MalformedDumpYieldsNoNode(t *testing.T) {
	for _, dump := range []string{"", "not json", "{}", "[]"} {
		if got := findPipeWireCameraNode([]byte(dump), "/dev/video0"); got != "" {
			t.Errorf("dump %q: got %q, want empty so the caller stays on direct V4L2", dump, got)
		}
	}
}

// A node.name is daemon-supplied, but it is interpolated into a pipeline string
// that is later split on whitespace — so a name carrying pipeline tokens must be
// dropped rather than passed through.
func TestFindPipeWireCameraNode_RejectsInjectableNodeName(t *testing.T) {
	hostile := `[{"info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
	                                 "node.name": "cam ! filesink location=/etc/passwd"}}}]`
	if got := findPipeWireCameraNode([]byte(hostile), "/dev/video0"); got != "" {
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
