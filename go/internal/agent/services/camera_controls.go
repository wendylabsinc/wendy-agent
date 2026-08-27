package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unsafe"

	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file adds tunable V4L2 controls (exposure, gain, white balance, ...) to
// the video service. It exists because the agent owns the /dev/videoN node --
// StreamVideo multiplexes one capture to every subscriber (see video_service.go)
// -- so the agent is the only process that can set these to stick. A control set
// by anyone else is lost the moment the capture pipeline reopens the device, and
// on a device the app reads through the agent (CAMERA_SOURCE=wendy-agent://...)
// there is no other writer at all. The concrete need: a scene the camera's
// auto-exposure gets wrong, e.g. a flame that blows out to white, where forcing
// manual exposure keeps the highlight from clipping so a fire model can see it.

const (
	// VIDIOC_G_EXT_CTRLS, _IOWR('V', 71, struct v4l2_ext_controls) -- reads the
	// current value; the S_ variant (vidiocSExtCtrls, video_service.go) writes it.
	vidiocGExtCtrls = 0xC0205647
	// VIDIOC_QUERYCTRL, _IOWR('V', 36, struct v4l2_queryctrl) -- range + flags.
	vidiocQueryCtrl = 0xC0445624
	// V4L2_CTRL_WHICH_CUR_VAL: address a control by CID regardless of its class,
	// which the classic per-class `which` cannot do for a mix of USER and CAMERA
	// controls. This is what v4l2-ctl uses.
	v4l2CtrlWhichCurVal = 0x0

	// v4l2_queryctrl flags we act on.
	v4l2CtrlFlagDisabled = 0x0001
	v4l2CtrlFlagReadOnly = 0x0004
	v4l2CtrlFlagInactive = 0x0010

	cameraControlsPath = "/var/lib/wendy/camera-controls.json"
)

// tunableControls is the fixed set of controls the agent will read and set, by
// stable slug. The slugs match v4l2-ctl's control names so an operator can move
// between the CLI and this API without a translation table. CIDs are the
// standard V4L2_CID_* values from linux/videodev2.h: the USER class base is
// 0x00980900, the CAMERA class base 0x009a0900.
var tunableControls = []struct {
	name string
	cid  uint32
}{
	{"brightness", 0x00980900},
	{"contrast", 0x00980901},
	{"saturation", 0x00980902},
	{"hue", 0x00980903},
	{"white_balance_automatic", 0x0098090c}, // V4L2_CID_AUTO_WHITE_BALANCE
	{"gamma", 0x00980910},
	{"gain", 0x00980913},
	{"power_line_frequency", 0x00980918},
	{"white_balance_temperature", 0x0098091a},
	{"sharpness", 0x0098091b},
	{"backlight_compensation", 0x0098091c},
	{"auto_exposure", 0x009a0901},          // V4L2_CID_EXPOSURE_AUTO
	{"exposure_time_absolute", 0x009a0902}, // V4L2_CID_EXPOSURE_ABSOLUTE
	{"exposure_dynamic_framerate", 0x009a0903},
}

func controlCID(name string) (uint32, bool) {
	for _, c := range tunableControls {
		if c.name == name {
			return c.cid, true
		}
	}
	return 0, false
}

// controlValue is a resolved control ready to write.
type controlValue struct {
	name  string
	cid   uint32
	value int32
}

// orderControls sorts so that a mode control is applied before the control it
// gates: auto_exposure=Manual must precede exposure_time_absolute (the driver
// marks the absolute control inactive until then), and likewise auto white
// balance before the temperature. Stable, so unrelated controls keep their order.
func orderControls(in []controlValue) []controlValue {
	out := append([]controlValue(nil), in...)
	rank := func(name string) int {
		switch name {
		case "auto_exposure", "white_balance_automatic":
			return 0
		default:
			return 1
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].name) < rank(out[j].name) })
	return out
}

// --- V4L2 ioctls -----------------------------------------------------------

// getV4L2ExtControl issues VIDIOC_G_EXT_CTRLS for a single control and returns
// its current value. Mirrors setV4L2ExtControl (video_service.go); the inner
// control is pinned because it is reached only through a uintptr the GC cannot see.
func getV4L2ExtControl(fd int, controlID uint32) (int32, unix.Errno) {
	var ctrl v4l2ExtControl
	ctrl.setID(controlID)

	var pinner runtime.Pinner
	pinner.Pin(&ctrl)
	defer pinner.Unpin()

	var ctrls v4l2ExtControls
	ctrls.setWhich(v4l2CtrlWhichCurVal)
	ctrls.setCount(1)
	ctrls.setControlsPtr(uintptr(unsafe.Pointer(&ctrl)))

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocGExtCtrls, uintptr(unsafe.Pointer(&ctrls)))
	return ctrl.value(), errno
}

