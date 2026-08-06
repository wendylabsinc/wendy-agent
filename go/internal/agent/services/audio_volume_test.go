package services

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestParseSimpleMixerControls(t *testing.T) {
	input := `Simple mixer control 'PCM',0
Simple mixer control 'Mic',0
ignored line`
	want := []string{"PCM", "Mic"}
	if got := parseSimpleMixerControls(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSimpleMixerControls() = %v, want %v", got, want)
	}
}

func TestParsePlaybackVolume(t *testing.T) {
	input := `Simple mixer control 'PCM',0
  Capabilities: pvolume pvolume-joined pswitch pswitch-joined
  Playback channels: Mono
  Mono: Playback 231 [100%] [-2.50dB] [on]`
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
	original := amixerRun
	t.Cleanup(func() { amixerRun = original })

	var calls [][]string
	amixerRun = func(_ context.Context, args ...string) ([]byte, error) {
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

func TestPopulatePlaybackVolumesOnlyTouchesOutputsAndCachesByCard(t *testing.T) {
	original := amixerRun
	t.Cleanup(func() { amixerRun = original })

	listCalls := 0
	amixerRun = func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-c 2 scontrols":
			listCalls++
			return []byte("Simple mixer control 'PCM',0\n"), nil
		case "-c 2 sget PCM":
			return []byte("Mono: Playback 150 [65%] [on]\n"), nil
		default:
			return nil, errors.New("unexpected amixer call")
		}
	}

	devices := []*agentpb.AudioDevice{
		{Id: 257, Type: agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_INPUT},
		{Id: 513, Type: agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_OUTPUT},
		{Id: 514, Type: agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_OUTPUT},
	}
	volumes := NewAudioService(zap.NewNop()).playbackVolumes(context.Background(), devices)

	if volumes[devices[0].GetId()] != nil {
		t.Fatal("input device unexpectedly has playback volume")
	}
	if len(volumes) != 2 {
		t.Fatalf("volume map has %d entries, want 2 output IDs", len(volumes))
	}
	for _, device := range devices[1:] {
		if volumes[device.GetId()] == nil || *volumes[device.GetId()] != 65 {
			t.Fatalf("output volume = %v, want 65", volumes[device.GetId()])
		}
	}
	if listCalls != 1 {
		t.Fatalf("mixer was listed %d times for one card, want 1", listCalls)
	}
}

func TestSetAudioVolume(t *testing.T) {
	original := amixerRun
	t.Cleanup(func() { amixerRun = original })

	var calls [][]string
	amixerRun = func(_ context.Context, args ...string) ([]byte, error) {
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

	actual, err := NewAudioService(zap.NewNop()).setAudioVolume(context.Background(), 513, 100)
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

func TestSetAudioVolumeValidatesRange(t *testing.T) {
	service := NewAudioService(zap.NewNop())
	_, err := service.setAudioVolume(context.Background(), 513, 101)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}
