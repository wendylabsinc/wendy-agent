package services

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

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
