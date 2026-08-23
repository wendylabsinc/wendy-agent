package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/audio"
	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

// The audio adapter records at a fixed s16le / 48 kHz / mono format and writes
// self-contained WAV files, so fragments and segments are decodable without a
// sidecar index. This is the smallest honest implementation: it is levels
// driven (peak dBFS per chunk via computeAudioLevels) and seals raw PCM into
// WAV. A fuller version would carry the input's native rate and channel count,
// map hardware capture timestamps instead of the agent receipt, and support
// per-crossing pre-roll from the rolling buffer.
const (
	audioSampleRate      = 48000
	audioChannels        = 1
	audioBytesPerSample  = 2
	audioBytesPerSecond  = audioSampleRate * audioChannels * audioBytesPerSample
	audioChunkBytes      = audioBytesPerSecond / 10 // ~100 ms per read
	audioSegmentDuration = 10 * time.Second
	audioSegmentBytes    = int64(audioBytesPerSecond) * int64(audioSegmentDuration/time.Second)
	// defaultAudioFragment is the sealed duration when a threshold source omits
	// an explicit fragment.
	defaultAudioFragment = 10 * time.Second
	// audioLevelField is the only field the audio threshold trigger understands.
	audioLevelField = "level_db"
	wavHeaderBytes  = 44
)

// audioStream is the PCM source the capture loop reads from. It is an interface
// so tests can inject deterministic PCM without a live capture process.
type audioStream interface {
	Read([]byte) (int, error)
	Close()
	Err() string
}

// pcmCapture adapts the audio service's *capture process to audioStream.
type pcmCapture struct{ c *capture }

func (p *pcmCapture) Read(b []byte) (int, error) { return p.c.stdout.Read(b) }
func (p *pcmCapture) Close()                     { p.c.Close() }
func (p *pcmCapture) Err() string                { return p.c.Err() }

type audioDataAdapter struct {
	audio *AudioService
	// openStream is a seam over the real PCM capture; tests inject fake PCM.
	openStream func(ctx context.Context, deviceID uint32) (audioStream, error)
}

func newAudioDataAdapter(audioSvc *AudioService) dataCaptureAdapter {
	if audioSvc == nil {
		return nil
	}
	a := &audioDataAdapter{audio: audioSvc}
	a.openStream = a.defaultOpenStream
	return a
}

