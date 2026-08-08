package discovery

import (
	"context"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// LANEventKind classifies a LANEvent emitted by a streaming LAN discovery scan.
type LANEventKind int

const (
	LANCached  LANEventKind = iota // cache entry, not yet verified this run
	LANFound                       // live-confirmed (mDNS resolve or probe)
	LANUpdated                     // an already-emitted device changed
	LANOffline                     // cached entry failed verification
)

// LANEvent is a single update emitted while streaming LAN discovery results.
type LANEvent struct {
	Kind   LANEventKind
	Device models.LANDevice
	// Probed: Device's AgentVersion/OS/IsMTLS were confirmed by a live agent
	// probe (not just mDNS TXT records).
	Probed bool
}

// LANProber verifies a device by talking to its agent. On success the
// returned device carries refreshed AgentVersion/DeviceType/OS/OSVersion/
// CPUArchitecture and IsMTLS reflecting the actual connection.
type LANProber func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error)

// StreamOptions configures a streaming LAN discovery scan.
type StreamOptions struct {
	UseCache bool      // emit cached entries and persist discoveries
	Prober   LANProber // nil = no probing (mDNS-only confirmation)
}
