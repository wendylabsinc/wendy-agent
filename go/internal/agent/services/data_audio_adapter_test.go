package services

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/audio"
	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

// fakeAudioStream yields a fixed sequence of PCM chunks and then EOF.
type fakeAudioStream struct {
	chunks [][]byte
	i      int
}

func (f *fakeAudioStream) Read(b []byte) (int, error) {
	if f.i >= len(f.chunks) {
		return 0, io.EOF
	}
	n := copy(b, f.chunks[f.i])
	f.i++
	return n, nil
}
func (f *fakeAudioStream) Close()      {}
func (f *fakeAudioStream) Err() string { return "" }

func silentChunk() []byte { return make([]byte, audioChunkBytes) }

func loudChunk() []byte {
	b := make([]byte, audioChunkBytes)
	for i := 0; i+1 < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], 0x7FFF) // near full-scale s16
	}
	return b
}

// runAudioCapture drives the adapter with a scripted PCM stream and returns the
// capture result plus the audio output directory.
func runAudioCapture(t *testing.T, capture *data.SourceCapture, chunks [][]byte) (data.CaptureResult, string) {
	t.Helper()
	sessionDir := t.TempDir()
	source := data.Source{ID: "audio:0", Kind: "audio", ClockDomain: "TEST_CAPTURE/AGENT_RECEIPT", Healthy: true, Detail: "test mic", Capture: capture}
	adapter := &audioDataAdapter{
		audio: &AudioService{},
		openStream: func(context.Context, uint32) (audioStream, error) {
			return &fakeAudioStream{chunks: chunks}, nil
		},
	}
	session := data.CaptureSession{ID: "ep", Directory: sessionDir, RequestBootNanos: 0}
	running, err := adapter.Start(context.Background(), session, []data.Source{source})
	if err != nil {
		t.Fatalf("adapter start: %v", err)
	}
	if running == nil {
		t.Fatal("adapter returned no running capture")
	}
	results, err := running.Stop(context.Background())
	if err != nil {
		t.Fatalf("adapter stop: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 capture result, got %d", len(results))
	}
	return results[0], filepath.Join(sessionDir, "audio", safeCaptureName(source.ID))
}

func wavFragments(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wavs []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".wav" {
			wavs = append(wavs, filepath.Join(dir, e.Name()))
		}
	}
	return wavs
}

// wavDataBytes reads the PCM payload size recorded in a WAV file's data chunk
// header and confirms it matches the bytes actually on disk.
func wavDataBytes(t *testing.T, path string) int64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < wavHeaderBytes || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("%s is not a WAV file", path)
	}
	declared := int64(binary.LittleEndian.Uint32(b[40:44]))
	if onDisk := int64(len(b)) - wavHeaderBytes; onDisk != declared {
		t.Fatalf("%s: header declares %d PCM bytes but file holds %d", path, declared, onDisk)
	}
	return declared
}

// TestAudioThresholdSealsFragmentOfConfiguredDuration verifies a level crossing
// seals exactly one fragment of the configured duration.
func TestAudioThresholdSealsFragmentOfConfiguredDuration(t *testing.T) {
	// fragment 1s = 10 chunks of ~100ms.
	chunks := [][]byte{silentChunk(), silentChunk()}
	for i := 0; i < 10; i++ {
		chunks = append(chunks, loudChunk())
	}
	chunks = append(chunks, silentChunk(), silentChunk())

	result, dir := runAudioCapture(t, &data.SourceCapture{Mode: "threshold", Trigger: "level_db > -20", Fragment: "1s"}, chunks)

	wavs := wavFragments(t, dir)
	if len(wavs) != 1 {
		t.Fatalf("expected exactly 1 sealed fragment, got %d (%v)", len(wavs), wavs)
	}
	if got := wavDataBytes(t, wavs[0]); got != int64(audioBytesPerSecond) {
		t.Fatalf("fragment PCM bytes = %d, want %d (1 second)", got, audioBytesPerSecond)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1 sealed fragment", result.Count)
	}
	if result.Drops == nil || *result.Drops != 0 {
		t.Fatalf("expected 0 missed crossings, got %v", result.Drops)
	}
	if result.DropAccounting != "missed_threshold_crossings_during_seal" {
		t.Fatalf("drop accounting = %q", result.DropAccounting)
	}
}

// TestAudioSubThresholdSealsNothing verifies audio that never crosses the
// threshold produces no fragment.
func TestAudioSubThresholdSealsNothing(t *testing.T) {
	chunks := [][]byte{silentChunk(), silentChunk(), silentChunk(), silentChunk()}
	result, dir := runAudioCapture(t, &data.SourceCapture{Mode: "threshold", Trigger: "level_db > -20", Fragment: "1s"}, chunks)

	if wavs := wavFragments(t, dir); len(wavs) != 0 {
		t.Fatalf("expected no fragments for sub-threshold audio, got %v", wavs)
	}
	if result.Count != 0 {
		t.Fatalf("result.Count = %d, want 0", result.Count)
	}
}

