package services

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestControlCID(t *testing.T) {
	if cid, ok := controlCID("exposure_time_absolute"); !ok || cid != 0x009a0902 {
		t.Fatalf("exposure_time_absolute: got (%#x, %v), want (0x009a0902, true)", cid, ok)
	}
	if _, ok := controlCID("not_a_control"); ok {
		t.Fatalf("unknown control resolved to a CID")
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
	if r := got["totally_bogus"]; r == nil || r.GetApplied() || r.GetDetail() != "unknown control" {
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
	// resolved with real CIDs
	for _, c := range *captured {
		if cid, ok := controlCID(c.name); !ok || cid != c.cid {
			t.Fatalf("control %q resolved to wrong cid %#x", c.name, c.cid)
		}
	}
}