// v4l2QueryCtrl matches struct v4l2_queryctrl (68 bytes): id@0, type@4,
// name[32]@8, minimum@40, maximum@44, step@48, default@52, flags@56, reserved@60.
type v4l2QueryCtrl [68]byte

func (q *v4l2QueryCtrl) setID(id uint32)   { *(*uint32)(unsafe.Pointer(&q[0])) = id }
func (q *v4l2QueryCtrl) minimum() int32    { return *(*int32)(unsafe.Pointer(&q[40])) }
func (q *v4l2QueryCtrl) maximum() int32    { return *(*int32)(unsafe.Pointer(&q[44])) }
func (q *v4l2QueryCtrl) step() int32       { return *(*int32)(unsafe.Pointer(&q[48])) }
func (q *v4l2QueryCtrl) defaultVal() int32 { return *(*int32)(unsafe.Pointer(&q[52])) }
func (q *v4l2QueryCtrl) flags() uint32     { return *(*uint32)(unsafe.Pointer(&q[56])) }

// queryV4L2Control issues VIDIOC_QUERYCTRL. ok is false when the driver does not
// expose the control (EINVAL) -- a normal answer, not an error, since the tunable
// set is a superset of any one camera's controls.
func queryV4L2Control(fd int, controlID uint32) (q v4l2QueryCtrl, ok bool) {
	q.setID(controlID)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQueryCtrl, uintptr(unsafe.Pointer(&q)))
	return q, errno == 0
}

// applyCameraControlsV4L2 opens the node read-write and sets each control,
// reporting per-control success. Controls are reordered (orderControls) so a
// mode change lands before the control it enables. A control the camera does
// not have comes back applied=false with the errno, not as a hard failure: one
// unknown knob must not sink the rest of the request.
func applyCameraControlsV4L2(path string, ctrls []controlValue) ([]*agentpb.CameraControlResult, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd) //nolint:errcheck

	results := make([]*agentpb.CameraControlResult, 0, len(ctrls))
	for _, c := range orderControls(ctrls) {
		errno := setV4L2ExtControl(fd, c.cid, c.value, v4l2CtrlWhichCurVal)
		r := &agentpb.CameraControlResult{Name: c.name, Applied: errno == 0}
		if errno != 0 {
			r.Detail = errno.Error()
		}
		results = append(results, r)
	}
	return results, nil
}

// queryCameraControlsV4L2 reports every tunable control the camera actually
// exposes, with its current value and range. O_RDONLY: QUERYCTRL and
// G_EXT_CTRLS are read-only, and a read-only open avoids the EBUSY an exclusive
// camera can return on a second writable open (same reasoning as hasVideoCapture).
func queryCameraControlsV4L2(path string) ([]*agentpb.CameraControl, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd) //nolint:errcheck

	out := make([]*agentpb.CameraControl, 0, len(tunableControls))
	for _, tc := range tunableControls {
		q, ok := queryV4L2Control(fd, tc.cid)
		if !ok {
			continue // this camera does not have it
		}
		val, errno := getV4L2ExtControl(fd, tc.cid)
		if errno != 0 {
			val = q.defaultVal() // report something rather than dropping the row
		}
		flags := q.flags()
		out = append(out, &agentpb.CameraControl{
			Name:         tc.name,
			Value:        val,
			Minimum:      q.minimum(),
			Maximum:      q.maximum(),
			Step:         q.step(),
			DefaultValue: q.defaultVal(),
			Settable:     flags&(v4l2CtrlFlagDisabled|v4l2CtrlFlagReadOnly) == 0 && flags&v4l2CtrlFlagInactive == 0,
		})
	}
	return out, nil
}

// --- persistence -----------------------------------------------------------

type storedControl struct {
	Name  string `json:"name"`
	Value int32  `json:"value"`
}

// cameraControlStore persists desired controls per device path so they can be
// re-asserted whenever the capture pipeline reopens the node and across agent
// restarts -- the difference between a fix that holds and one that reverts to
// the firmware default on the next stream reconnect or reboot.
type cameraControlStore struct {
	path   string
	mu     sync.Mutex
	byPath map[string][]storedControl
}

func newCameraControlStore(path string) *cameraControlStore {
	return &cameraControlStore{path: path, byPath: map[string][]storedControl{}}
}

func (c *cameraControlStore) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing persisted yet is not an error
		}
		return err
	}
	var m map[string][]storedControl
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m != nil {
		c.byPath = m
	}
	return nil
}

