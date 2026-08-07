package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/audio"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// A graph with a Bluetooth speaker (the default sink), a Bluetooth microphone
// (the default source), an HDMI sink and a USB microphone. The Bluetooth
// endpoints are the ones ALSA cannot represent, and they are why enumeration
// goes through PipeWire.
const graphFixture = `[
  {
    "id": 30,
    "type": "PipeWire:Interface:Metadata",
    "props": { "metadata.name": "default" },
    "metadata": [
      { "subject": 0, "key": "default.audio.sink", "value": { "name": "bluez_output.78_2B_64_76_F3_CE.1" } },
      { "subject": 0, "key": "default.audio.source", "value": { "name": "bluez_input.78:2B:64:76:F3:CE" } }
    ]
  },
  {
    "id": 62,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 60, "media.class": "Audio/Sink",
      "node.name": "alsa_output.platform-fef00700.hdmi.hdmi-stereo",
      "node.description": "Built-in Audio"
    } }
  },
  {
    "id": 43,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 242, "media.class": "Audio/Sink",
      "node.name": "bluez_output.78_2B_64_76_F3_CE.1",
      "node.description": "Bose Revolve II SoundLink"
    } }
  },
  {
    "id": 44,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 251, "media.class": "Audio/Source",
      "node.name": "bluez_input.78:2B:64:76:F3:CE",
      "node.description": "Bose Revolve II SoundLink"
    } }
  },
  {
    "id": 55,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 88, "media.class": "Audio/Source",
      "node.name": "alsa_input.usb-046d_C920",
      "node.description": "HD Pro Webcam C920"
    } }
  }
]`

// stubGraph points the audio package at a fixture and records every wpctl call.
// It returns the recorded calls, which the caller reads after the exercise.
// Volume reads run concurrently, so the recorder is guarded.
func stubGraph(t *testing.T, dump string, wpctl func(args ...string) ([]byte, error)) *[]string {
	t.Helper()
	origDump, origWpctl := audio.DumpRun, audio.WpctlRun
	t.Cleanup(func() { audio.DumpRun, audio.WpctlRun = origDump, origWpctl })

	var mu sync.Mutex
	var calls []string
	audio.DumpRun = func(context.Context) ([]byte, error) { return []byte(dump), nil }
	audio.WpctlRun = func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.Join(args, " "))
		mu.Unlock()
		if wpctl == nil {
			return nil, nil
		}
		return wpctl(args...)
	}
	return &calls
}

func testAudioService() *AudioService { return NewAudioService(zap.NewNop()) }

func TestListAudioDevices(t *testing.T) {
	stubGraph(t, graphFixture, nil)

	resp, err := testAudioService().ListAudioDevices(context.Background(), &agentpb.ListAudioDevicesRequest{})
	if err != nil {
		t.Fatalf("ListAudioDevices() error = %v", err)
	}

	// Sorted by node ID so repeated listings are stable even as the graph
	// reorders itself.
	var gotIDs []uint32
	for _, d := range resp.GetDevices() {
		gotIDs = append(gotIDs, d.GetId())
	}
	want := []uint32{43, 44, 55, 62}
	if fmt.Sprint(gotIDs) != fmt.Sprint(want) {
		t.Fatalf("device IDs = %v, want %v", gotIDs, want)
	}

	byID := map[uint32]*agentpb.AudioDevice{}
	for _, d := range resp.GetDevices() {
		byID[d.GetId()] = d
	}

	// The Bluetooth speaker: an output, the current default, and named by its
	// PipeWire node name rather than a card/device pair it does not have.
	speaker := byID[43]
	if speaker.GetType() != agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_OUTPUT {
		t.Errorf("Bluetooth sink type = %v, want OUTPUT", speaker.GetType())
	}
	if !speaker.GetIsDefault() {
		t.Error("Bluetooth sink should be the default output")
	}
	if speaker.GetName() != "bluez_output.78_2B_64_76_F3_CE.1" {
		t.Errorf("name = %q", speaker.GetName())
	}
	if speaker.GetDescription() != "Bose Revolve II SoundLink" {
		t.Errorf("description = %q", speaker.GetDescription())
	}

	if !byID[44].GetIsDefault() {
		t.Error("Bluetooth source should be the default input")
	}
	// Defaults are per-direction: the HDMI sink and the USB mic are not
	// defaults just because something else in their direction is.
	if byID[62].GetIsDefault() || byID[55].GetIsDefault() {
		t.Error("non-default devices marked as default")
	}
	if byID[55].GetType() != agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_INPUT {
		t.Errorf("USB mic type = %v, want INPUT", byID[55].GetType())
	}
}

