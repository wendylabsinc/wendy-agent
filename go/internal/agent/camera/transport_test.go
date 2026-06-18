package camera

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withFakeSysfs(t *testing.T, drivers map[string]string, ofNode map[string]bool) {
	t.Helper()
	origRead := readDriverSymlink
	origStat := statPath
	t.Cleanup(func() {
		readDriverSymlink = origRead
		statPath = origStat
	})
	readDriverSymlink = func(path string) (string, error) {
		// path is /sys/class/video4linux/<base>/device/driver
		base := filepath.Base(filepath.Dir(filepath.Dir(path)))
		drv, ok := drivers[base]
		if !ok {
			return "", errors.New("no link")
		}
		return "/sys/bus/.../drivers/" + drv, nil
	}
	statPath = func(path string) error {
		base := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if ofNode[base] {
			return nil
		}
		return errors.New("not exist")
	}
}

func TestClassify_USB_UVC(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "uvcvideo"}, nil)
	got, drv := Classify("video0")
	if got != TransportUSB || drv != "uvcvideo" {
		t.Fatalf("got (%v, %q), want (USB, uvcvideo)", got, drv)
	}
}

func TestClassify_CSI_TegraCapture(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "tegra-capture-vi"}, nil)
	got, drv := Classify("video0")
	if got != TransportCSI || drv != "tegra-capture-vi" {
		t.Fatalf("got (%v, %q), want (CSI, tegra-capture-vi)", got, drv)
	}
}

func TestClassify_CSI_Unicam(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "unicam"}, nil)
	got, _ := Classify("video0")
	if got != TransportCSI {
		t.Fatalf("got %v, want CSI", got)
	}
}

func TestClassify_CSI_Bcm2835Unicam(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "bcm2835-unicam"}, nil)
	got, _ := Classify("video0")
	if got != TransportCSI {
		t.Fatalf("got %v, want CSI", got)
	}
}

func TestClassify_CSI_SensorDriverImx(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "imx477"}, nil)
	got, _ := Classify("video0")
	if got != TransportCSI {
		t.Fatalf("got %v, want CSI", got)
	}
}

func TestClassify_NonCamera_Bcm2835Isp(t *testing.T) {
	// The bcm2835-isp m2m capture nodes (bcm2835-isp-capture0/1) advertise
	// VIDEO_CAPTURE but are the legacy Pi 0-4 ISP output, not a sensor capture
	// source. They must NOT be classified as CSI cameras.
	withFakeSysfs(t, map[string]string{"video14": "bcm2835-isp"}, nil)
	got, drv := Classify("video14")
	if got != TransportUnknown || drv != "bcm2835-isp" {
		t.Fatalf("got (%v, %q), want (Unknown, bcm2835-isp)", got, drv)
	}
}

func TestClassify_NonCamera_Bcm2835Codec(t *testing.T) {
	// The bcm2835-codec H.264 encode/decode m2m nodes are not cameras either.
	withFakeSysfs(t, map[string]string{"video10": "bcm2835-codec"}, nil)
	got, drv := Classify("video10")
	if got != TransportUnknown || drv != "bcm2835-codec" {
		t.Fatalf("got (%v, %q), want (Unknown, bcm2835-codec)", got, drv)
	}
}

func TestClassify_NonCamera_IspNotReclassifiedByOfNode(t *testing.T) {
	// Even when the ISP platform device carries a device-tree of_node, the
	// non-camera denylist must win over the of_node CSI fallback.
	withFakeSysfs(t, map[string]string{"video14": "bcm2835-isp"}, map[string]bool{"video14": true})
	got, drv := Classify("video14")
	if got != TransportUnknown || drv != "bcm2835-isp" {
		t.Fatalf("got (%v, %q), want (Unknown, bcm2835-isp)", got, drv)
	}
}

func TestIsNonCameraDriver(t *testing.T) {
	nonCamera := []string{"bcm2835-isp", "bcm2835-codec"}
	for _, drv := range nonCamera {
		if !IsNonCameraDriver(drv) {
			t.Errorf("IsNonCameraDriver(%q) = false, want true", drv)
		}
	}
	camera := []string{"bcm2835-unicam", "unicam", "imx477", "ov5647", "uvcvideo", "tegra-capture-vi", ""}
	for _, drv := range camera {
		if IsNonCameraDriver(drv) {
			t.Errorf("IsNonCameraDriver(%q) = true, want false", drv)
		}
	}
}

func TestClassify_CSI_OfNodeImpliesCSI(t *testing.T) {
	// Unknown driver but a device-tree node present → still CSI.
	withFakeSysfs(t, map[string]string{"video0": "mystery-driver"}, map[string]bool{"video0": true})
	got, drv := Classify("video0")
	if got != TransportCSI || drv != "mystery-driver" {
		t.Fatalf("got (%v, %q), want (CSI, mystery-driver)", got, drv)
	}
}

func TestClassify_Unknown_NoDriverNoOfNode(t *testing.T) {
	withFakeSysfs(t, nil, nil)
	got, drv := Classify("video0")
	if got != TransportUnknown || drv != "" {
		t.Fatalf("got (%v, %q), want (Unknown, empty)", got, drv)
	}
}

func TestClassify_Unknown_UnknownDriver(t *testing.T) {
	withFakeSysfs(t, map[string]string{"video0": "mystery-driver"}, nil)
	got, drv := Classify("video0")
	if got != TransportUnknown || drv != "mystery-driver" {
		t.Fatalf("got (%v, %q), want (Unknown, mystery-driver)", got, drv)
	}
}

