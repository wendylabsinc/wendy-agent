package oshealth

import "time"

// CriticalService is a systemd unit that must be healthy for an OS update to
// be committed.
type CriticalService struct {
	// Unit is the systemd unit name, e.g. "avahi-daemon.service".
	Unit string
	// Timeout is how long to wait for the unit to become active before
	// declaring the healthcheck failed. Units still start in parallel with
	// the agent at boot, so this must absorb normal startup latency.
	Timeout time.Duration
}

// DefaultCriticalServices is the healthcheck list applied after an OS update.
// To protect an additional service, append an entry here. Units that are not
// present on a device (or are intentionally disabled) are skipped, so entries
// do not have to exist on every device type.
var DefaultCriticalServices = []CriticalService{
	{Unit: "avahi-daemon.service", Timeout: 30 * time.Second},
	{Unit: "containerd.service", Timeout: 60 * time.Second},
	{Unit: "NetworkManager.service", Timeout: 60 * time.Second},
}