func TestListAudioDevicesTypeFilter(t *testing.T) {
	stubGraph(t, graphFixture, nil)
	in := agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_INPUT

	resp, err := testAudioService().ListAudioDevices(context.Background(),
		&agentpb.ListAudioDevicesRequest{TypeFilter: &in})
	if err != nil {
		t.Fatalf("ListAudioDevices() error = %v", err)
	}
	if len(resp.GetDevices()) != 2 {
		t.Fatalf("got %d devices, want the 2 inputs", len(resp.GetDevices()))
	}
	for _, d := range resp.GetDevices() {
		if d.GetType() != in {
			t.Errorf("device %d has type %v, want INPUT", d.GetId(), d.GetType())
		}
	}
}

func TestListAudioDevicesNoSession(t *testing.T) {
	orig := audio.DumpRun
	t.Cleanup(func() { audio.DumpRun = orig })
	audio.DumpRun = func(context.Context) ([]byte, error) {
		return nil, fmt.Errorf("no PipeWire session found")
	}

	_, err := testAudioService().ListAudioDevices(context.Background(), &agentpb.ListAudioDevicesRequest{})
	// Reporting audio as unavailable is the point: an empty list would read as
	// "this device has no speakers".
	if status.Code(err) != codes.Internal {
		t.Fatalf("error = %v, want codes.Internal", err)
	}
}

