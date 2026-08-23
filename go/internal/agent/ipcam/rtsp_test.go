package ipcam

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func testCamera() Camera {
	return Camera{
		MAC:        "ec:71:db:2a:ae:7e",
		ID:         200,
		Address:    "10.98.0.50",
		StreamMain: "/h264Preview_01_main",
		StreamSub:  "/h264Preview_01_sub",
	}
}

func TestStreamURLPrefersSubForAuto(t *testing.T) {
	got, err := StreamURL(testCamera(), Credential{Username: "admin", Password: "p"}, StreamAuto)
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	want := "rtsp://admin:p@10.98.0.50:554/h264Preview_01_sub"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestStreamURLMain(t *testing.T) {
	got, err := StreamURL(testCamera(), Credential{Username: "admin", Password: "p"}, StreamMainOnly)
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if !strings.HasSuffix(got, "/h264Preview_01_main") {
		t.Fatalf("url = %q, want the main stream path", got)
	}
}

// A password containing URL-significant characters must not be able to change
// which host the pipeline connects to.
func TestStreamURLEncodesCredentials(t *testing.T) {
	got, err := StreamURL(testCamera(), Credential{Username: "ad@min", Password: "p@ss:w/rd"}, StreamSubOnly)
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if !strings.Contains(got, "ad%40min:p%40ss%3Aw%2Frd@10.98.0.50:554") {
		t.Fatalf("credentials not encoded: %q", got)
	}
	if strings.Count(got, "@") != 1 {
		t.Fatalf("url has an ambiguous authority: %q", got)
	}
}

// Cameras discovered by lease alone have no known stream paths yet, so fall back
// to the widely-implemented default rather than refusing to stream.
func TestStreamURLFallsBackToDefaultPath(t *testing.T) {
	c := testCamera()
	c.StreamSub, c.StreamMain = "", ""
	got, err := StreamURL(c, Credential{Username: "admin", Password: "p"}, StreamAuto)
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if !strings.HasSuffix(got, defaultSubPath) {
		t.Fatalf("url = %q, want the default sub path %q", got, defaultSubPath)
	}

	got, err = StreamURL(c, Credential{Username: "admin", Password: "p"}, StreamMainOnly)
	if err != nil {
		t.Fatalf("StreamURL main: %v", err)
	}
	if !strings.HasSuffix(got, defaultMainPath) {
		t.Fatalf("url = %q, want the default main path %q", got, defaultMainPath)
	}
}

// A camera stream path stored without a leading slash must still produce a valid
// URL rather than gluing the path onto the port.
func TestStreamURLNormalisesPath(t *testing.T) {
	c := testCamera()
	c.StreamSub = "h264Preview_01_sub"
	got, err := StreamURL(c, Credential{Username: "admin", Password: "p"}, StreamSubOnly)
	if err != nil {
		t.Fatalf("StreamURL: %v", err)
	}
	if !strings.Contains(got, ":554/h264Preview_01_sub") {
		t.Fatalf("url = %q, want a slash before the path", got)
	}
}

func TestStreamURLRequiresAddress(t *testing.T) {
	c := testCamera()
	c.Address = ""
	if _, err := StreamURL(c, Credential{Username: "admin", Password: "p"}, StreamAuto); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("err = %v, want ErrNoAddress", err)
	}
}

// Nothing reaching a log or an error message may carry the password.
func TestRedactURL(t *testing.T) {
	got := RedactURL("rtsp://admin:hunter2@10.98.0.50:554/h264Preview_01_sub")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("redacted url still contains the password: %q", got)
	}
	if !strings.Contains(got, "10.98.0.50:554") {
		t.Fatalf("redacted url lost the address: %q", got)
	}
	// A URL with no credentials passes through unchanged.
	plain := "rtsp://10.98.0.50:554/stream"
	if RedactURL(plain) != plain {
		t.Fatalf("plain url altered: %q", RedactURL(plain))
	}
	// Unparseable input is not echoed back: it may itself be a credential we
	// failed to understand.
	if got := RedactURL("rtsp://ad min:p@ss@:99999/x\x7f"); strings.Contains(got, "p@ss") {
		t.Fatalf("unparseable url echoed a secret: %q", got)
	}
}

