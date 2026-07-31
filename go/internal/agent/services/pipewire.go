package services

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"go.uber.org/zap"
)

// The kernel lets exactly one process stream from a V4L2 node, so an app holding
// /dev/video0 locks every viewer out. PipeWire breaks that tie: WirePlumber owns
// the device and fans the frames out to as many clients as ask for them. When a
// node exists for a camera the agent captures through it instead of opening the
// device itself.

// pipewireRuntimeDir is where the system-wide PipeWire daemon opens its socket
// (systemd RuntimeDirectory=pipewire). A client with no XDG session finds it
// there by default, but one started from a login shell would follow that
// session's XDG_RUNTIME_DIR to a socket that does not exist — so name it.
const pipewireRuntimeDir = "/run/pipewire"

// pipewireLookupTimeout bounds pw-dump so a wedged daemon cannot stall capture.
const pipewireLookupTimeout = 2 * time.Second

// pipewireEnv is the environment for a PipeWire client subprocess. An operator
// override of PIPEWIRE_RUNTIME_DIR wins.
func pipewireEnv() []string {
	env := os.Environ()
	if os.Getenv("PIPEWIRE_RUNTIME_DIR") != "" {
		return env
	}
	return append(env, "PIPEWIRE_RUNTIME_DIR="+pipewireRuntimeDir)
}

// pipewireDumpEntry is the slice of `pw-dump` output this file reads.
type pipewireDumpEntry struct {
	Info struct {
		State string `json:"state"`
		Props struct {
			MediaClass string `json:"media.class"`
			V4L2Path   string `json:"api.v4l2.path"`
			NodeName   string `json:"node.name"`
		} `json:"props"`
	} `json:"info"`
}

// pipewireCamera is what the graph knows about one camera.
type pipewireCamera struct {
	// node is the node.name to hand pipewiresrc, or "" when PipeWire has no node
	// for the device. node.name comes from the device's bus path, so unlike an
	// object id it survives a WirePlumber restart.
	node string
	// inUse reports that another client is already pulling frames. Only then is
	// routing through the graph worth giving up a camera's own H.264 stream for.
	inUse bool
}

// findPipeWireCamera locates the source publishing devicePath in a pw-dump.
func findPipeWireCamera(dump []byte, devicePath string) pipewireCamera {
	var entries []pipewireDumpEntry
	if err := json.Unmarshal(dump, &entries); err != nil {
		return pipewireCamera{}
	}
	for _, e := range entries {
		p := e.Info.Props
		if p.MediaClass == "Video/Source" && p.V4L2Path == devicePath && isValidPipeWireNodeName(p.NodeName) {
			// A node with no consumer reads "idle" or "suspended".
			return pipewireCamera{node: p.NodeName, inUse: e.Info.State == "running"}
		}
	}
	return pipewireCamera{}
}

// pipewireCameraFor asks the running PipeWire daemon about devicePath. Every
// failure returns the zero value — no daemon, no pw-dump, no node — which leaves
// the caller on the direct V4L2 path it used before.
func (s *VideoService) pipewireCameraFor(ctx context.Context, devicePath string) pipewireCamera {
	dumpPath, err := exec.LookPath("pw-dump")
	if err != nil {
		return pipewireCamera{}
	}

	ctx, cancel := context.WithTimeout(ctx, pipewireLookupTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, dumpPath)
	cmd.Env = pipewireEnv()
	out, err := cmd.Output()
	if err != nil {
		s.logger.Debug("pw-dump failed, capturing directly from the device", zap.Error(err))
		return pipewireCamera{}
	}
	return findPipeWireCamera(out, devicePath)
}

// isValidPipeWireNodeName guards the one externally sourced string interpolated
// into the pipeline for the PipeWire path. The pipeline is later split with
// strings.Fields, so anything outside this set — whitespace above all — is
// rejected rather than escaped.
func isValidPipeWireNodeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}
