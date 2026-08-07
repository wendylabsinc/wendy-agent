package ipcam

import (
	"errors"
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