// The pipeline must depayload rather than re-encode: the camera already sends
// H.264, and an Orin Nano should not spend a core undoing that.
func TestPipelineArgsDepayloadsWithoutTranscode(t *testing.T) {
	args := strings.Join(PipelineArgs("rtsp://admin:p@10.98.0.50:554/x"), " ")
	for _, want := range []string{"rtspsrc", "rtph264depay", "h264parse", "config-interval=-1", "fdsink"} {
		if !strings.Contains(args, want) {
			t.Fatalf("pipeline missing %q: %s", want, args)
		}
	}
	for _, unwanted := range []string{"x264enc", "avdec_h264", "videoconvert", "nvv4l2h264enc"} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("pipeline transcodes (%s): %s", unwanted, args)
		}
	}
	// Interleaved TCP avoids UDP packet loss showing up as decode corruption.
	if !strings.Contains(args, "protocols=tcp") {
		t.Fatalf("pipeline does not force TCP: %s", args)
	}
}

// Unlike PipelineArgs, the loopback pipeline decodes: a v4l2loopback CAPTURE
// side needs raw frames, since nothing consuming it as a normal camera device
// can be expected to speak compressed H.264.
func TestLoopbackPipelineArgsDecodesToDevice(t *testing.T) {
	args := strings.Join(LoopbackPipelineArgs("rtsp://admin:p@10.98.0.50:554/x", "/dev/video203"), " ")
	for _, want := range []string{"rtspsrc", "rtph264depay", "h264parse", "config-interval=-1", "avdec_h264", "videoconvert", "v4l2sink", "device=/dev/video203"} {
		if !strings.Contains(args, want) {
			t.Fatalf("pipeline missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "fdsink") {
		t.Fatalf("loopback pipeline has an fd sink; the v4l2loopback node is the sink: %s", args)
	}
	if !strings.Contains(args, "protocols=tcp") {
		t.Fatalf("pipeline does not force TCP: %s", args)
	}
}

// The loopback pipeline's location= token must redact exactly like
// PipelineArgs's does: this is the one place SecretsIn/RedactText have to
// keep working for Task C3's supervisor to log a pump failure safely.
func TestLoopbackPipelineArgsRedactsCredentials(t *testing.T) {
	args := LoopbackPipelineArgs("rtsp://admin:hunter2@10.98.0.50:554/h264Preview_01_sub", "/dev/video203")

	diagnostic := "ERROR from element rtspsrc0: Could not open resource for reading and writing.\n" +
		"gstrtspsrc.c(9105): gst_rtspsrc_retrieve_sdp (): location=rtsp://admin:hunter2@10.98.0.50:554/h264Preview_01_sub\n" +
		"Failed to connect. (Timeout while waiting for server response)"

	got := RedactText(diagnostic, SecretsIn(args)...)

	if strings.Contains(got, "hunter2") {
		t.Fatalf("password survived redaction: %s", got)
	}
	if !strings.Contains(got, "Failed to connect") || !strings.Contains(got, "10.98.0.50") {
		t.Fatalf("redaction destroyed the diagnostic: %s", got)
	}
}

func TestChooseStream(t *testing.T) {
	cases := []struct {
		width uint32
		want  StreamChoice
	}{
		{0, StreamAuto},
		{640, StreamSubOnly},
		{1024, StreamSubOnly},
		{1280, StreamMainOnly},
		{2560, StreamMainOnly},
	}
	for _, tc := range cases {
		if got := ChooseStream(tc.width); got != tc.want {
			t.Fatalf("ChooseStream(%d) = %v, want %v", tc.width, got, tc.want)
		}
	}
}

func TestFormatUnreachable(t *testing.T) {
	c := testCamera()
	if got := FormatUnreachable(c); !strings.Contains(got, "never been reached") {
		t.Fatalf("message for a never-seen camera = %q", got)
	}
	c.LastSeen = time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC)
	got := FormatUnreachable(c)
	if !strings.Contains(got, "10.98.0.50") || !strings.Contains(got, "2026-08-05") {
		t.Fatalf("message = %q, want the address and when it was last seen", got)
	}
}

// A GStreamer diagnostic is worth showing, but it echoes the location property
// back, and that property carries the camera password.
func TestRedactTextRemovesCredentialsFromDiagnostics(t *testing.T) {
	args := PipelineArgs("rtsp://admin:hunter2@10.98.0.50:554/h264Preview_01_sub")
	diagnostic := "ERROR from element rtspsrc0: Could not open resource for reading and writing.\n" +
		"gstrtspsrc.c(9105): gst_rtspsrc_retrieve_sdp (): location=rtsp://admin:hunter2@10.98.0.50:554/h264Preview_01_sub\n" +
		"Failed to connect. (Timeout while waiting for server response)"

	got := RedactText(diagnostic, SecretsIn(args)...)

	if strings.Contains(got, "hunter2") {
		t.Fatalf("password survived redaction: %s", got)
	}
	// The part that makes the diagnostic worth keeping must survive.
	if !strings.Contains(got, "Failed to connect") || !strings.Contains(got, "10.98.0.50") {
		t.Fatalf("redaction destroyed the diagnostic: %s", got)
	}
}

// A password containing reserved characters appears percent-encoded in the
// pipeline, so the literal pass alone would miss it and the URL pass alone would
// miss any copy outside a URL. Both spellings must go.
func TestRedactTextRemovesEncodedAndBareCredentials(t *testing.T) {
	secret := "p@ss:word/1"
	args := PipelineArgs("rtsp://" + url.UserPassword("admin", secret).String() + "@10.98.0.50:554/x")
	encoded := url.UserPassword("admin", secret).String()
	diagnostic := "location=rtsp://" + encoded + "@10.98.0.50:554/x and a bare copy " + secret

	got := RedactText(diagnostic, SecretsIn(args)...)

	if strings.Contains(got, secret) || strings.Contains(got, encoded) {
		t.Fatalf("credential survived redaction: %s", got)
	}
}

// Redaction must not depend on this package having built the URL: a library can
// echo back a form we never produced.
func TestRedactTextRemovesUnknownURLCredentials(t *testing.T) {
	got := RedactText("failed on rtsp://someone:secret@10.98.0.50:554/x")
	if strings.Contains(got, "secret") {
		t.Fatalf("credential survived redaction: %s", got)
	}
	if !strings.Contains(got, "10.98.0.50") {
		t.Fatalf("host was destroyed: %s", got)
	}
}

func TestSecretsInIgnoresPipelineWithoutLocation(t *testing.T) {
	if got := SecretsIn([]string{"rtspsrc", "protocols=tcp", "!", "fdsink", "fd=1"}); len(got) != 0 {
		t.Fatalf("SecretsIn = %v, want nothing", got)
	}
}

func TestSegmentIndexFor(t *testing.T) {
	for _, tc := range []struct {
		address string
		index   int
		ok      bool
	}{
		{"10.98.0.50", 0, true},
		{"10.98.3.50", 3, true},
		{"10.98.255.1", 255, true},
		{"192.168.2.69", 0, false},
		{"10.99.0.50", 0, false},
		{"", 0, false},
	} {
		index, ok := SegmentIndexFor(net.ParseIP(tc.address))
		if ok != tc.ok || index != tc.index {
			t.Errorf("SegmentIndexFor(%q) = %d, %v; want %d, %v", tc.address, index, ok, tc.index, tc.ok)
		}
	}
}

// The relay must not pace or buffer the stream on its own account. Both of these
// were costing a frame's worth of delay or more on every frame, on a path whose
// only job is to move bytes.
func TestPipelineArgsAddsNoLatencyOfItsOwn(t *testing.T) {
	args := PipelineArgs("rtsp://admin:p@10.98.0.50:554/x")
	joined := strings.Join(args, " ")

	// A jitter buffer only earns its delay on a reordering transport, and this
	// pipeline forces TCP.
	if !strings.Contains(joined, "latency=0") {
		t.Errorf("jitter buffer adds delay on an ordered transport: %s", joined)
	}
	// fdsink is a GstBaseSink, so without this it holds every buffer until its
	// presentation time — clock-pacing a relay.
	fdsinkAt := -1
	for i, arg := range args {
		if arg == "fdsink" {
			fdsinkAt = i
		}
	}
	if fdsinkAt < 0 {
		t.Fatalf("pipeline has no fdsink: %s", joined)
	}
	if !slices.Contains(args[fdsinkAt:], "sync=false") {
		t.Errorf("fdsink paces output to the clock: %s", joined)
	}
}
