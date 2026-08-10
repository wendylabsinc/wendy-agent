// ALSA fallback: enumeration, volume and capture for when no PipeWire user
// session is up (see Available). Call these only in that situation.
//
// PipeWire's own ALSA monitor exposes every sound card as a node, so mixing
// this package's two enumerations in one listing would double-list cards and
// risks the exact bug the PipeWire-first design replaced: a Bluetooth
// endpoint reported as present by one surface and absent from another. This
// file exists only to keep basic sound-card playback and capture honest on a
// host with no session manager, not to run alongside PipeWire.

package audio

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AplayListRun and ArecordListRun enumerate ALSA cards. Behind vars so tests
// can supply a fixture instead of running the real tools.
var (
	AplayListRun = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "aplay", "-l").Output()
	}
	ArecordListRun = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "arecord", "-l").Output()
	}
)

// AmixerRun runs amixer for ALSA volume control. Behind a var for tests.
var AmixerRun = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "amixer", args...).CombinedOutput()
}

// Layout of the Node.ID an ALSA endpoint encodes to: device in the low 8 bits,
// card in the next 16, direction in the bit above.
//
// Direction has to be part of the ID because a card/device pair does not
// identify one endpoint. A duplex device — a USB speakerphone, say — is
// reported as the same card and device by both aplay -l and arecord -l, so an
// ID built from card and device alone names its playback and capture halves at
// once, and FindNode resolves it to whichever one the sort happened to place
// first.
const (
	alsaDeviceBits = 8
	alsaCardBits   = 16
	alsaDeviceMask = 1<<alsaDeviceBits - 1
	alsaCardMask   = 1<<alsaCardBits - 1
	// alsaSourceFlag marks a capture endpoint; sinks leave it clear.
	alsaSourceFlag = uint64(1) << (alsaDeviceBits + alsaCardBits)
)

// EncodeAlsaID and DecodeAlsaID convert between an ALSA card/device/direction
// triple and the Node.ID used to address it, so 0 remains the "unspecified"
// sentinel callers already use for PipeWire node IDs.
func EncodeAlsaID(card, device uint64, isSink bool) uint32 {
	encoded := ((card & alsaCardMask) << alsaDeviceBits) | (device & alsaDeviceMask)
	if !isSink {
		encoded |= alsaSourceFlag
	}
	return uint32(encoded + 1)
}

// DecodeAlsaID reverses EncodeAlsaID. The zero sentinel decodes to card 0,
// device 0; its direction is meaningless and callers must not read it.
func DecodeAlsaID(id uint32) (card, device uint64, isSink bool) {
	if id == 0 {
		return 0, 0, false
	}
	encoded := uint64(id) - 1
	card = (encoded >> alsaDeviceBits) & alsaCardMask
	device = encoded & alsaDeviceMask
	return card, device, encoded&alsaSourceFlag == 0
}

// parseAlsaList parses the output of "aplay -l" or "arecord -l": lines of the
// form "card N: CardName [Desc], device M: DeviceName [Desc]".
func parseAlsaList(output string, isSink bool) []Node {
	var nodes []Node
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "card ") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		card, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(parts[0], "card ")), 10, 32)
		if err != nil {
			continue
		}
		var device uint64
		rest := parts[1]
		if idx := strings.Index(rest, ", device "); idx >= 0 {
			after := rest[idx+len(", device "):]
			if ci := strings.Index(after, ":"); ci >= 0 {
				if d, err := strconv.ParseUint(strings.TrimSpace(after[:ci]), 10, 32); err == nil {
					device = d
				}
			}
		}
		nodes = append(nodes, Node{
			ID: EncodeAlsaID(card, device, isSink),
			// plughw, not hw: the plug layer handles format conversion a
			// card's native rate/format may not support directly.
			Name:        fmt.Sprintf("plughw:%d,%d", card, device),
			Description: strings.TrimSpace(rest),
			IsSink:      isSink,
		})
	}
	return nodes
}

