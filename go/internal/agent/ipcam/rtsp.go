package ipcam

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Default RTSP paths. Reolink uses these and they are the de facto convention for
// ONVIF cameras exposing a two-stream profile, which makes them a better fallback
// than refusing to stream from a camera found by lease alone.
const (
	defaultMainPath = "/h264Preview_01_main"
	defaultSubPath  = "/h264Preview_01_sub"
	defaultRTSPPort = 554
)

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

// StreamURL builds the RTSP URL for a camera.
//
// Credentials go through url.UserPassword so they are percent-encoded: a password
// containing '@' or ':' would otherwise change which host the pipeline connects
// to.
func StreamURL(c Camera, cred Credential, choice StreamChoice) (string, error) {
	if c.Address == "" {
		return "", ErrNoAddress
	}
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
	u := &url.URL{
		Scheme: "rtsp",
		Host:   net.JoinHostPort(c.Address, strconv.Itoa(defaultRTSPPort)),
		Path:   path,
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

// PipelineArgs returns the gst-launch-1.0 arguments that pull an RTSP stream and
// write Annex-B H.264 to stdout.
//
// There is deliberately no decode or encode element: the camera already emits
// H.264, and the agent's job is to depayload it and hand the bytes to the same
// broadcast path a USB camera's native H.264 uses. config-interval=-1 repeats SPS
// and PPS before every keyframe, which is what lets a client joining mid-stream
// start decoding, and what the command-line interface's keyframe buffer relies
// on. TCP interleaving is forced so UDP loss does not surface as decode
// corruption.
func PipelineArgs(streamURL string) []string {
	return []string{
		"rtspsrc",
		"location=" + streamURL,
		"protocols=tcp",
		"latency=200",
		"timeout=5000000",
		"!", "rtph264depay",
		"!", "h264parse", "config-interval=-1",
		"!", "video/x-h264,stream-format=byte-stream,alignment=au",
		"!", "fdsink", "fd=1",
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
