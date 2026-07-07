// Package diskspace defines the WendyOS device disk-space policy: the free-%
// thresholds shared by the agent's image garbage collector and the CLI's
// `wendy device doctor` diagnostics, plus a helper to measure a filesystem's
// free percentage. Keeping the thresholds in one neutral leaf package lets both
// the agent and the CLI agree on when a device is "too full" without either
// importing the other's internals.
package diskspace

// Free-space thresholds, expressed as a percentage of total filesystem
// capacity. They are the single source of truth for "how full is too full" on a
// WendyOS device, consumed by both `wendy device doctor` (which warns/fails its
// disk check) and the agent image GC (which reclaims stale image data once free
// space drops below WarnFreePct).
const (
	// WarnFreePct is the free-% at or below which a filesystem is considered
	// under disk pressure: doctor warns, and the image GC engages.
	WarnFreePct = 10.0
	// FailFreePct is the free-% at or below which a filesystem is critically
	// full: doctor fails (new deploys are at risk of running out of space).
	FailFreePct = 2.0
)