func TestSetDefaultAudioDevice(t *testing.T) {
	calls := stubGraph(t, graphFixture, nil)

	resp, err := testAudioService().SetDefaultAudioDevice(context.Background(),
		&agentpb.SetDefaultAudioDeviceRequest{DeviceId: 62})
	if err != nil {
		t.Fatalf("SetDefaultAudioDevice() error = %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("success = false, error = %q", resp.GetErrorMessage())
	}
	// The node ID goes straight through: it is what wpctl takes, and one call
	// covers the device because wpctl infers the direction from the node.
	if want := "set-default 62"; fmt.Sprint(*calls) != "["+want+"]" {
		t.Errorf("wpctl calls = %v, want [%s]", *calls, want)
	}
}

func TestSetDefaultAudioDeviceRejectsUnknown(t *testing.T) {
	calls := stubGraph(t, graphFixture, nil)
	svc := testAudioService()

	// Zero is the "unspecified" sentinel elsewhere in the API and is never a
	// device, so it is rejected outright rather than reported as a failure.
	if _, err := svc.SetDefaultAudioDevice(context.Background(),
		&agentpb.SetDefaultAudioDeviceRequest{DeviceId: 0}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("device ID 0: error = %v, want codes.InvalidArgument", err)
	}

	resp, err := svc.SetDefaultAudioDevice(context.Background(),
		&agentpb.SetDefaultAudioDeviceRequest{DeviceId: 999})
	if err != nil {
		t.Fatalf("unknown device: error = %v, want a failure response", err)
	}
	if resp.GetSuccess() || !strings.Contains(resp.GetErrorMessage(), "999") {
		t.Errorf("unknown device: success = %v, message = %q", resp.GetSuccess(), resp.GetErrorMessage())
	}
	if len(*calls) != 0 {
		t.Errorf("wpctl was called for a device that does not exist: %v", *calls)
	}
}

func TestSetDefaultAudioDeviceReportsWpctlFailure(t *testing.T) {
	stubGraph(t, graphFixture, func(...string) ([]byte, error) {
		return []byte("Object not found"), fmt.Errorf("exit status 1")
	})

	resp, err := testAudioService().SetDefaultAudioDevice(context.Background(),
		&agentpb.SetDefaultAudioDeviceRequest{DeviceId: 43})
	if err != nil {
		t.Fatalf("SetDefaultAudioDevice() error = %v", err)
	}
	if resp.GetSuccess() {
		t.Fatal("success = true despite wpctl failing")
	}
	if !strings.Contains(resp.GetErrorMessage(), "Object not found") {
		t.Errorf("message = %q, want wpctl's own output", resp.GetErrorMessage())
	}
}

func TestSetAudioVolume(t *testing.T) {
	// wpctl takes a fraction and reports one back; the API speaks percentages.
	calls := stubGraph(t, graphFixture, func(args ...string) ([]byte, error) {
		if args[0] == "get-volume" {
			return []byte("Volume: 0.35\n"), nil
		}
		return nil, nil
	})

	got, err := testAudioService().setAudioVolume(context.Background(), 43, 35)
	if err != nil {
		t.Fatalf("setAudioVolume() error = %v", err)
	}
	if got != 35 {
		t.Errorf("volume = %d, want 35", got)
	}
	// The unmute is part of setting a volume: the reported volume does not
	// reflect mute, so a muted node would report a volume while staying silent.
	if want := "[set-volume 43 0.35 set-mute 43 0 get-volume 43]"; fmt.Sprint(*calls) != want {
		t.Errorf("wpctl calls = %v, want %s", *calls, want)
	}
}

func TestSetAudioVolumeReportsWhatTookEffect(t *testing.T) {
	// A device with a coarse hardware mixer lands on a nearby step. Report the
	// value the graph actually holds, not the one that was asked for.
	stubGraph(t, graphFixture, func(args ...string) ([]byte, error) {
		if args[0] == "get-volume" {
			return []byte("Volume: 0.48\n"), nil
		}
		return nil, nil
	})

	got, err := testAudioService().setAudioVolume(context.Background(), 43, 50)
	if err != nil {
		t.Fatalf("setAudioVolume() error = %v", err)
	}
	if got != 48 {
		t.Errorf("volume = %d, want the 48 the graph reports", got)
	}
}

func TestSetAudioVolumeValidates(t *testing.T) {
	stubGraph(t, graphFixture, nil)
	svc := testAudioService()

	if _, err := svc.setAudioVolume(context.Background(), 43, 101); status.Code(err) != codes.InvalidArgument {
		t.Errorf("volume 101: error = %v, want codes.InvalidArgument", err)
	}
	if _, err := svc.setAudioVolume(context.Background(), 0, 50); status.Code(err) != codes.InvalidArgument {
		t.Errorf("device ID 0: error = %v, want codes.InvalidArgument", err)
	}
	if _, err := svc.setAudioVolume(context.Background(), 999, 50); status.Code(err) != codes.NotFound {
		t.Errorf("unknown device: error = %v, want codes.NotFound", err)
	}
}

func TestNodeVolumes(t *testing.T) {
	// Volume is unavailable on some nodes; those are omitted rather than
	// reported as zero, which would look like silence.
	stubGraph(t, graphFixture, func(args ...string) ([]byte, error) {
		if args[1] == "43" {
			return []byte("Volume: 0.50\n"), nil
		}
		return []byte("Node has no volume\n"), nil
	})

	svc := testAudioService()
	resp, err := svc.ListAudioDevices(context.Background(), &agentpb.ListAudioDevicesRequest{})
	if err != nil {
		t.Fatalf("ListAudioDevices() error = %v", err)
	}

	volumes := svc.nodeVolumes(context.Background(), resp.GetDevices())
	if len(volumes) != 1 {
		t.Fatalf("got %d volumes, want only the one node that has a volume", len(volumes))
	}
	if v, ok := volumes[43]; !ok || *v != 50 {
		t.Errorf("volumes[43] = %v, want 50", volumes[43])
	}
}

func TestCaptureTarget(t *testing.T) {
	stubGraph(t, graphFixture, nil)
	ctx := context.Background()

	// pw-record takes an object serial, not the object ID used everywhere else.
	if got, err := captureTarget(ctx, 55); err != nil || got != "88" {
		t.Errorf("captureTarget(55) = %q, %v; want the USB mic's serial 88", got, err)
	}

	// Unspecified means the default source.
	if got, err := captureTarget(ctx, 0); err != nil || got != "251" {
		t.Errorf("captureTarget(0) = %q, %v; want the default source's serial 251", got, err)
	}

	// Recording from a speaker is not a thing; say so rather than letting
	// pw-record fail obscurely.
	_, err := captureTarget(ctx, 43)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("captureTarget(sink) error = %v, want codes.InvalidArgument", err)
	}

	if _, err := captureTarget(ctx, 999); status.Code(err) != codes.NotFound {
		t.Errorf("captureTarget(unknown) error = %v, want codes.NotFound", err)
	}
}

func TestCaptureTargetFallsBackToAnySource(t *testing.T) {
	// No default.audio.source metadata at all: still record from something.
	noDefaults := strings.Replace(graphFixture,
		`{ "subject": 0, "key": "default.audio.source", "value": { "name": "bluez_input.78:2B:64:76:F3:CE" } }`,
		`{ "subject": 0, "key": "default.video.source", "value": { "name": "v4l2_input.platform" } }`, 1)
	stubGraph(t, noDefaults, nil)

	got, err := captureTarget(context.Background(), 0)
	if err != nil {
		t.Fatalf("captureTarget(0) error = %v", err)
	}
	// The lowest node id wins, so an unspecified device records from the same
	// microphone every time rather than following pw-dump's graph order.
	if got != "251" {
		t.Errorf("captureTarget(0) = %q, want the lowest-id source's serial 251", got)
	}
}

func TestCaptureTargetNoInputs(t *testing.T) {
	stubGraph(t, `[]`, nil)

	_, err := captureTarget(context.Background(), 0)
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("error = %v, want codes.FailedPrecondition", err)
	}
}
