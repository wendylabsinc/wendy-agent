// Package oshealth implements post-OS-update healthchecks for critical
// system services and the commit-or-rollback decision for pending A/B
// updates. A backend that runs its own health gate inside commit
// (wendyos-update runs /etc/wendyos-update/health.d) delegates the verdict to
// its commit result instead.
package oshealth

import (
	"os"
	"strings"
	"time"
)

const (
	// DefaultStateDir is on the WendyOS data partition, which is shared
	// between both A/B rootfs slots — state written here before a reboot is
	// visible to whichever slot boots next.
	DefaultStateDir = "/data/wendy-agent"

	pendingMarkerFile = "pending-update.json"

	// MaxPendingMarkerAge bounds how long a pending-update marker is trusted.
	// A marker can outlive its update when the freshly booted slot ships an
	// agent without healthcheck support, which commits without consuming it.
	MaxPendingMarkerAge = time.Hour
)

// PendingMarker records that an OS update was installed and a reboot into the
// new slot is pending verification. It is written right before the
// post-install reboot.
type PendingMarker struct {
	CreatedAt    time.Time `json:"created_at"`
	OldOSVersion string    `json:"old_os_version,omitempty"`
	ArtifactURL  string    `json:"artifact_url,omitempty"`
	AgentVersion string    `json:"agent_version,omitempty"`
	// BootID is the kernel boot ID of the boot that installed the update. The
	// gate only consumes the marker on a *different* boot: seeing the same
	// boot ID means the device has not rebooted into the new slot yet, so
	// there is nothing to verify (e.g. the agent restarted between install
	// and reboot).
	BootID string `json:"boot_id,omitempty"`
	// Backend identifies the OS update backend that installed this update
	// (currently only "wendyos-update"), so the next boot's gate commits or
	// rolls back with the same backend. Empty on markers written before
	// multi-backend support; the gate then auto-selects (see
	// requestedBackendFromMarker). A marker can still carry a legacy value
	// such as "mender" if it was written by an old agent shortly before an
	// upgrade; an unrecognized value resolves to no backend, so the gate
	// degrades to keep-marker-and-retry until the marker goes stale.
	Backend string `json:"backend,omitempty"`
}

// CurrentBootID returns the kernel's boot ID, which uniquely identifies the
// current boot. Empty when unavailable (non-Linux, restricted /proc); callers
// must treat empty as "cannot verify" and fail open.
func CurrentBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func WritePendingMarker(dir string, m PendingMarker) error {
	return writeJSONAtomic(dir, pendingMarkerFile, m)
}

func ReadPendingMarker(dir string) (PendingMarker, bool, error) {
	var m PendingMarker
	found, err := readJSON(dir, pendingMarkerFile, &m)
	if err != nil {
		return PendingMarker{}, false, err
	}
	return m, found, nil
}

func ClearPendingMarker(dir string) error {
	return removeIfExists(dir, pendingMarkerFile)
}