// TestAudioThresholdCountsMissedCrossingsDuringSeal verifies a second crossing
// while a fragment is still sealing is counted as a missed crossing rather than
// starting an overlapping fragment.
func TestAudioThresholdCountsMissedCrossingsDuringSeal(t *testing.T) {
	// A long fragment (3s = 30 chunks) so the seal is still in progress when a
	// second crossing occurs. Script: loud (start seal), silent (drop below),
	// loud (a new crossing mid-seal -> missed), then enough loud to finish.
	chunks := [][]byte{loudChunk(), silentChunk(), loudChunk()}
	for i := 0; i < 30; i++ {
		chunks = append(chunks, loudChunk())
	}
	result, dir := runAudioCapture(t, &data.SourceCapture{Mode: "threshold", Trigger: "level_db > -20", Fragment: "3s"}, chunks)

	if wavs := wavFragments(t, dir); len(wavs) != 1 {
		t.Fatalf("expected 1 fragment, got %v", wavs)
	}
	if result.Drops == nil || *result.Drops != 1 {
		t.Fatalf("expected exactly 1 missed crossing during the seal, got %v", result.Drops)
	}
}

// jetsonArecordFixture is the verbatim "arecord -l" output from
// wendyos-hubert.local, a Jetson Orin Nano: one real USB microphone on card 0
// and the twenty Audio DMA Interface front-ends of the Tegra Audio Processing
// Engine on card 2. Every one of the twenty opens, streams and returns pure
// digital silence; a one-second capture from plughw:2,0 yields 192044 bytes
// with zero non-zero samples, while the same capture from the C920 on
// plughw:0,0 yields 159601 non-zero bytes.
const jetsonArecordFixture = `**** List of CAPTURE Hardware Devices ****
card 0: C920 [HD Pro Webcam C920], device 0: USB Audio [USB Audio]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 0: fe.admaif@290f000.ADMAIF1 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 1: fe.admaif@290f000.ADMAIF2 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 2: fe.admaif@290f000.ADMAIF3 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 3: fe.admaif@290f000.ADMAIF4 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 4: fe.admaif@290f000.ADMAIF5 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 5: fe.admaif@290f000.ADMAIF6 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 6: fe.admaif@290f000.ADMAIF7 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 7: fe.admaif@290f000.ADMAIF8 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 8: fe.admaif@290f000.ADMAIF9 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 9: fe.admaif@290f000.ADMAIF10 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 10: fe.admaif@290f000.ADMAIF11 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 11: fe.admaif@290f000.ADMAIF12 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 12: fe.admaif@290f000.ADMAIF13 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 13: fe.admaif@290f000.ADMAIF14 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 14: fe.admaif@290f000.ADMAIF15 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 15: fe.admaif@290f000.ADMAIF16 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 16: fe.admaif@290f000.ADMAIF17 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 17: fe.admaif@290f000.ADMAIF18 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 18: fe.admaif@290f000.ADMAIF19 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 19: fe.admaif@290f000.ADMAIF20 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
`

// jetsonAplayFixture is the playback half from the same device. Card 1 is the
// HDMI codec; the APE card exposes the playback direction of the same twenty
// ADMAIF front-ends. Sinks are skipped by Discover, so these only prove the
// health check never reaches them.
const jetsonAplayFixture = `**** List of PLAYBACK Hardware Devices ****
card 1: HDA [NVIDIA Jetson Orin Nano HDA], device 3: HDMI 0 [HDMI 0]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: APE [NVIDIA Jetson Orin Nano APE], device 0: fe.admaif@290f000.ADMAIF1 (*) []
  Subdevices: 1/1
  Subdevice #0: subdevice #0
`

// discoverWithAlsaFixtures runs Discover over the ALSA fallback path with the
// given aplay/arecord output, which is the path a WendyOS device actually takes
// (the agent has no PipeWire session, so its sources report the
// ALSA_CAPTURE/AGENT_RECEIPT clock domain).
func discoverWithAlsaFixtures(t *testing.T, aplayOut, arecordOut string) []data.Source {
	t.Helper()
	origAvailable, origAplay, origArecord := audio.Available, audio.AplayListRun, audio.ArecordListRun
	t.Cleanup(func() {
		audio.Available, audio.AplayListRun, audio.ArecordListRun = origAvailable, origAplay, origArecord
	})
	audio.Available = func() bool { return false }
	audio.AplayListRun = func(context.Context) ([]byte, error) { return []byte(aplayOut), nil }
	audio.ArecordListRun = func(context.Context) ([]byte, error) { return []byte(arecordOut), nil }

	adapter := &audioDataAdapter{audio: &AudioService{}}
	return adapter.Discover(context.Background())
}

