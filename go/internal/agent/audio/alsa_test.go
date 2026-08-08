package audio

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Shape taken from real aplay -l / arecord -l output on a Raspberry Pi 4: two
// HDMI playback devices and one USB capture device.
const aplayFixture = `**** List of PLAYBACK Hardware Devices ****
card 0: vc4hdmi0 [vc4-hdmi-0], device 0: MAI PCM i2s-hifi-0 [MAI PCM i2s-hifi-0]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 1: vc4hdmi1 [vc4-hdmi-1], device 0: MAI PCM i2s-hifi-0 [MAI PCM i2s-hifi-0]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
`

const arecordFixture = `**** List of CAPTURE Hardware Devices ****
card 2: C920 [HD Pro Webcam C920], device 0: USB Audio [USB Audio]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
`

func TestListAlsaNodes(t *testing.T) {
	origAplay, origArecord := AplayListRun, ArecordListRun
	t.Cleanup(func() { AplayListRun, ArecordListRun = origAplay, origArecord })
	AplayListRun = func(context.Context) ([]byte, error) { return []byte(aplayFixture), nil }
	ArecordListRun = func(context.Context) ([]byte, error) { return []byte(arecordFixture), nil }

	nodes, err := ListAlsaNodes(context.Background())
	if err != nil {
		t.Fatalf("ListAlsaNodes() error = %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (two HDMI outputs, one USB input); got %+v", len(nodes), nodes)
	}

	hdmi0, ok := FindNode(nodes, EncodeAlsaID(0, 0))
	if !ok {
		t.Fatal("card 0 device 0 missing")
	}
	if !hdmi0.IsSink || hdmi0.Name != "plughw:0,0" {
		t.Errorf("hdmi0 = %+v, want a sink named plughw:0,0", hdmi0)
	}

	mic, ok := FindNode(nodes, EncodeAlsaID(2, 0))
	if !ok {
		t.Fatal("card 2 device 0 missing")
	}
	if mic.IsSink {
		t.Error("USB capture device marked as a sink")
	}

	// Sorted by ID so a listing is stable.
	for i := 1; i < len(nodes); i++ {
		if nodes[i-1].ID >= nodes[i].ID {
			t.Errorf("nodes are not sorted by id: %d before %d", nodes[i-1].ID, nodes[i].ID)
		}
	}
}

func TestListAlsaNodesNoCards(t *testing.T) {
	origAplay, origArecord := AplayListRun, ArecordListRun
	t.Cleanup(func() { AplayListRun, ArecordListRun = origAplay, origArecord })
	failure := errors.New("no soundcards found")
	AplayListRun = func(context.Context) ([]byte, error) { return nil, failure }
	ArecordListRun = func(context.Context) ([]byte, error) { return nil, failure }

	if _, err := ListAlsaNodes(context.Background()); err == nil {
		t.Fatal("expected an error when neither aplay nor arecord finds a card")
	}
}

func TestEncodeDecodeAlsaID(t *testing.T) {
	for _, tc := range []struct{ card, device uint64 }{
		{0, 0}, {0, 1}, {2, 0}, {255, 255},
	} {
		id := EncodeAlsaID(tc.card, tc.device)
		if id == 0 {
			t.Fatalf("EncodeAlsaID(%d, %d) = 0, which collides with the unspecified sentinel", tc.card, tc.device)
		}
		gotCard, gotDevice := DecodeAlsaID(id)
		if gotCard != tc.card || gotDevice != tc.device {
			t.Errorf("DecodeAlsaID(EncodeAlsaID(%d, %d)) = (%d, %d)", tc.card, tc.device, gotCard, gotDevice)
		}
	}
	if card, device := DecodeAlsaID(0); card != 0 || device != 0 {
		t.Errorf("DecodeAlsaID(0) = (%d, %d), want (0, 0)", card, device)
	}
}

