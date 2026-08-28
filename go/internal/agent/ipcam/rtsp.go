package ipcam

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Default RTSP paths. Reolink uses these and they are the de facto convention for
// ONVIF cameras exposing a two-stream profile, which makes them a better fallback
// than refusing to stream from a camera found by lease alone.
const (
	defaultMainPath = "/h264Preview_01_main"
	defaultSubPath  = "/h264Preview_01_sub"
	// RTSPPort is the port a camera serves streams on, and the one to test when
	// deciding whether a camera is reachable at all.
	RTSPPort        = 554
	defaultRTSPPort = RTSPPort
)

// ReachTimeout bounds a reachability test against a camera. A camera on its own
// cabled segment answers in milliseconds; two seconds is generous and is far
// better than the twenty a stalled RTSP connect takes to give up.
const ReachTimeout = 2 * time.Second

// Reachable reports whether a camera accepts a TCP connection on its RTSP port.
//
// This tests the port the stream actually needs, rather than the registry's
// Online flag: that flag comes from a probe of port 80, which some cameras do
// not serve at all, so gating a stream on it would refuse cameras that work.
func Reachable(address string) bool {
	if address == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort(address, strconv.Itoa(RTSPPort)), ReachTimeout)
	if err != nil {
		return false
	}
	conn.Close() //nolint:errcheck
	return true
}

// StreamChoice selects which of a camera's streams to open.
type StreamChoice int

const (
	// StreamAuto prefers the sub-stream: it is the smooth, low-cost feed and the
	// right default for a viewer window.
	StreamAuto StreamChoice = iota
	StreamSubOnly
	StreamMainOnly
)

// subWidthCeiling is the largest requested width still served by the sub-stream.
// Above it the caller wants detail and gets the main stream.
const subWidthCeiling = 1024

// ChooseStream maps a requested frame width onto a stream. Zero means the caller
// expressed no preference.
func ChooseStream(width uint32) StreamChoice {
	switch {
	case width == 0:
		return StreamAuto
	case width <= subWidthCeiling:
		return StreamSubOnly
	default:
		return StreamMainOnly
	}
}

// ErrNoAddress is returned when a camera has no known address to dial.
var ErrNoAddress = errors.New("camera has no known address")

