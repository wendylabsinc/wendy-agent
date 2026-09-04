package services

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/camera"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const testPWSerial uint64 = 68

// pipewireElements is defaultElements plus the GStreamer PipeWire plugin, which
// ships in its own package (gstreamer1.0-pipewire) and so must be probed for.
func pipewireElements() map[string]bool {
	e := defaultElements()
	e["pipewiresrc"] = true
	return e
}

func TestBuildSourceElement_UsesPipeWireNodeWhenPresent(t *testing.T) {
	src := buildSourceElement("/dev/video9", camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if src != fmt.Sprintf("pipewiresrc target-object=%d", testPWSerial) {
		t.Errorf("expected pipewiresrc on the node, got %q", src)
	}
}

func TestBuildSourceElement_NoNodeKeepsV4L2(t *testing.T) {
	src := buildSourceElement("/dev/video9", camera.TransportUSB, "", pipeWireSource{}, pipewireElements())
	if src != "v4l2src device=/dev/video9" {
		t.Errorf("no node must leave the direct source in place, got %q", src)
	}
}

// On CSI the ISP source is the only one producing usable frames, so a node for
// the same device must not displace it.
func TestBuildSourceElement_CSIKeepsLibcamerasrc(t *testing.T) {
	src := buildSourceElement("/dev/video9", camera.TransportCSI, "/base/soc/i2c0mux/imx708@1a", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if !strings.HasPrefix(src, "libcamerasrc") {
		t.Errorf("CSI must stay on libcamerasrc, got %q", src)
	}
}

// Pinning a dimension on pipewiresrc trips "assertion 'gst_caps_is_fixed' failed" once
// another client is streaming, so size goes through videoscale and rate through videorate.
func TestBuildGStreamerArgs_PipeWireConvertsInsteadOfConstraining(t *testing.T) {
	args, err := buildGStreamerArgs("gst", "/dev/video9",
		&agentpb.StreamVideoRequest{Width: 640, Height: 480, Framerate: 30},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "videoscale ! video/x-raw,width=640,height=480") {
		t.Errorf("size must go through videoscale: %v", args)
	}
	if !strings.Contains(joined, "videorate max-rate=30") {
		t.Errorf("rate must go through videorate: %v", args)
	}
	if strings.Contains(joined, fmt.Sprintf("pipewiresrc target-object=%d", testPWSerial)+" ! video/x-raw") {
		t.Errorf("no capsfilter may sit directly on pipewiresrc: %v", args)
	}
	if strings.Contains(joined, "framerate=30/1") {
		t.Errorf("framerate must not reach a source capsfilter: %v", args)
	}
}

// With nothing requested the source is left completely unconstrained.
func TestBuildGStreamerArgs_PipeWireDefaultsAddNoCaps(t *testing.T) {
	withMJPEGProbe(t, func(string, uint32, uint32) bool { return true })

	args, err := buildGStreamerArgs("gst", "/dev/video9", &agentpb.StreamVideoRequest{},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "videoscale") || strings.Contains(joined, "videorate") {
		t.Errorf("nothing was requested, so nothing should be converted: %v", args)
	}
	if strings.Contains(joined, "width=") || strings.Contains(joined, "height=") {
		t.Errorf("no size may be pinned on the graph: %v", args)
	}
}

// A backlog must be shed before any scale work is spent on it.
func TestBuildGStreamerArgs_PipeWireQueuesBeforeConverting(t *testing.T) {
	args, err := buildGStreamerArgs("gst", "/dev/video9",
		&agentpb.StreamVideoRequest{Width: 640, Height: 480, Framerate: 30},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Index(joined, "leaky=downstream") > strings.Index(joined, "videoscale") {
		t.Errorf("the leaky queue must precede videoscale: %v", args)
	}
}

// PipeWire's V4L2 source decodes MJPEG itself and publishes raw video, so an
// image/jpeg capsfilter on pipewiresrc cannot negotiate.
func TestBuildGStreamerArgs_PipeWireSkipsMJPEG(t *testing.T) {
	withMJPEGProbe(t, func(string, uint32, uint32) bool { return true })

	elements := pipewireElements()
	elements["jpegdec"] = true
	args, err := buildGStreamerArgs("gst", "/dev/video9",
		&agentpb.StreamVideoRequest{Width: 1280, Height: 720},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, elements)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "image/jpeg") || strings.Contains(joined, "jpegdec") {
		t.Errorf("pipewiresrc cannot deliver MJPEG: %v", args)
	}
}

// The direct V4L2 path negotiates size and rate fine and must keep doing so.
func TestBuildGStreamerArgs_V4L2KeepsCapsBehaviour(t *testing.T) {
	withMJPEGProbe(t, func(string, uint32, uint32) bool { return false })

	args, err := buildGStreamerArgs("gst", "/dev/video9",
		&agentpb.StreamVideoRequest{Width: 640, Height: 480, Framerate: 30},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{}, pipewireElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "video/x-raw,width=640,height=480,framerate=30/1") {
		t.Errorf("v4l2src must still pin size and rate in caps: %v", args)
	}
	if strings.Contains(joined, "videoscale") || strings.Contains(joined, "videorate") {
		t.Errorf("v4l2src needs no conversion stages: %v", args)
	}
}

// The graph serves one format per camera, picked by whichever client got there
// first. When that is MJPEG the type must be named and decoded — videoconvert
// cannot take compressed input, and unnamed caps trip pipewiresrc's assertion.
func TestBuildGStreamerArgs_PipeWireDecodesMJPEGNode(t *testing.T) {
	elements := pipewireElements()
	elements["jpegdec"] = true
	args, err := buildGStreamerArgs("gst", "/dev/video9", &agentpb.StreamVideoRequest{},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial, jpeg: true}, elements)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "image/jpeg") || !strings.Contains(joined, "jpegdec") {
		t.Errorf("an MJPEG node must be named and decoded: %v", args)
	}
}

// jpeg describes a graph node, so it must not reach a pipeline that ended up on
// another source: a jpeg capsfilter behind libcamerasrc cannot link, and the raw
// side would lose the leaky queue that sheds its backlog.
func TestBuildGStreamerArgs_JPEGNodeDoesNotSteerAnotherSource(t *testing.T) {
	elements := pipewireElements()
	elements["jpegdec"] = true
	args, err := buildGStreamerArgs("gst", "/dev/video9", &agentpb.StreamVideoRequest{},
		"x264enc", true, camera.TransportCSI, "/base/soc/i2c0mux/imx708@1a",
		pipeWireSource{serial: testPWSerial, jpeg: true}, elements)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "image/jpeg") || strings.Contains(joined, "jpegdec") {
		t.Errorf("libcamerasrc serves raw; no jpeg stage may appear: %v", args)
	}
	if !strings.Contains(joined, "leaky=downstream") {
		t.Errorf("the raw path must keep its leaky queue: %v", args)
	}
}

// videorate drops frames videoscale would otherwise resize for nothing.
func TestBuildGStreamerArgs_PipeWireRatesBeforeScaling(t *testing.T) {
	args, err := buildGStreamerArgs("gst", "/dev/video9",
		&agentpb.StreamVideoRequest{Width: 640, Height: 480, Framerate: 15},
		"x264enc", true, camera.TransportUSB, "", pipeWireSource{serial: testPWSerial}, pipewireElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Index(joined, "videorate") > strings.Index(joined, "videoscale") {
		t.Errorf("videorate must precede videoscale: %v", args)
	}
}
