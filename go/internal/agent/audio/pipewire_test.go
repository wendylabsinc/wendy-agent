package audio

import (
	"os"
	"path/filepath"
	"testing"
)

// Shapes taken from real pw-dump output on a Raspberry Pi 4 with a Bose
// Revolve II paired: the two HDMI cards PipeWire's ALSA monitor exposes, the
// Bluetooth sink and source, and the default-device metadata object. The
// Bluetooth entries are the ones aplay/arecord can never see.
const dumpFixture = `[
  {
    "id": 30,
    "type": "PipeWire:Interface:Metadata",
    "props": { "metadata.name": "default" },
    "metadata": [
      { "subject": 0, "key": "default.video.source", "type": "Spa:String:JSON", "value": { "name": "v4l2_input.platform" } },
      { "subject": 0, "key": "default.audio.sink", "type": "Spa:String:JSON", "value": { "name": "bluez_output.78_2B_64_76_F3_CE.1" } },
      { "subject": 0, "key": "default.audio.source", "type": "Spa:String:JSON", "value": { "name": "bluez_input.78:2B:64:76:F3:CE" } }
    ]
  },
  {
    "id": 43,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 242,
      "factory.name": "api.bluez5.a2dp.sink",
      "media.class": "Audio/Sink",
      "node.name": "bluez_output.78_2B_64_76_F3_CE.1",
      "node.description": "Bose Revolve II SoundLink"
    } }
  },
  {
    "id": 44,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": "251",
      "media.class": "Audio/Source",
      "node.name": "bluez_input.78:2B:64:76:F3:CE",
      "node.description": "Bose Revolve II SoundLink"
    } }
  },
  {
    "id": 62,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "object.serial": 60,
      "media.class": "Audio/Sink",
      "node.name": "alsa_output.platform-fef00700.hdmi.hdmi-stereo",
      "node.description": "Built-in Audio"
    } }
  },
  {
    "id": 70,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "media.class": "Video/Source",
      "node.name": "v4l2_input.platform",
      "node.description": "bcm2835-isp"
    } }
  },
  {
    "id": 71,
    "type": "PipeWire:Interface:Node",
    "info": { "props": {
      "media.class": "Audio/Sink",
      "node.description": "nameless node is skipped"
    } }
  }
]`

func TestParseDump(t *testing.T) {
	nodes, defaults, err := parseDump([]byte(dumpFixture))
	if err != nil {
		t.Fatalf("parseDump() error = %v", err)
	}

	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (two Bluetooth, one ALSA-backed); got %+v", len(nodes), nodes)
	}

	// The Bluetooth sink: the device aplay -l cannot represent at all.
	bt, ok := FindNode(nodes, 43)
	if !ok {
		t.Fatal("Bluetooth sink (id 43) missing")
	}
	if !bt.IsSink {
		t.Error("Bluetooth sink not marked as a sink")
	}
	if bt.Description != "Bose Revolve II SoundLink" {
		t.Errorf("description = %q", bt.Description)
	}
	// object.serial is what pw-record --target takes, and is not object.id.
	if bt.Serial != 242 {
		t.Errorf("serial = %d, want 242", bt.Serial)
	}

	// pw-dump quotes some numeric properties and not others.
	src, _ := FindNode(nodes, 44)
	if src.Serial != 251 {
		t.Errorf("string-encoded serial = %d, want 251", src.Serial)
	}
	if src.IsSink {
		t.Error("Audio/Source marked as a sink")
	}

	// PipeWire's ALSA monitor exposes sound cards as nodes, which is why
	// enumerating PipeWire alone still covers everything ALSA would.
	if _, ok := FindNode(nodes, 62); !ok {
		t.Error("ALSA-backed sink (id 62) missing; PipeWire enumeration must cover sound cards")
	}

	// Video nodes and nodes without a node.name are not audio devices.
	if _, ok := FindNode(nodes, 70); ok {
		t.Error("Video/Source was listed as an audio device")
	}
	if _, ok := FindNode(nodes, 71); ok {
		t.Error("node without node.name was listed")
	}

	if defaults.SinkName != "bluez_output.78_2B_64_76_F3_CE.1" {
		t.Errorf("default sink = %q", defaults.SinkName)
	}
	if defaults.SourceName != "bluez_input.78:2B:64:76:F3:CE" {
		t.Errorf("default source = %q", defaults.SourceName)
	}

	// Defaults are per-direction, so a sink is never the default source even
	// when both belong to the same physical device.
	hdmi, _ := FindNode(nodes, 62)
	for _, tc := range []struct {
		node Node
		want bool
	}{{bt, true}, {src, true}, {hdmi, false}} {
		if got := defaults.IsDefault(tc.node); got != tc.want {
			t.Errorf("IsDefault(%q) = %v, want %v", tc.node.Name, got, tc.want)
		}
	}
}

func TestParseDumpMalformed(t *testing.T) {
	if _, _, err := parseDump([]byte("not json")); err == nil {
		t.Error("expected an error for malformed pw-dump output")
	}
	nodes, defaults, err := parseDump([]byte(`[]`))
	if err != nil || len(nodes) != 0 || defaults.SinkName != "" {
		t.Errorf("empty dump: nodes=%v defaults=%+v err=%v", nodes, defaults, err)
	}
}

func TestParseVolume(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   uint32
		ok     bool
	}{
		{"half", "Volume: 0.50\n", 50, true},
		{"full", "Volume: 1.00\n", 100, true},
		{"zero", "Volume: 0.00\n", 0, true},
		// Muting does not change the underlying volume, so it is ignored.
		{"muted", "Volume: 0.43 [MUTED]\n", 43, true},
		{"rounds", "Volume: 0.335\n", 34, true},
		// wpctl can be asked about a node that has no volume at all.
		{"no volume line", "Node 43 has no volume\n", 0, false},
		{"empty", "", 0, false},
		{"garbage", "Volume: banana\n", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseVolume(tt.output)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parseVolume(%q) = %d, %v; want %d, %v", tt.output, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	orig := SocketGlob
	t.Cleanup(func() { SocketGlob = orig })

	// No session: callers must be able to tell, so they can report audio as
	// unavailable rather than silently listing nothing.
	SocketGlob = filepath.Join(dir, "user-*", "pipewire-0")
	if got := RuntimeDir(); got != "" {
		t.Errorf("no session should yield \"\", got %q", got)
	}

	// A plain file is not a socket and must not be mistaken for a session.
	sessionDir := filepath.Join(dir, "user-1000")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "pipewire-0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RuntimeDir(); got != "" {
		t.Errorf("a regular file should not count as a session, got %q", got)
	}
}