// streamPath returns the RTSP request path for the requested stream,
// normalized to a leading slash and falling back to the widely-implemented
// default when the camera has no stored path for that stream yet.
//
// Extracted from StreamURL so the URL builder and the credential probe
// (probe.go, which needs a path to DESCRIBE but has no use for the rest of a
// stream URL) cannot drift onto two different notions of "the camera's
// stream path".
func streamPath(c Camera, choice StreamChoice) string {
	var path string
	switch choice {
	case StreamMainOnly:
		path = c.StreamMain
		if path == "" {
			path = defaultMainPath
		}
	default:
		path = c.StreamSub
		if path == "" {
			path = defaultSubPath
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// StreamURL builds the RTSP URL for a camera.
//
// Credentials go through url.UserPassword so they are percent-encoded: a password
// containing '@' or ':' would otherwise change which host the pipeline connects
// to.
func StreamURL(c Camera, cred Credential, choice StreamChoice) (string, error) {
	if c.Address == "" {
		return "", ErrNoAddress
	}
	u := &url.URL{
		Scheme: "rtsp",
		Host:   net.JoinHostPort(c.Address, strconv.Itoa(defaultRTSPPort)),
		Path:   streamPath(c, choice),
	}
	if cred.Username != "" {
		u.User = url.UserPassword(cred.Username, cred.Password)
	}
	return u.String(), nil
}

// RedactURL replaces any credentials in rawurl with a placeholder. Every log line
// and error message that mentions a stream URL goes through this.
func RedactURL(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		// Unparseable input is not echoed back: it may itself be a
		// credential-bearing string we failed to understand.
		return "<redacted url>"
	}
	if u.User == nil {
		return rawurl
	}
	u.User = url.User("<redacted>")
	return u.String()
}

// credentialInURL matches the userinfo of any URL, which is the only shape a
// camera password takes in a GStreamer diagnostic: it is carried in the pipeline
// as the userinfo of an rtsp:// location and is echoed back inside that URL.
var credentialInURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^\s/@]*@`)

// RedactText removes credentials from free-form text so a GStreamer diagnostic
// can be shown instead of discarded.
//
// Two passes, because either alone is insufficient. The URL pass catches the
// password wherever a library echoes the location back, including forms this
// code never built. The literal pass catches a secret that appears outside a URL,
// such as a property dump. Anything unparseable stays redacted rather than being
// let through.
func RedactText(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "<redacted>")
	}
	return credentialInURL.ReplaceAllString(text, "${1}<redacted>@")
}

// SecretsIn returns the credential material carried by pipeline tokens, so a
// diagnostic produced from those tokens can be scrubbed of it.
func SecretsIn(args []string) []string {
	var out []string
	for _, arg := range args {
		raw, ok := strings.CutPrefix(arg, "location=")
		if !ok {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.User == nil {
			continue
		}
		out = append(out, u.User.Username())
		if password, set := u.User.Password(); set {
			out = append(out, password)
		}
		// The percent-encoded spelling is what actually appears in the pipeline
		// string, and it differs from the decoded one whenever the password
		// contains a reserved character.
		out = append(out, u.User.String())
	}
	return out
}

// PipelineArgs returns GStreamer pipeline tokens that pull an RTSP stream and
// write Annex-B H.264 to a file descriptor. The agent feeds these tokens to the
// GStreamer library in-process, rather than putting the credential-bearing URL
// in a child process's command line.
//
// There is deliberately no decode or encode element: the camera already emits
// H.264, and the agent's job is to depayload it and hand the bytes to the same
// broadcast path a USB camera's native H.264 uses. config-interval=-1 repeats SPS
// and PPS before every keyframe, which is what lets a client joining mid-stream
// start decoding, and what the command-line interface's keyframe buffer relies
// on. TCP interleaving is forced so UDP loss does not surface as decode
// corruption.
//
// Two settings exist purely to keep the relay from adding latency of its own:
//
// latency=0 — a jitter buffer absorbs reordering and variable delay, both of
// which are properties of RTP over UDP. This pipeline forces TCP, where the
// transport already guarantees ordered delivery, so any jitter buffer depth is
// delay added to every frame in exchange for nothing. drop-on-latency stays at
// its default of false, so a depth of zero delays rather than discards.
//
// sync=false — fdsink inherits GstBaseSink, which by default holds each buffer
// until its presentation time. That is right for a sink that renders and wrong
// for one that relays: it paces the byte stream to the pipeline clock, adding
// the pipeline's latency a second time on top of the jitter buffer's. Whatever
// eventually displays these frames does its own timing.
func PipelineArgs(streamURL string) []string {
	return []string{
		"rtspsrc",
		"location=" + streamURL,
		"protocols=tcp",
		"latency=0",
		"timeout=5000000",
		"!", "rtph264depay",
		"!", "h264parse", "config-interval=-1",
		"!", "video/x-h264,stream-format=byte-stream,alignment=au",
		"!", "fdsink", "fd=1", "sync=false",
	}
}

// LoopbackPipelineArgs returns GStreamer pipeline tokens that decode an RTSP
// H.264 stream to raw frames and write them into a v4l2loopback node at
// devicePath. Unlike PipelineArgs there is no fd sink: the node is the sink,
// and it is the container-facing device Loopback creates and Task C3's
// supervisor feeds.
//
// This does transcode, unlike PipelineArgs — avdec_h264 and videoconvert are
// required, not optional, because v4l2sink's CAPTURE side needs a real pixel
// format: nothing consuming /dev/videoN as a normal camera device (a
// container's own OpenCV, GStreamer, or FFmpeg pipeline) can be expected to
// speak compressed H.264 off a v4l2 node the way the command-line interface's
// own broadcast path does.
//
// location=streamURL carries the same credential-bearing form PipelineArgs
// does, so SecretsIn/RedactText — keyed on any "location=" token — scrub a
// diagnostic from this pipeline exactly the way they already scrub one from
// PipelineArgs.
func LoopbackPipelineArgs(streamURL, devicePath string) []string {
	return []string{
		"rtspsrc", "location=" + streamURL, "protocols=tcp", "latency=0", "timeout=5000000",
		"!", "rtph264depay", "!", "h264parse", "config-interval=-1",
		"!", "avdec_h264", "!", "videoconvert",
		"!", "v4l2sink", "device=" + devicePath, "sync=false",
	}
}

// FormatUnreachable builds the operator-facing message for a camera we cannot
// reach, naming the address and when it was last seen.
func FormatUnreachable(c Camera) string {
	if c.LastSeen.IsZero() {
		return fmt.Sprintf("camera %d at %s has never been reached", c.ID, c.Address)
	}
	return fmt.Sprintf("camera %d at %s is unreachable, last seen %s",
		c.ID, c.Address, c.LastSeen.Format("2006-01-02 15:04:05"))
}
