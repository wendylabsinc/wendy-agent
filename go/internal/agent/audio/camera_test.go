package audio

import "testing"

// cameraDump is a trimmed pw-dump from an AGX Thor with a Logitech BRIO: two
// V4L2 sources sharing one node.name, the Device object that shares the id
// space, and an audio node that must not match.
const cameraDump = `[
  {"id": 31, "type": "PipeWire:Interface:Device",
   "info": {"props": {"media.class": "Video/Device", "api.v4l2.path": "/dev/video0",
                      "object.serial": 60}}},
  {"id": 63, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
                      "object.serial": 68,
                      "node.name": "v4l2_input.platform-a80aa10000.usb-usb-0_3.2_1.0"}}},
  {"id": 61, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video2",
                      "object.serial": 65,
                      "node.name": "v4l2_input.platform-a80aa10000.usb-usb-0_3.2_1.0"}}},
  {"id": 51, "type": "PipeWire:Interface:Node",
   "info": {"props": {"media.class": "Audio/Source", "object.serial": 51,
                      "node.name": "alsa_input.platform-sound.analog-stereo"}}}
]`

func TestParseCameraSource(t *testing.T) {
	tests := []struct {
		name       string
		devicePath string
		want       uint64
	}{
		// Both cameras share one node.name, so only the serial tells them apart.
		{"first camera", "/dev/video0", 68},
		{"second camera", "/dev/video2", 65},
		{"device object only", "/dev/video1", 0},
		{"absent device", "/dev/video9", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCameraSource([]byte(cameraDump), tt.devicePath)
			if (tt.want != 0) != ok {
				t.Fatalf("ok = %v, want %v", ok, tt.want != 0)
			}
			if got != tt.want {
				t.Errorf("serial = %d, want %d", got, tt.want)
			}
		})
	}
}

// A node without object.serial must not abandon the scan: a later entry for the
// same device is still usable, and giving up sends the caller to the device the
// graph could have shared.
func TestParseCameraSource_SkipsNodeMissingSerial(t *testing.T) {
	dump := `[
	  {"id": 1, "type": "PipeWire:Interface:Node",
	   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0"}}},
	  {"id": 2, "type": "PipeWire:Interface:Node",
	   "info": {"props": {"media.class": "Video/Source", "api.v4l2.path": "/dev/video0",
	                      "object.serial": 99}}}
	]`
	got, ok := parseCameraSource([]byte(dump), "/dev/video0")
	if !ok || got != 99 {
		t.Errorf("serial = %d, ok = %v; want 99, true", got, ok)
	}
}

// A malformed dump leaves the caller on the device rather than failing a stream.
func TestParseCameraSource_MalformedDump(t *testing.T) {
	for _, dump := range []string{"", "not json", "{}", "[]"} {
		if _, ok := parseCameraSource([]byte(dump), "/dev/video0"); ok {
			t.Errorf("dump %q: reported a node that does not exist", dump)
		}
	}
}
