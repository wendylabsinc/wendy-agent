package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Control names now come from the driver, so the spelling this API accepts is
// produced by normalising the driver's label. It must match what v4l2-ctl
// prints, because that is what anyone reading a camera's docs will type.
func TestQueryCtrlName_MatchesV4L2CtlSpelling(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"Exposure Time, Absolute", "exposure_time_absolute"},
		{"White Balance, Automatic", "white_balance_automatic"},
		{"Zoom, Absolute", "zoom_absolute"},
		{"Focus, Automatic Continuous", "focus_automatic_continuous"},
		{"Backlight Compensation", "backlight_compensation"},
		{"Gain", "gain"},
		// trailing NUL padding, leading/trailing separators, and runs of
		// punctuation must all collapse rather than leak into the name
		{"Brightness   ", "brightness"},
		{",,Hue,,", "hue"},
	}
	for _, tc := range cases {
		var q v4l2QueryCtrl
		copy(q[8:40], tc.raw)
		if got := q.name(); got != tc.want {
			t.Errorf("name(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// A name longer than the 32-byte field, or one with no terminator, must not
// read past the struct.
func TestQueryCtrlName_UnterminatedFieldStaysInBounds(t *testing.T) {
	var q v4l2QueryCtrl
	for i := 8; i < 40; i++ {
		q[i] = 'a'
	}
	if got := q.name(); len(got) != 32 {
		t.Fatalf("want the full 32 bytes, got %d (%q)", len(got), got)
	}
}

// orderControls must put the mode control before the control it gates, or the
// driver rejects the gated one (exposure_time_absolute is inactive until
// auto_exposure=Manual).
func TestOrderControls_ModeBeforeGatedControl(t *testing.T) {
	in := []controlValue{
		{name: "exposure_time_absolute", value: 20},
		{name: "backlight_compensation", value: 0},
		{name: "auto_exposure", value: 1},
	}
	out := orderControls(in)
	if out[0].name != "auto_exposure" {
		t.Fatalf("auto_exposure not first: %v", names(out))
	}
	// the two non-mode controls keep their relative order (stable sort)
	if out[1].name != "exposure_time_absolute" || out[2].name != "backlight_compensation" {
		t.Fatalf("stable order not preserved: %v", names(out))
	}
}

func names(cs []controlValue) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

func TestCameraControlStore_MergeAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera-controls.json")
	store := newCameraControlStore(path)
	if err := store.Load(); err != nil { // absent file is fine
		t.Fatalf("Load of absent file: %v", err)
	}
	if err := store.merge("/dev/video0", []storedControl{{Name: "auto_exposure", Value: 1}}); err != nil {
		t.Fatalf("merge 1: %v", err)
	}
	// A second merge updates auto_exposure and adds exposure_time_absolute.
	if err := store.merge("/dev/video0", []storedControl{
		{Name: "auto_exposure", Value: 1},
		{Name: "exposure_time_absolute", Value: 20},
	}); err != nil {
		t.Fatalf("merge 2: %v", err)
	}

	reloaded := newCameraControlStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.get("/dev/video0")
	if len(got) != 2 {
		t.Fatalf("want 2 stored controls, got %d: %+v", len(got), got)
	}
	byName := map[string]int32{}
	for _, c := range got {
		byName[c.Name] = c.Value
	}
	if byName["auto_exposure"] != 1 || byName["exposure_time_absolute"] != 20 {
		t.Fatalf("reloaded values wrong: %+v", byName)
	}
}

// newControlsTestService builds a VideoService with the V4L2 seams stubbed so
// the RPC logic is exercised without a real /dev/video node, and with an
// isolated on-disk store.
func newControlsTestService(t *testing.T) (*VideoService, *[]controlValue) {
	t.Helper()
	svc := NewVideoService(context.Background(), zap.NewNop())
	svc.controls = newCameraControlStore(filepath.Join(t.TempDir(), "cc.json"))
	captured := &[]controlValue{}
	svc.controlIndexFor = func(string) (map[string]uint32, error) {
		// stands in for asking the camera; the CIDs only need to be distinct
		return map[string]uint32{
			"auto_exposure":          0x009a0901,
			"exposure_time_absolute": 0x009a0902,
			"backlight_compensation": 0x0098091c,
			"brightness":             0x00980900,
			"gain":                   0x00980913,
			"zoom_absolute":          0x009a090d,
		}, nil
	}
	svc.applyLocalControls = func(_ string, ctrls []controlValue) ([]*agentpb.CameraControlResult, error) {
		*captured = ctrls
		res := make([]*agentpb.CameraControlResult, len(ctrls))
		for i, c := range ctrls {
			res[i] = &agentpb.CameraControlResult{Name: c.name, Applied: true}
		}
		return res, nil
	}
	svc.queryLocalControls = func(_ string) ([]*agentpb.CameraControl, error) {
		return []*agentpb.CameraControl{{Name: "auto_exposure", Value: 3, Minimum: 0, Maximum: 3, Settable: true}}, nil
	}
	return svc, captured
}

func TestSetCameraControls_RejectsNetworkCamera(t *testing.T) {
	svc, _ := newControlsTestService(t)
	_, err := svc.SetCameraControls(context.Background(), &agentpb.SetCameraControlsRequest{
		DeviceId: ipcam.IDBandStart,
		Controls: []*agentpb.CameraControl{{Name: "auto_exposure", Value: 1}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for a network camera, got %v", err)
	}
}

func TestSetCameraControls_UnknownControlReportedAndKnownApplied(t *testing.T) {
	svc, captured := newControlsTestService(t)
	resp, err := svc.SetCameraControls(context.Background(), &agentpb.SetCameraControlsRequest{
		DeviceId: 0,
		Controls: []*agentpb.CameraControl{
			{Name: "auto_exposure", Value: 1},
			{Name: "totally_bogus", Value: 5},
		},
		Persist: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]*agentpb.CameraControlResult{}
	for _, r := range resp.GetResults() {
		got[r.GetName()] = r
	}
	// Names are resolved against the camera now, so the reason is not "nobody
	// has heard of this control" but "this camera does not have it" -- the same
	// answer a real camera gives for a control it lacks.
	if r := got["totally_bogus"]; r == nil || r.GetApplied() ||
		r.GetDetail() != "this camera has no control by that name" {
		t.Fatalf("unknown control not reported correctly: %+v", got["totally_bogus"])
	}
	if r := got["auto_exposure"]; r == nil || !r.GetApplied() {
		t.Fatalf("known control not applied: %+v", got["auto_exposure"])
	}
	// The unknown control must not reach the V4L2 seam.
	if len(*captured) != 1 || (*captured)[0].name != "auto_exposure" || (*captured)[0].cid != 0x009a0901 {
		t.Fatalf("seam received wrong controls: %v", *captured)
	}
	// persist=true stored the applied control.
	if stored := svc.controls.get("/dev/video0"); len(stored) != 1 || stored[0].Name != "auto_exposure" || stored[0].Value != 1 {
		t.Fatalf("applied control not persisted: %+v", stored)
	}
}

func TestSetCameraControls_NoControls(t *testing.T) {
	svc, _ := newControlsTestService(t)
	_, err := svc.SetCameraControls(context.Background(), &agentpb.SetCameraControlsRequest{DeviceId: 0})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for empty controls, got %v", err)
	}
}

func TestSetCameraControls_NoPersistDoesNotStore(t *testing.T) {
	svc, _ := newControlsTestService(t)
	if _, err := svc.SetCameraControls(context.Background(), &agentpb.SetCameraControlsRequest{
		DeviceId: 0,
		Controls: []*agentpb.CameraControl{{Name: "gain", Value: 10}},
		Persist:  false,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored := svc.controls.get("/dev/video0"); len(stored) != 0 {
		t.Fatalf("no-persist must not store, got %+v", stored)
	}
}

func TestGetCameraControls_RejectsNetworkCamera(t *testing.T) {
	svc, _ := newControlsTestService(t)
	_, err := svc.GetCameraControls(context.Background(), &agentpb.GetCameraControlsRequest{DeviceId: ipcam.IDBandEnd})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestGetCameraControls_ReturnsSeamOutput(t *testing.T) {
	svc, _ := newControlsTestService(t)
	resp, err := svc.GetCameraControls(context.Background(), &agentpb.GetCameraControlsRequest{DeviceId: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetControls()) != 1 || resp.GetControls()[0].GetName() != "auto_exposure" {
		t.Fatalf("unexpected controls: %+v", resp.GetControls())
	}
}

func TestApplyStoredCameraControls_ReAppliesResolved(t *testing.T) {
	svc, captured := newControlsTestService(t)
	if err := svc.controls.merge("/dev/video0", []storedControl{
		{Name: "auto_exposure", Value: 1},
		{Name: "exposure_time_absolute", Value: 20},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	svc.applyStoredCameraControls("/dev/video0")
	if len(*captured) != 2 {
		t.Fatalf("want 2 controls re-applied, got %v", *captured)
	}
	// Resolved against the camera, not a table: every control carries a CID the
	// enumeration supplied, and two different names never share one.
	seen := map[uint32]string{}
	for _, c := range *captured {
		if c.cid == 0 {
			t.Fatalf("control %q re-applied with no cid", c.name)
		}
		if prev, dup := seen[c.cid]; dup {
			t.Fatalf("controls %q and %q share cid %#x", prev, c.name, c.cid)
		}
		seen[c.cid] = c.name
	}
}

// The store is device configuration that decides how a camera captures, so it
// is readable and writable only by the agent -- same footing as ipcam's
// credential store. It was 0o644/0o755, which let any local user read it and,
// depending on the directory, replace it.
func TestCameraControlStore_IsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "camera-controls.json")
	store := newCameraControlStore(path)
	if err := store.merge("/dev/video0", []storedControl{{Name: "gain", Value: 4}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("store file mode = %#o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %#o, want 0700", perm)
	}
}