func TestParseSimpleMixerControls(t *testing.T) {
	input := "Simple mixer control 'PCM',0\nSimple mixer control 'Mic',0\nignored line"
	want := []string{"PCM", "Mic"}
	if got := parseSimpleMixerControls(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSimpleMixerControls() = %v, want %v", got, want)
	}
}

func TestParsePlaybackVolume(t *testing.T) {
	input := "Simple mixer control 'PCM',0\n" +
		"  Capabilities: pvolume pvolume-joined pswitch pswitch-joined\n" +
		"  Playback channels: Mono\n" +
		"  Mono: Playback 231 [100%] [-2.50dB] [on]"
	volume, ok := parsePlaybackVolume(input)
	if !ok || volume != 100 {
		t.Fatalf("parsePlaybackVolume() = %d, %v; want 100, true", volume, ok)
	}

	if _, ok := parsePlaybackVolume("Mono: Capture 12 [22%]"); ok {
		t.Fatal("capture-only controls must not be treated as playback volume")
	}
}

func TestOrderPlaybackControls(t *testing.T) {
	got := orderPlaybackControls([]string{"Mic", "Speaker", "PCM", "Digital"})
	want := []string{"PCM", "Speaker", "Mic", "Digital"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderPlaybackControls() = %v, want %v", got, want)
	}
}

func TestMixerControlPrefersPCMAndSkipsCaptureOnly(t *testing.T) {
	orig := AmixerRun
	t.Cleanup(func() { AmixerRun = orig })

	var calls [][]string
	AmixerRun = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch strings.Join(args, " ") {
		case "-c 2 scontrols":
			return []byte("Simple mixer control 'Mic',0\nSimple mixer control 'PCM',0\n"), nil
		case "-c 2 sget PCM":
			return []byte("Mono: Playback 51 [22%] [-20.04dB] [on]\n"), nil
		default:
			return nil, errors.New("unexpected amixer call")
		}
	}

	control, volume, err := mixerControl(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if control != "PCM" || volume != 22 {
		t.Fatalf("mixerControl() = %q, %d; want PCM, 22", control, volume)
	}
	wantCalls := [][]string{{"-c", "2", "scontrols"}, {"-c", "2", "sget", "PCM"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
}

func TestSetAlsaVolume(t *testing.T) {
	orig := AmixerRun
	t.Cleanup(func() { AmixerRun = orig })

	var calls [][]string
	AmixerRun = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch strings.Join(args, " ") {
		case "-c 2 scontrols":
			return []byte("Simple mixer control 'PCM',0\n"), nil
		case "-c 2 sget PCM":
			return []byte("Mono: Playback 51 [22%] [on]\n"), nil
		case "-c 2 sset PCM 100% unmute":
			return []byte("Mono: Playback 231 [100%] [on]\n"), nil
		default:
			return nil, errors.New("unexpected amixer call")
		}
	}

	actual, err := SetAlsaVolume(context.Background(), 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if actual != 100 {
		t.Fatalf("actual volume = %d, want 100", actual)
	}
	wantLast := []string{"-c", "2", "sset", "PCM", "100%", "unmute"}
	if !reflect.DeepEqual(calls[len(calls)-1], wantLast) {
		t.Fatalf("set call = %v, want %v", calls[len(calls)-1], wantLast)
	}
}

func TestAlsaVolumeNoControl(t *testing.T) {
	orig := AmixerRun
	t.Cleanup(func() { AmixerRun = orig })
	AmixerRun = func(context.Context, ...string) ([]byte, error) {
		return []byte(""), nil
	}

	if _, ok := AlsaVolume(context.Background(), 0); ok {
		t.Error("AlsaVolume() ok = true with no mixer controls at all")
	}
}

func TestArecordCommand(t *testing.T) {
	cmd := ArecordCommand(context.Background(), "plughw:1,0", 44100, 2)
	want := []string{"arecord", "-q", "-D", "plughw:1,0", "-f", "S16_LE", "-r", "44100", "-c", "2", "-t", "raw", "-"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("ArecordCommand args = %v, want %v", cmd.Args, want)
	}
}
