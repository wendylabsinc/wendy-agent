package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// The incident these tests exist for: a reboot re-ordered USB enumeration on a
// two-camera device, so every config that had pinned /dev/videoN was addressing
// the other camera. It went unnoticed for hours because opening the wrong
// camera SUCCEEDS -- the tile showed a real picture, from the wrong sensor, and
// nothing anywhere reported an error.
//
//	before   Arducam video0,1,2,3   TC001 video4,5
//	after    Arducam video2,3,4,5   TC001 video0,1
//
// The names are the real ones from that device.
const (
	arducamByID = "usb-Arducam_Technology_Co.__Ltd._USB_2.0_Camera_SN0001-video-index0"
	topdonByID  = "usb-Camera_USB_Camera_EA6913883-video-index0"
)

// fakeV4L builds a /dev/v4l-shaped tree: real files standing in for device
// nodes, and a by-id directory of symlinks pointing at them.
func fakeV4L(t *testing.T, links map[string]string) (byIDDir string, devDir string) {
	t.Helper()
	root := t.TempDir()
	devDir = filepath.Join(root, "dev")
	byIDDir = filepath.Join(root, "by-id")
	for _, d := range []string{devDir, byIDDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, node := range links {
		nodePath := filepath.Join(devDir, node)
		if err := os.WriteFile(nodePath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(nodePath, filepath.Join(byIDDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return byIDDir, devDir
}

// resolved matches what readStableNames keys on: EvalSymlinks returns the real
// path, and on macOS /var and /tmp are themselves symlinks.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestByIDSurvivesTheEnumerationSwap(t *testing.T) {
	// The same two cameras, before and after the reboot that swapped them.
	cases := []struct {
		label           string
		links           map[string]string
		arducam, topdon string
	}{
		{
			label:   "before reboot",
			links:   map[string]string{arducamByID: "video0", topdonByID: "video4"},
			arducam: "video0", topdon: "video4",
		},
		{
			label:   "after reboot",
			links:   map[string]string{arducamByID: "video2", topdonByID: "video0"},
			arducam: "video2", topdon: "video0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			byIDDir, devDir := fakeV4L(t, tc.links)
			names := readStableNames(byIDDir, filepath.Join(byIDDir, "absent"))

			for _, want := range []struct {
				byID, node string
			}{{arducamByID, tc.arducam}, {topdonByID, tc.topdon}} {
				got, ok := names[resolved(t, filepath.Join(devDir, want.node))]
				if !ok {
					t.Fatalf("%s: no entry for %s", tc.label, want.node)
				}
				if got.byID != want.byID {
					t.Errorf("%s: %s -> by-id %q, want %q",
						tc.label, want.node, got.byID, want.byID)
				}
			}
		})
	}
}

func TestByPathIsReportedSeparately(t *testing.T) {
	// by-path is the only thing that separates two same-model cameras whose
	// serial is a factory default, so it must not be conflated with by-id.
	root := t.TempDir()
	devDir, byIDDir, byPathDir := filepath.Join(root, "dev"),
		filepath.Join(root, "by-id"), filepath.Join(root, "by-path")
	for _, d := range []string{devDir, byIDDir, byPathDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	node := filepath.Join(devDir, "video2")
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(byIDDir, arducamByID)); err != nil {
		t.Fatal(err)
	}
	const portName = "platform-xhci-hcd.0-usb-0:2.3:1.0-video-index0"
	if err := os.Symlink(node, filepath.Join(byPathDir, portName)); err != nil {
		t.Fatal(err)
	}

	got := readStableNames(byIDDir, byPathDir)[resolved(t, node)]
	if got.byID != arducamByID {
		t.Errorf("byID = %q, want %q", got.byID, arducamByID)
	}
	if got.byPath != portName {
		t.Errorf("byPath = %q, want %q", got.byPath, portName)
	}
}

func TestMissingDirectoriesAreNotAnError(t *testing.T) {
	// A device with no /dev/v4l at all must still enumerate. Losing the
	// identity is survivable; failing the listing is not.
	if got := readStableNames("/nonexistent/by-id", "/nonexistent/by-path"); len(got) != 0 {
		t.Errorf("want empty map, got %v", got)
	}
}

func TestDanglingSymlinkDoesNotCostTheOthers(t *testing.T) {
	byIDDir, devDir := fakeV4L(t, map[string]string{topdonByID: "video0"})
	if err := os.Symlink(filepath.Join(devDir, "video9"), // never created
		filepath.Join(byIDDir, "usb-Broken-video-index0")); err != nil {
		t.Fatal(err)
	}
	names := readStableNames(byIDDir, "")
	if got := names[resolved(t, filepath.Join(devDir, "video0"))].byID; got != topdonByID {
		t.Errorf("a dangling symlink cost a healthy camera its identity: got %q", got)
	}
}

func TestResolveByIDIsExactNotPrefix(t *testing.T) {
	// A prefix rule would make "…_SN1-video-index0" match "…_SN10-video-index0"
	// and hand back one of them silently -- the same class of wrong-camera bug
	// this whole change exists to remove.
	names := map[string]stableNames{
		"/dev/video0": {byID: "usb-Acme_Cam_SN1-video-index0"},
		"/dev/video2": {byID: "usb-Acme_Cam_SN10-video-index0"},
	}
	got, ok := resolveByID(names, "usb-Acme_Cam_SN1-video-index0")
	if !ok || got != 0 {
		t.Fatalf("exact name resolved to (%d, %v), want (0, true)", got, ok)
	}
	if _, ok := resolveByID(names, "usb-Acme_Cam_SN"); ok {
		t.Error("a prefix matched; resolution must be exact")
	}
	if _, ok := resolveByID(names, ""); ok {
		t.Error("an empty name matched")
	}
}

func TestResolveByIDReportsNoMatch(t *testing.T) {
	names := map[string]stableNames{"/dev/video0": {byID: topdonByID}}
	if _, ok := resolveByID(names, arducamByID); ok {
		t.Error("an absent camera resolved; the caller must be able to refuse")
	}
}

// ── service level ───────────────────────────────────────────────────────────

func TestListCamerasReportsStableIdentity(t *testing.T) {
	svc := newTestVideoService(
		func() ([]string, error) { return []string{"/dev/video0", "/dev/video2"}, nil },
		func(base string) (string, error) { return base, nil },
	)
	svc.readStableNames = func() map[string]stableNames {
		return map[string]stableNames{
			"/dev/video0": {byID: topdonByID, byPath: "platform-xhci-hcd.0-usb-0:2.1:1.0-video-index0"},
			"/dev/video2": {byID: arducamByID, byPath: "platform-xhci-hcd.0-usb-0:2.3:1.0-video-index0"},
		}
	}
	devices, err := svc.listCameras(context.Background())
	if err != nil {
		t.Fatalf("listCameras: %v", err)
	}
	got := map[uint32]string{}
	for _, d := range devices {
		got[d.GetId()] = d.GetById()
		if d.GetByPath() == "" {
			t.Errorf("device %d reported no by-path", d.GetId())
		}
	}
	if got[0] != topdonByID {
		t.Errorf("video0 by-id = %q, want %q", got[0], topdonByID)
	}
	if got[2] != arducamByID {
		t.Errorf("video2 by-id = %q, want %q", got[2], arducamByID)
	}
}

func TestListCamerasWithoutStableNamesIsUnchanged(t *testing.T) {
	// Every device with no /dev/v4l -- and every existing caller -- must see
	// exactly what it saw before.
	svc := newTestVideoService(
		func() ([]string, error) { return []string{"/dev/video0"}, nil },
		func(base string) (string, error) { return "USB Camera", nil },
	)
	devices, err := svc.listCameras(context.Background())
	if err != nil {
		t.Fatalf("listCameras: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].GetById() != "" || devices[0].GetByPath() != "" {
		t.Errorf("identity invented from nothing: by-id %q by-path %q",
			devices[0].GetById(), devices[0].GetByPath())
	}
	if devices[0].GetId() != 0 || devices[0].GetName() != "USB Camera" {
		t.Errorf("existing fields changed: %+v", devices[0])
	}
}

func TestStreamDeviceIDPrefersTheName(t *testing.T) {
	svc := newTestVideoService(nil, nil)
	// The post-reboot layout: the Arducam is on video2 now, and a config still
	// says deviceId 0. The name must win.
	svc.readStableNames = func() map[string]stableNames {
		return map[string]stableNames{
			"/dev/video0": {byID: topdonByID},
			"/dev/video2": {byID: arducamByID},
		}
	}
	got, err := svc.streamDeviceID(&agentpb.StreamVideoRequest{
		DeviceId:   0,
		DeviceById: arducamByID,
	})
	if err != nil {
		t.Fatalf("streamDeviceID: %v", err)
	}
	if got != 2 {
		t.Errorf("resolved to %d, want 2 -- the stale deviceId won", got)
	}
}

func TestStreamDeviceIDRefusesAnUnknownName(t *testing.T) {
	// The load-bearing case. Falling back to device_id here would stream
	// whatever the kernel put at that number, succeed, and tell nobody.
	svc := newTestVideoService(nil, nil)
	svc.readStableNames = func() map[string]stableNames {
		return map[string]stableNames{"/dev/video0": {byID: topdonByID}}
	}
	_, err := svc.streamDeviceID(&agentpb.StreamVideoRequest{
		DeviceId:   0,
		DeviceById: arducamByID,
	})
	if err == nil {
		t.Fatal("an absent camera resolved; it must refuse rather than fall back")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
	// The error must name what IS there, or the operator is left guessing on a
	// device they may not be able to log into.
	if !strings.Contains(err.Error(), topdonByID) {
		t.Errorf("error does not list the available cameras: %v", err)
	}
}

func TestStreamDeviceIDFallsBackToTheNumberWhenUnnamed(t *testing.T) {
	svc := newTestVideoService(nil, nil)
	got, err := svc.streamDeviceID(&agentpb.StreamVideoRequest{DeviceId: 4})
	if err != nil {
		t.Fatalf("streamDeviceID: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4 -- an unnamed request must behave as before", got)
	}
}