// TestIsAudioHubDMAEndpointOnRealDeviceStrings exercises the detection function
// against the exact strings the kernel produced on hardware, since it parses a
// driver-formatted name and nothing else pins that format.
func TestIsAudioHubDMAEndpointOnRealDeviceStrings(t *testing.T) {
	unrouted := []string{
		"APE [NVIDIA Jetson Orin Nano APE], device 0: fe.admaif@290f000.ADMAIF1 (*) [] plughw:2,0",
		"APE [NVIDIA Jetson Orin Nano APE], device 19: fe.admaif@290f000.ADMAIF20 (*) [] plughw:2,19",
		// /proc/asound/card2/pcm0c/info reports the id without the empty name,
		// so the check must not depend on the trailing "[]".
		"fe.admaif@290f000.ADMAIF1 (*)",
	}
	for _, detail := range unrouted {
		if !isAudioHubDMAEndpoint(detail) {
			t.Errorf("isAudioHubDMAEndpoint(%q) = false, want true", detail)
		}
	}

	physical := []string{
		"C920 [HD Pro Webcam C920], device 0: USB Audio [USB Audio] plughw:0,0",
		"HDA [NVIDIA Jetson Orin Nano HDA], device 3: HDMI 0 [HDMI 0] plughw:1,3",
		"tegra-hsp [tegra-hsp], device 0: ADMA Interface [ADMA] plughw:3,0",
	}
	for _, detail := range physical {
		if isAudioHubDMAEndpoint(detail) {
			t.Errorf("isAudioHubDMAEndpoint(%q) = true, want false", detail)
		}
	}
}

// TestJetsonAudioHubEndpointsAreAllUnhealthy pins the shape of the device that
// exposed this defect: of the twenty-one capture sources a Jetson Orin Nano
// advertises, exactly one is a real microphone. Before this check all
// twenty-one reported healthy, so a campaign could resolve to an endpoint that
// records silence forever without erring at any layer.
func TestJetsonAudioHubEndpointsAreAllUnhealthy(t *testing.T) {
	sources := discoverWithAlsaFixtures(t, jetsonAplayFixture, jetsonArecordFixture)
	if len(sources) != 21 {
		t.Fatalf("got %d audio sources, want 21 (one C920 plus twenty ADMAIF); got %+v", len(sources), sources)
	}

	var healthy, unhealthy []data.Source
	for _, source := range sources {
		if source.Kind != "audio" {
			t.Fatalf("unexpected source kind %q in audio discovery: %+v", source.Kind, source)
		}
		if source.Healthy {
			healthy = append(healthy, source)
		} else {
			unhealthy = append(unhealthy, source)
		}
	}
	if len(healthy) != 1 {
		t.Fatalf("want exactly 1 healthy audio source, got %d: %+v", len(healthy), healthy)
	}
	if len(unhealthy) != 20 {
		t.Fatalf("want all 20 ADMAIF endpoints unhealthy, got %d", len(unhealthy))
	}

	// The one healthy source is the real microphone, and its description
	// survives intact so the source is still identifiable.
	if !strings.Contains(healthy[0].Detail, "HD Pro Webcam C920") || !strings.Contains(healthy[0].Detail, "plughw:0,0") {
		t.Errorf("healthy source is not the C920 with its description intact: %q", healthy[0].Detail)
	}
	if strings.Contains(healthy[0].Detail, audioHubDMAEndpointReason) {
		t.Errorf("a real microphone was annotated with the audio hub reason: %q", healthy[0].Detail)
	}

	// Every unhealthy source keeps its description and gains the reason.
	for _, source := range unhealthy {
		if !strings.Contains(source.Detail, "fe.admaif@290f000.ADMAIF") {
			t.Errorf("unhealthy source lost its description: %q", source.Detail)
		}
		if !strings.Contains(source.Detail, audioHubDMAEndpointReason) {
			t.Errorf("unhealthy source does not say why: %q", source.Detail)
		}
		if !strings.Contains(source.Detail, "plughw:2,") {
			t.Errorf("unhealthy source lost its device address: %q", source.Detail)
		}
	}
}

// TestUsbMicrophoneStaysHealthy proves the check does not cost a normal device
// its health or its description when no audio hub endpoint is present at all.
func TestUsbMicrophoneStaysHealthy(t *testing.T) {
	const arecordOnlyUSB = `**** List of CAPTURE Hardware Devices ****
card 0: C920 [HD Pro Webcam C920], device 0: USB Audio [USB Audio]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
`
	sources := discoverWithAlsaFixtures(t, "**** List of PLAYBACK Hardware Devices ****\n", arecordOnlyUSB)
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1: %+v", len(sources), sources)
	}
	if !sources[0].Healthy {
		t.Errorf("USB microphone reported unhealthy: %+v", sources[0])
	}
	want := "C920 [HD Pro Webcam C920], device 0: USB Audio [USB Audio] plughw:0,0"
	if sources[0].Detail != want {
		t.Errorf("detail = %q, want %q", sources[0].Detail, want)
	}
}