func TestEnumerateLibcamera_NoBinary_ReturnsNilNil(t *testing.T) {
	orig := lookupCam
	t.Cleanup(func() { lookupCam = orig })
	lookupCam = func() (string, error) { return "", errors.New("not found") }

	got, err := EnumerateLibcamera(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil map, got %v", got)
	}
}

func TestEnumerateLibcamera_ParsesCamList(t *testing.T) {
	origLookup := lookupCam
	origRun := runCamList
	t.Cleanup(func() {
		lookupCam = origLookup
		runCamList = origRun
	})
	lookupCam = func() (string, error) { return "/usr/bin/cam", nil }
	runCamList = func(ctx context.Context, bin string) ([]byte, error) {
		return []byte(`Available cameras:
0: 'Sensor1' (/base/soc/i2c0mux/i2c@1/imx477@1a)
1: 'Sensor2' (/base/soc/i2c0mux/i2c@2/ov5647@36)
`), nil
	}

	got, err := EnumerateLibcamera(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	if got["/base/soc/i2c0mux/i2c@1/imx477@1a"] != "Sensor1" {
		t.Errorf("entry 1 wrong: %q", got["/base/soc/i2c0mux/i2c@1/imx477@1a"])
	}
	if got["/base/soc/i2c0mux/i2c@2/ov5647@36"] != "Sensor2" {
		t.Errorf("entry 2 wrong: %q", got["/base/soc/i2c0mux/i2c@2/ov5647@36"])
	}
}

func TestEnumerateLibcamera_Empty(t *testing.T) {
	origLookup := lookupCam
	origRun := runCamList
	t.Cleanup(func() {
		lookupCam = origLookup
		runCamList = origRun
	})
	lookupCam = func() (string, error) { return "/usr/bin/cam", nil }
	runCamList = func(ctx context.Context, bin string) ([]byte, error) { return []byte("Available cameras:\n"), nil }

	got, err := EnumerateLibcamera(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestEnumerateLibcamera_Timeout(t *testing.T) {
	origLookup := lookupCam
	origRun := runCamList
	origTimeout := libcameraTimeout
	t.Cleanup(func() {
		lookupCam = origLookup
		runCamList = origRun
		libcameraTimeout = origTimeout
	})
	lookupCam = func() (string, error) { return "/usr/bin/cam", nil }
	libcameraTimeout = 10 * time.Millisecond
	runCamList = func(ctx context.Context, bin string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, err := EnumerateLibcamera(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestParseCamList_HandlesNoIndex(t *testing.T) {
	out := `'OnlyOne' (/path/to/cam)`
	got := parseCamList(out)
	if got["/path/to/cam"] != "OnlyOne" {
		t.Fatalf("got %v", got)
	}
}

func TestParseCamList_IgnoresMalformed(t *testing.T) {
	out := "this line has no parens\n0: 'X' (id)\n"
	got := parseCamList(out)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(got), got)
	}
}

func TestIsValidLibcameraID(t *testing.T) {
	valid := []string{
		"/base/soc/i2c0mux/i2c@1/imx477@1a",
		"/base/cam@1a",
		"imx219",
		"a.b_c-d:e/f@g",
	}
	for _, id := range valid {
		if !IsValidLibcameraID(id) {
			t.Errorf("IsValidLibcameraID(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",                            // empty
		"/base/cam id with spaces",    // whitespace splits under strings.Fields
		"/cam ! filesink location=/x", // injects extra pipeline elements
		"cam\tname",                   // tab is whitespace
		"cam=value",                   // '=' is a gst property separator
		"/cam'quoted",                 // quote not in allowlist
	}
	for _, id := range invalid {
		if IsValidLibcameraID(id) {
			t.Errorf("IsValidLibcameraID(%q) = true, want false", id)
		}
	}
}

// A camera-name that would inject extra GStreamer pipeline elements (spaces,
// '!', '=') once the pipeline is split with strings.Fields must be dropped, not
// surfaced to callers.
func TestParseCamList_DropsInjectableID(t *testing.T) {
	out := "0: 'Evil' (/cam ! filesink location=/etc/passwd)\n1: 'Good' (/base/cam@1a)\n"
	got := parseCamList(out)
	if len(got) != 1 {
		t.Fatalf("expected only the safe id to survive, got %d: %v", len(got), got)
	}
	if _, ok := got["/base/cam@1a"]; !ok {
		t.Errorf("expected the safe id to be kept, got %v", got)
	}
}

// Sanity: confirm Classify works for a non-existent /sys path (real-world
// host-test fallback before fakes are installed in TestMain).
func TestClassify_DefaultsAreSafe(t *testing.T) {
	// Don't install fakes — defaults should treat missing files as unknown.
	got, drv := Classify(fmt.Sprintf("video-does-not-exist-%d", time.Now().UnixNano()))
	if got != TransportUnknown || drv != "" {
		t.Fatalf("got (%v, %q), want (Unknown, empty)", got, drv)
	}
}

func TestReadCamListBounded_CapsOversizedOutput(t *testing.T) {
	// A pathological or compromised `cam` could emit unbounded output; the reader
	// must cap what it buffers into memory rather than growing without limit.
	big := strings.Repeat("imx477\n", (maxCamListBytes/7)+1000) // > maxCamListBytes
	out, err := readCamListBounded(strings.NewReader(big))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) > maxCamListBytes {
		t.Errorf("output must be capped at %d bytes, got %d", maxCamListBytes, len(out))
	}
}

func TestReadCamListBounded_SmallOutputPassesThrough(t *testing.T) {
	const sample = "1: 'imx477' (/base/axi/pcie@120000/rp1/i2c@80000/imx477@1a)\n"
	out, err := readCamListBounded(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != sample {
		t.Errorf("small output must pass through unchanged, got %q", out)
	}
}
