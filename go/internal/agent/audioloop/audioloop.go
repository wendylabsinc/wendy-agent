// Package audioloop is the microphone analogue of ipcam's v4l2loopback
// manager: it turns a demanded microphone channel into a PCM sink backed by
// the snd-aloop kernel module.
//
// Unlike v4l2loopback, snd-aloop has no per-node ioctl — its card and
// subdevices are fixed at module load (pcm_substreams, default 8), so
// "allocation" here is just picking a free subdevice index in
// [0, MaxSubdevices), mirroring mcusource.Supervisor.nodeID's stable
// per-key allocator over a fixed band.
package audioloop

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"go.uber.org/zap"
)

// MaxSubdevices is snd-aloop's default pcm_substreams: subdevices 0..7 on
// card "Loopback".
const MaxSubdevices = 8

// PCMFormat describes the raw PCM stream written to a subdevice.
type PCMFormat struct {
	SampleRate uint32
	Channels   uint32
}

// AudioWriter is an open PCM sink: WritePCM feeds raw interleaved S16_LE
// samples to it, Close tears it down.
type AudioWriter interface {
	WritePCM(pcm []byte) error
	Close() error
}

// deps seams every subprocess audioloop touches, so the package builds and
// its allocator/arg-builder tests run on macOS. Linux's real implementation
// lives in audioloop_linux.go; the non-Linux stub lives in audioloop_other.go.
type deps struct {
	// modprobe loads snd-aloop, idempotently (already-loaded is success).
	modprobe func(ctx context.Context) error
	// newWriter opens an aplay-backed PCM sink on the given ALSA hw ID
	// (e.g. "hw:Loopback,0,3").
	newWriter func(hwID string, f PCMFormat) (AudioWriter, error)
}

// Manager allocates snd-aloop subdevices and opens PCM writers on them.
type Manager struct {
	logger *zap.Logger
	deps   deps

	modprobeOnce sync.Once
	modprobeErr  error

	mu   sync.Mutex
	subs map[string]int // "sourceAssetID:channelID" -> subdevice index
}

// NewManager returns a Manager wired to the real, platform-specific deps.
// logger may be nil.
func NewManager(logger *zap.Logger) *Manager {
	return &Manager{
		logger: logger,
		deps:   defaultDeps(),
		subs:   make(map[string]int),
	}
}

// EnsureModule loads snd-aloop, once. Subsequent calls reuse the first
// attempt's result rather than re-running modprobe every time.
func (m *Manager) EnsureModule(ctx context.Context) error {
	m.modprobeOnce.Do(func() {
		m.modprobeErr = m.deps.modprobe(ctx)
	})
	return m.modprobeErr
}

// Allocate returns a stable snd-aloop subdevice index for (sourceAssetID,
// channelID), allocating the lowest free index in [0, MaxSubdevices) on
// first use. Mirrors mcusource.Supervisor.nodeID.
func (m *Manager) Allocate(sourceAssetID int32, channelID uint32) (int, error) {
	key := fmt.Sprintf("%d:%d", sourceAssetID, channelID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if sub, ok := m.subs[key]; ok {
		return sub, nil
	}
	used := make(map[int]bool, len(m.subs))
	for _, sub := range m.subs {
		used[sub] = true
	}
	for sub := 0; sub < MaxSubdevices; sub++ {
		if !used[sub] {
			m.subs[key] = sub
			return sub, nil
		}
	}
	return 0, fmt.Errorf("audioloop: snd-aloop subdevice band [0,%d) exhausted", MaxSubdevices)
}

// OpenWriter ensures snd-aloop is loaded and opens a PCM writer on
// subdevice sub of card "Loopback".
func (m *Manager) OpenWriter(ctx context.Context, sub int, f PCMFormat) (AudioWriter, error) {
	if err := m.EnsureModule(ctx); err != nil {
		return nil, err
	}
	hwID := "hw:Loopback,0," + strconv.Itoa(sub)
	return m.deps.newWriter(hwID, f)
}

// aplayArgs builds the argv aplay needs to play raw S16_LE PCM into hwID.
//
// buffer-time/period-time cap the snd-aloop ring at ~200ms (8×25ms periods).
// Without them aplay picks the device default (~500ms here), and every frame
// then sits that long in the ring before a consumer reads it — pure added
// latency for a live mic. But too tight (we tried 100ms) and network jitter
// or Mac↔device clock drift overruns the ring and drops audio; 200ms is the
// balance — small periods keep it responsive, the deeper ring absorbs jitter.
func aplayArgs(hwID string, f PCMFormat) []string {
	return []string{
		"-D", hwID,
		"-f", "S16_LE",
		"-r", strconv.FormatUint(uint64(f.SampleRate), 10),
		"-c", strconv.FormatUint(uint64(f.Channels), 10),
		"-t", "raw",
		"--buffer-time=200000",
		"--period-time=25000",
		"-q", "-",
	}
}