// ListAlsaNodes enumerates ALSA cards directly via aplay/arecord. There is no
// notion of a default sink or source at this layer, so callers get a nil
// Defaults and must pick a device themselves (e.g. the first available).
func ListAlsaNodes(ctx context.Context) ([]Node, error) {
	var nodes []Node
	var firstErr error
	for _, s := range []struct {
		run    func(context.Context) ([]byte, error)
		isSink bool
	}{
		{AplayListRun, true},
		{ArecordListRun, false},
	} {
		out, err := s.run(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		nodes = append(nodes, parseAlsaList(string(out), s.isSink)...)
	}
	if len(nodes) == 0 && firstErr != nil {
		return nil, fmt.Errorf("querying ALSA: %w", firstErr)
	}
	// SliceStable, not Slice: an unstable sort leaves the order of any two
	// nodes that compare equal up to the algorithm, which is how a duplicate ID
	// used to resolve to an arbitrary one of the two nodes carrying it. IDs are
	// unique now, so this is belt and braces — but it costs nothing and keeps a
	// listing reproducible if a future card ever collides again.
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes, nil
}

var (
	simpleMixerControlPattern = regexp.MustCompile(`^Simple mixer control '(.+)',\d+$`)
	playbackVolumePattern     = regexp.MustCompile(`\[(\d{1,3})%\]`)
	// preferredPlaybackControls covers the conventional ALSA simple-control
	// names. If none is present, mixerControl falls back to the first control
	// that exposes a playback volume, which keeps USB and board-specific
	// codecs usable.
	preferredPlaybackControls = []string{"Master", "PCM", "Speaker", "Headphone"}
)

func parseSimpleMixerControls(output string) []string {
	var controls []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		if match := simpleMixerControlPattern.FindStringSubmatch(strings.TrimSpace(scanner.Text())); len(match) == 2 {
			controls = append(controls, match[1])
		}
	}
	return controls
}

func orderPlaybackControls(controls []string) []string {
	ordered := make([]string, 0, len(controls))
	used := make(map[int]bool, len(controls))
	for _, preferred := range preferredPlaybackControls {
		for i, control := range controls {
			if !used[i] && strings.EqualFold(control, preferred) {
				ordered = append(ordered, control)
				used[i] = true
			}
		}
	}
	for i, control := range controls {
		if !used[i] {
			ordered = append(ordered, control)
		}
	}
	return ordered
}

func parsePlaybackVolume(output string) (uint32, bool) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "Playback") {
			continue
		}
		match := playbackVolumePattern.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		volume, err := strconv.ParseUint(match[1], 10, 32)
		if err == nil && volume <= 100 {
			return uint32(volume), true
		}
	}
	return 0, false
}

// mixerControl resolves the ALSA simple mixer control that owns playback
// volume for a card and returns its current percentage.
func mixerControl(ctx context.Context, card uint64) (string, uint32, error) {
	cardArg := strconv.FormatUint(card, 10)
	listOutput, err := AmixerRun(ctx, "-c", cardArg, "scontrols")
	if err != nil {
		return "", 0, fmt.Errorf("listing mixer controls: %w: %s", err, strings.TrimSpace(string(listOutput)))
	}

	for _, control := range orderPlaybackControls(parseSimpleMixerControls(string(listOutput))) {
		output, getErr := AmixerRun(ctx, "-c", cardArg, "sget", control)
		if getErr != nil {
			continue
		}
		if volume, ok := parsePlaybackVolume(string(output)); ok {
			return control, volume, nil
		}
	}
	return "", 0, fmt.Errorf("no playback volume control found on ALSA card %d", card)
}

// AlsaVolume reports a card's playback volume as a percentage, or ok=false
// when the card has no discoverable playback control.
func AlsaVolume(ctx context.Context, card uint64) (uint32, bool) {
	_, volume, err := mixerControl(ctx, card)
	return volume, err == nil
}

// SetAlsaVolume sets a card's playback volume from a percentage and unmutes
// it, returning the value the mixer actually landed on (a coarse hardware
// mixer may land on a nearby step rather than the exact request).
func SetAlsaVolume(ctx context.Context, card uint64, percent uint32) (uint32, error) {
	if percent > 100 {
		percent = 100
	}
	control, _, err := mixerControl(ctx, card)
	if err != nil {
		return 0, err
	}
	cardArg := strconv.FormatUint(card, 10)
	percentArg := fmt.Sprintf("%d%%", percent)
	output, err := AmixerRun(ctx, "-c", cardArg, "sset", control, percentArg, "unmute")
	if err != nil {
		return 0, fmt.Errorf("amixer sset %s: %w: %s", control, err, strings.TrimSpace(string(output)))
	}
	actual := percent
	if volume, ok := parsePlaybackVolume(string(output)); ok {
		actual = volume
	}
	return actual, nil
}

// ArecordCommand builds an arecord invocation that emits raw s16le PCM on
// stdout from device (an ALSA -D argument, e.g. "plughw:0,0"), matching
// pw-record's output shape so a caller can treat either source the same way
// once the process is running.
func ArecordCommand(ctx context.Context, device string, sampleRate, channels uint32) *exec.Cmd {
	return exec.CommandContext(ctx, "arecord",
		"-q",
		"-D", device,
		"-f", "S16_LE",
		"-r", strconv.FormatUint(uint64(sampleRate), 10),
		"-c", strconv.FormatUint(uint64(channels), 10),
		"-t", "raw",
		"-",
	)
}