func (a *audioDataAdapter) defaultOpenStream(ctx context.Context, deviceID uint32) (audioStream, error) {
	target, err := captureTarget(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	rec, err := startCapture(ctx, target, audioSampleRate, audioChannels, "100ms")
	if err != nil {
		return nil, err
	}
	return &pcmCapture{c: rec}, nil
}

// audioHubDMAEndpointReason is appended to the description of an audio-hub DMA
// endpoint. It has to tell an operator both what is wrong and what would fix
// it, because the endpoint opens and streams perfectly: the only symptom is
// that every sample is zero.
const audioHubDMAEndpointReason = "audio hub DMA endpoint, not a physical input: captures digital silence unless an external I2S codec is wired and the audio hub is routed to it"

// admaifEndpointPattern matches the ALSA device identity that the Tegra Audio
// Processing Engine's ASoC driver gives an Audio DMA Interface (ADMAIF)
// front-end, as "arecord -l" renders it. On a Jetson Orin Nano that is
// "fe.admaif@290f000.ADMAIF1 (*) []" through to ADMAIF20.
//
// Matching a driver-formatted string is fragile, which is why it is isolated
// here behind isAudioHubDMAEndpoint: a future kernel may rename these, and this
// is the single place to fix. It is nevertheless the right signal, and the two
// seemingly more authoritative alternatives were measured on hardware and
// rejected:
//
//   - The "(*)" and "[]" in the name do not mean "unrouted". /proc/asound/
//     card2/pcm0c/info reports id "fe.admaif@290f000.ADMAIF1 (*)" with an empty
//     name field, and arecord prints "<id> [<name>]" — so "[]" is just the
//     empty PCM name and "(*)" is ASoC's marker for a dynamic PCM (a DPCM
//     front-end). Both are structural to an ADMAIF, present routed or not. Only
//     the "admaif" identity itself carries meaning.
//
//   - The audio hub crossbar mixer state is authoritative about routing, but
//     routing is not the question. On wendyos-hubert the "ADMAIF1 Mux" control
//     reads I2S2 and "ADMAIF2 Mux" reads I2S4 (NVIDIA's default board config),
//     yet a one-second capture from the routed plughw:2,0 still returns 192044
//     bytes containing zero non-zero samples, because /sys/kernel/debug/asoc/
//     components lists no codec beyond two snd-soc-dummy entries: the I2S ports
//     terminate in nothing. A mux-based health rule would therefore call
//     ADMAIF1 and ADMAIF2 healthy while they deliver pure silence, which is the
//     exact lie this check exists to remove — and it would cost twenty amixer
//     subprocesses per enumeration to get there.
//
// So health is decided on the structural question the identity does answer:
// this is a memory endpoint of the on-chip audio hub crossbar, not a physical
// capture input. The endpoint is still enumerated, because someone who wires an
// I2S codec and routes the hub has a legitimately usable ADMAIF and hiding it
// would blind us to real hardware. It is reported unhealthy rather than absent,
// and the reason names what to check.
var admaifEndpointPattern = regexp.MustCompile(`(?i)admaif@[0-9a-f]+\.admaif[0-9]+`)

// isAudioHubDMAEndpoint reports whether an audio source detail names an on-chip
// audio-hub DMA endpoint rather than a physical capture device.
func isAudioHubDMAEndpoint(detail string) bool {
	return admaifEndpointPattern.MatchString(detail)
}

func (a *audioDataAdapter) Discover(ctx context.Context) []data.Source {
	var nodes []audio.Node
	domain := "PIPEWIRE_CAPTURE/AGENT_RECEIPT"
	if audio.Available() {
		var err error
		nodes, _, err = audio.ListNodes(ctx)
		if err != nil {
			return nil
		}
	} else {
		var err error
		nodes, err = audio.ListAlsaNodes(ctx)
		if err != nil {
			return nil
		}
		domain = "ALSA_CAPTURE/AGENT_RECEIPT"
	}
	out := make([]data.Source, 0, len(nodes))
	for _, n := range nodes {
		if n.IsSink {
			continue
		}
		detail := strings.TrimSpace(n.Description + " " + n.Name)
		healthy := true
		if isAudioHubDMAEndpoint(detail) {
			healthy = false
			detail = strings.TrimSpace(detail + "; " + audioHubDMAEndpointReason)
		}
		out = append(out, data.Source{
			ID:          fmt.Sprintf("audio:%d", n.ID),
			Kind:        "audio",
			ClockDomain: domain,
			Healthy:     healthy,
			Detail:      detail,
		})
	}
	return out
}

func audioDeviceID(sourceID string) (uint32, bool) {
	if !strings.HasPrefix(sourceID, "audio:") {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(sourceID, "audio:"), 10, 32)
	return uint32(n), err == nil
}

func (a *audioDataAdapter) Start(ctx context.Context, session data.CaptureSession, selected []data.Source) (runningDataCapture, error) {
	group := &audioCaptureGroup{}
	for _, source := range selected {
		devID, ok := audioDeviceID(source.ID)
		if !ok {
			continue
		}
		capture, err := a.startOne(ctx, session, source, devID)
		if err != nil {
			_, _ = group.Stop(context.Background())
			return nil, fmt.Errorf("%s: %w", source.ID, err)
		}
		group.captures = append(group.captures, capture)
	}
	if len(group.captures) == 0 {
		return nil, nil
	}
	return group, nil
}

func (a *audioDataAdapter) startOne(ctx context.Context, session data.CaptureSession, source data.Source, devID uint32) (*audioCapture, error) {
	mode := "continuous"
	var notes []string
	var op string
	var threshold float64
	var fragmentBytes int64

	capture := source.Capture
	if capture.EffectiveMode() == "threshold" {
		field, operator, value, err := data.ParseFieldThreshold(capture.Trigger)
		if err != nil {
			return nil, err
		}
		if field != audioLevelField {
			notes = append(notes, fmt.Sprintf("audio threshold triggers only on %s, not %q; recording continuously", audioLevelField, field))
		} else {
			mode = "threshold"
			op, threshold = operator, value
			fragment := defaultAudioFragment
			if d := capture.Fragment; d != "" {
				if parsed, perr := time.ParseDuration(d); perr == nil && parsed > 0 {
					fragment = parsed
				} else {
					notes = append(notes, fmt.Sprintf("fragment %q is not a positive duration; sealing %s per crossing", d, defaultAudioFragment))
				}
			} else {
				notes = append(notes, fmt.Sprintf("threshold source declared no fragment; sealing %s per crossing", defaultAudioFragment))
			}
			fragmentBytes = int64(fragment.Seconds() * float64(audioBytesPerSecond))
			notes = append(notes, fmt.Sprintf("threshold capture: sealing a %s fragment when %s %s %.3g dBFS", fragment, audioLevelField, op, threshold))
		}
	}

	stream, err := a.openStream(ctx, devID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(session.Directory, "audio", safeCaptureName(source.ID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		stream.Close()
		return nil, err
	}
	index, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		stream.Close()
		return nil, err
	}

	captureCtx, cancel := context.WithCancel(context.Background())
	c := &audioCapture{
		source: source, session: session, dir: dir, stream: stream, index: index,
		ctx: captureCtx, cancel: cancel, done: make(chan struct{}), ready: make(chan error, 1),
		mode: mode, op: op, threshold: threshold, fragmentBytes: fragmentBytes, notes: notes,
	}
	go c.run()
	select {
	case err := <-c.ready:
		if err != nil {
			c.shutdown()
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		c.shutdown()
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		c.shutdown()
		return nil, errors.New("timed out waiting for first audio chunk")
	}
}

// shutdown cancels the capture context and closes the PCM stream, unblocking a
// pending Read, then waits for run to finish.
func (c *audioCapture) shutdown() {
	c.cancel()
	c.stream.Close()
	<-c.done
}

type audioCaptureGroup struct{ captures []*audioCapture }

func (g *audioCaptureGroup) Stop(ctx context.Context) ([]data.CaptureResult, error) {
	var results []data.CaptureResult
	var errs []error
	for i := len(g.captures) - 1; i >= 0; i-- {
		r, err := g.captures[i].Stop(ctx)
		results = append(results, r...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return results, errors.Join(errs...)
}

type audioCapture struct {
	source  data.Source
	session data.CaptureSession
	dir     string
	stream  audioStream
	index   *os.File

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	ready     chan error
	readyOnce sync.Once
	stopOnce  sync.Once
	result    data.CaptureResult
	runErr    error

	// mode is "continuous" or "threshold".
	mode string
	// Threshold configuration.
	op            string
	threshold     float64
	fragmentBytes int64
	// Threshold state.
	wasAbove bool
	sealing  bool

	// Current segment (WAV) state, shared by both modes.
	seg          *os.File
	segRel       string
	segBytes     int64
	segNumber    int
	segCanonical int64
	segUncert    int64
	segReceipt   int64
	segTrigger   *float64

	// Accounting.
	missedCrossings   uint64
	maxCanonicalError int64
	firstOffset       *int64
	notes             []string

	// captureReceipt is a test seam over data.CaptureReceipt.
	captureReceipt func() (int64, int64, int64, error)
}

func (c *audioCapture) receiptNow() (int64, int64, int64, error) {
	if c.captureReceipt != nil {
		return c.captureReceipt()
	}
	return data.CaptureReceipt()
}

func (c *audioCapture) signalReady(err error) { c.readyOnce.Do(func() { c.ready <- err }) }

// run reads PCM until the stream ends. It does not poll c.ctx: Stop closes the
// stream, which unblocks Read and drains the buffered PCM, so a stop never
// truncates data the producer already delivered.
func (c *audioCapture) run() {
	defer close(c.done)
	defer c.finish()
	buf := make([]byte, audioChunkBytes)
	for {
		n, err := c.stream.Read(buf)
		if n > 0 {
			if perr := c.handleChunk(buf[:n]); perr != nil {
				c.runErr = perr
				c.signalReady(perr)
				return
			}
			c.signalReady(nil)
		}
		if err != nil {
			if err != io.EOF {
				if msg := c.stream.Err(); msg != "" {
					c.runErr = errors.New(msg)
				} else {
					c.runErr = err
				}
			}
			c.signalReady(c.runErr)
			return
		}
	}
}

func (c *audioCapture) handleChunk(pcm []byte) error {
	if c.mode == "threshold" {
		return c.handleThresholdChunk(pcm)
	}
	return c.handleContinuousChunk(pcm)
}

// handleThresholdChunk detects a rising crossing of the level threshold and
// seals a fragment of the configured duration. A crossing that occurs while a
// fragment is still being sealed is counted as a missed crossing (an honest
// drop) rather than starting an overlapping fragment.
func (c *audioCapture) handleThresholdChunk(pcm []byte) error {
	peak, _ := computeAudioLevels(pcm)
	aboveNow := data.CompareThreshold(float64(peak), c.op, c.threshold)
	crossing := aboveNow && !c.wasAbove
	c.wasAbove = aboveNow

	if crossing {
		if c.sealing {
			c.missedCrossings++
		} else {
			level := float64(peak)
			if err := c.beginSegment(&level); err != nil {
				return err
			}
			c.sealing = true
		}
	}
	if !c.sealing {
		return nil
	}
	remaining := c.fragmentBytes - c.segBytes
	if remaining <= 0 {
		if err := c.finishSegment(false); err != nil {
			return err
		}
		c.result.Count++
		c.sealing = false
		return nil
	}
	write := pcm
	if int64(len(write)) > remaining {
		write = write[:remaining]
	}
	if err := c.writeSegment(write); err != nil {
		return err
	}
	if c.segBytes >= c.fragmentBytes {
		if err := c.finishSegment(false); err != nil {
			return err
		}
		c.result.Count++
		c.sealing = false
	}
	return nil
}

// handleContinuousChunk appends PCM to a rotating WAV segment.
func (c *audioCapture) handleContinuousChunk(pcm []byte) error {
	if c.seg == nil {
		if err := c.beginSegment(nil); err != nil {
			return err
		}
	}
	if err := c.writeSegment(pcm); err != nil {
		return err
	}
	if c.segBytes >= audioSegmentBytes {
		if err := c.finishSegment(false); err != nil {
			return err
		}
		c.result.Count++
	}
	return nil
}

// beginSegment stamps the segment start on the canonical episode timeline and
// opens a WAV file with a placeholder header (patched on finish).
func (c *audioCapture) beginSegment(triggerLevel *float64) error {
	canonical, uncertainty, receipt, err := c.canonicalTime()
	if err != nil {
		return err
	}
	c.segNumber++
	prefix := "segment"
	if c.mode == "threshold" {
		prefix = "fragment"
	}
	name := fmt.Sprintf("%s-%06d.wav", prefix, c.segNumber)
	f, err := os.OpenFile(filepath.Join(c.dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		return err
	}
	if err := writeWAVHeader(f, audioSampleRate, audioChannels, 0); err != nil {
		f.Close()
		return err
	}
	c.seg = f
	c.segRel = filepath.ToSlash(filepath.Join("audio", safeCaptureName(c.source.ID), name))
	c.segBytes = 0
	c.segCanonical = canonical
	c.segUncert = uncertainty
	c.segReceipt = receipt
	c.segTrigger = triggerLevel
	if c.firstOffset == nil {
		offset := canonical - c.session.RequestBootNanos
		c.firstOffset = &offset
	}
	return nil
}

func (c *audioCapture) writeSegment(pcm []byte) error {
	n, err := c.seg.Write(pcm)
	if err != nil {
		return err
	}
	if n != len(pcm) {
		return ioErrShortWrite(n, len(pcm))
	}
	c.segBytes += int64(n)
	return nil
}

// finishSegment patches the WAV header with the real data size, closes the
// file, and records the segment in index.jsonl.
func (c *audioCapture) finishSegment(truncated bool) error {
	if c.seg == nil {
		return nil
	}
	dataBytes := c.segBytes
	var writeErr error
	if _, err := c.seg.Seek(0, io.SeekStart); err != nil {
		writeErr = err
	} else if err := writeWAVHeader(c.seg, audioSampleRate, audioChannels, uint32(dataBytes)); err != nil {
		writeErr = err
	}
	if syncErr := c.seg.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := c.seg.Close(); writeErr == nil {
		writeErr = closeErr
	}
	c.seg = nil
	if writeErr != nil {
		return writeErr
	}
	record := audioIndexRecord{
		CanonicalEpisodeNanos:     c.segCanonical - c.session.RequestBootNanos,
		CanonicalUncertaintyNanos: c.segUncert,
		AgentReceiptBootNanos:     c.segReceipt,
		MappingSegment:            "receipt-bracket-v1",
		Segment:                   c.segRel,
		ByteSize:                  dataBytes,
		SampleRate:                audioSampleRate,
		Channels:                  audioChannels,
		DurationNanos:             dataBytes * int64(time.Second) / int64(audioBytesPerSecond),
		Mode:                      c.mode,
		TriggerLevelDb:            c.segTrigger,
		Truncated:                 truncated,
	}
	b, _ := json.Marshal(record)
	_, err := c.index.Write(append(b, '\n'))
	c.segBytes = 0
	return err
}

func (c *audioCapture) canonicalTime() (canonical, uncertainty, receipt int64, err error) {
	before, mid, after, err := c.receiptNow()
	if err != nil {
		return 0, 0, 0, err
	}
	canonical = mid
	uncertainty = (after - before + 1) / 2
	if uncertainty > c.maxCanonicalError {
		c.maxCanonicalError = uncertainty
	}
	return canonical, uncertainty, mid, nil
}

// finish flushes any in-progress segment and folds accounting into the result.
func (c *audioCapture) finish() {
	if c.seg != nil {
		// A fragment cut short by capture stop is still a valid, shorter WAV.
		truncated := c.mode == "threshold" && c.sealing
		if truncated {
			c.notes = append(c.notes, "final fragment truncated by capture stop")
		}
		if err := c.finishSegment(truncated); err == nil {
			c.result.Count++
		}
	}
	c.result.SourceID = c.source.ID
	c.result.ClockDomain = c.source.ClockDomain
	c.result.ActualOffset = c.firstOffset
	if c.maxCanonicalError > 0 {
		maxError := c.maxCanonicalError
		c.result.MappingError = &maxError
	}
	if c.mode == "threshold" {
		missed := c.missedCrossings
		c.result.Drops = &missed
		c.result.DropAccounting = "missed_threshold_crossings_during_seal"
	} else {
		c.result.DropAccounting = "pcm_stream_has_no_sequence_numbers"
	}
	if len(c.notes) > 0 {
		c.result.SourceDetail = strings.TrimSpace(c.source.Detail + " (" + strings.Join(c.notes, "; ") + ")")
	}
	_ = c.index.Sync()
	_ = c.index.Close()
}

func (c *audioCapture) Stop(context.Context) ([]data.CaptureResult, error) {
	c.stopOnce.Do(c.shutdown)
	return []data.CaptureResult{c.result}, c.runErr
}

type audioIndexRecord struct {
	CanonicalEpisodeNanos     int64    `json:"canonical_episode_nanos"`
	CanonicalUncertaintyNanos int64    `json:"canonical_uncertainty_nanos"`
	AgentReceiptBootNanos     int64    `json:"agent_receipt_boottime_nanos"`
	MappingSegment            string   `json:"mapping_segment"`
	Segment                   string   `json:"segment"`
	ByteSize                  int64    `json:"byte_size"`
	SampleRate                uint32   `json:"sample_rate"`
	Channels                  uint32   `json:"channels"`
	DurationNanos             int64    `json:"duration_nanos"`
	Mode                      string   `json:"mode"`
	TriggerLevelDb            *float64 `json:"trigger_level_db,omitempty"`
	Truncated                 bool     `json:"truncated,omitempty"`
}

// writeWAVHeader writes a 44-byte canonical PCM WAV header at the file cursor.
// dataBytes is the size of the PCM payload that follows; it is zero for the
// placeholder written before recording and the real size when patched on close.
func writeWAVHeader(f *os.File, sampleRate, channels, dataBytes uint32) error {
	var buf [wavHeaderBytes]byte
	byteRate := sampleRate * channels * audioBytesPerSample
	blockAlign := uint16(channels * audioBytesPerSample)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataBytes)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], byteRate)
	binary.LittleEndian.PutUint16(buf[32:34], blockAlign)
	binary.LittleEndian.PutUint16(buf[34:36], 16) // bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataBytes)
	_, err := f.Write(buf[:])
	return err
}
