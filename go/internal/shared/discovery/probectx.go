package discovery

import "context"

// probeContextKey marks a context as running underneath a LAN discovery probe.
type probeContextKey struct{}

// WithinProbe marks ctx as belonging to a discovery probe, so code further down
// the call chain can refuse to re-enter discovery.
//
// This exists to break a recursive cycle. A discovery session probes each
// candidate device to confirm its agent answers; in the CLI that probe dials the
// device, and the dial path falls back to an mDNS browse when it cannot resolve
// a ".local" name. That browse is another discovery session, which probes its
// own candidates, which dial, which browse — a tree with a branching factor of
// probeWorkers per level and no bound on depth beyond each level's timeout. It
// cost 934 MB of live heap in one `wendy device logs`, 83% of the process, most
// of it thousands of duplicate parsed copies of the CLI config that each level
// re-read from disk.
//
// The mark rides the context rather than a parameter because the cycle closes
// seven frames below the probe, through a seam that exists for tests; threading
// a flag through all of them would put the knowledge of this cycle in every
// intermediate signature instead of at the two ends that care.
//
// A probe never needs the browse it would trigger: the session already resolved
// the device's addresses from the mDNS record it is probing and hands them to
// the prober. Re-browsing to find what discovery just found is the bug, not a
// capability being given up.
func WithinProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, probeContextKey{}, true)
}

// IsWithinProbe reports whether ctx descends from a discovery probe.
func IsWithinProbe(ctx context.Context) bool {
	v, _ := ctx.Value(probeContextKey{}).(bool)
	return v
}