// merge updates or inserts each control for path and persists. Merge, not
// replace: setting exposure after having set auto-mode must keep both.
func (c *cameraControlStore) merge(path string, ctrls []storedControl) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.byPath[path]
	for _, nc := range ctrls {
		found := false
		for i := range cur {
			if cur[i].Name == nc.Name {
				cur[i].Value = nc.Value
				found = true
				break
			}
		}
		if !found {
			cur = append(cur, nc)
		}
	}
	c.byPath[path] = cur
	return c.save()
}

// save writes the store atomically. Caller holds c.mu.
func (c *cameraControlStore) save() error {
	b, err := json.MarshalIndent(c.byPath, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *cameraControlStore) get(path string) []storedControl {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]storedControl(nil), c.byPath[path]...)
}

// --- RPCs ------------------------------------------------------------------

// localCameraPath validates that devID names a local V4L2 camera and returns
// its node path. Network cameras are rejected: they expose no V4L2 controls here.
func localCameraPath(devID uint32) (string, error) {
	if devID >= ipcam.IDBandStart && devID <= ipcam.IDBandEnd {
		return "", status.Errorf(codes.InvalidArgument,
			"camera %d is a network camera; V4L2 controls apply only to local (USB/CSI) cameras", devID)
	}
	return fmt.Sprintf("/dev/video%d", devID), nil
}

// GetCameraControls reports the tunable controls a local camera exposes.
func (s *VideoService) GetCameraControls(_ context.Context, req *agentpb.GetCameraControlsRequest) (*agentpb.GetCameraControlsResponse, error) {
	path, err := localCameraPath(req.GetDeviceId())
	if err != nil {
		return nil, err
	}
	controls, err := s.queryLocalControls(path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reading controls from %s: %v", path, err)
	}
	return &agentpb.GetCameraControlsResponse{Controls: controls}, nil
}

// SetCameraControls sets controls on a local camera and, when persist is set,
// remembers them so they survive a pipeline reopen and an agent restart.
func (s *VideoService) SetCameraControls(_ context.Context, req *agentpb.SetCameraControlsRequest) (*agentpb.SetCameraControlsResponse, error) {
	path, err := localCameraPath(req.GetDeviceId())
	if err != nil {
		return nil, err
	}
	if len(req.GetControls()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no controls given")
	}

	var resolved []controlValue
	results := make([]*agentpb.CameraControlResult, 0, len(req.GetControls()))
	unknown := map[string]bool{}
	for _, c := range req.GetControls() {
		cid, ok := controlCID(c.GetName())
		if !ok {
			unknown[c.GetName()] = true
			results = append(results, &agentpb.CameraControlResult{
				Name: c.GetName(), Applied: false, Detail: "unknown control",
			})
			continue
		}
		resolved = append(resolved, controlValue{name: c.GetName(), cid: cid, value: c.GetValue()})
	}

	if len(resolved) > 0 {
		applied, err := s.applyLocalControls(path, resolved)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "opening %s: %v", path, err)
		}
		results = append(results, applied...)

		if req.GetPersist() && s.controls != nil {
			var toStore []storedControl
			for _, r := range applied {
				if r.GetApplied() {
					for _, rv := range resolved {
						if rv.name == r.GetName() {
							toStore = append(toStore, storedControl{Name: rv.name, Value: rv.value})
						}
					}
				}
			}
			if len(toStore) > 0 {
				if err := s.controls.merge(path, toStore); err != nil {
					s.logger.Warn("persisting camera controls failed",
						zap.String("device", path), zap.Error(err))
				}
			}
		}
	}
	_ = unknown
	return &agentpb.SetCameraControlsResponse{Results: results}, nil
}

// applyStoredCameraControls re-asserts any persisted controls for a local
// camera. Called from runProducer each time a producer (re)starts, so a stream
// reconnect does not silently revert exposure to the firmware default. Best
// effort: a camera that has lost a control since it was stored just logs.
func (s *VideoService) applyStoredCameraControls(path string) {
	if s.controls == nil {
		return
	}
	stored := s.controls.get(path)
	if len(stored) == 0 {
		return
	}
	resolved := make([]controlValue, 0, len(stored))
	for _, sc := range stored {
		if cid, ok := controlCID(sc.Name); ok {
			resolved = append(resolved, controlValue{name: sc.Name, cid: cid, value: sc.Value})
		}
	}
	if len(resolved) == 0 {
		return
	}
	if _, err := s.applyLocalControls(path, resolved); err != nil {
		s.logger.Debug("re-applying stored camera controls failed",
			zap.String("device", path), zap.Error(err))
		return
	}
	s.logger.Info("re-applied stored camera controls",
		zap.String("device", path), zap.Int("count", len(resolved)))
}
