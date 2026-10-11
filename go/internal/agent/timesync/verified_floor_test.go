package timesync

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// floorTestTime is a plausible verified time. Tests express it against a fake
// boot clock so they never depend on the host's own clock.
var floorTestTime = time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)

// fakeBootClock stands in for CLOCK_BOOTTIME and only moves when told to.
type fakeBootClock struct {
	mu    sync.Mutex
	nanos int64
}

func (c *fakeBootClock) now() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nanos, nil
}

func (c *fakeBootClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nanos += int64(d)
}

func writeFloorFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifiedFloorAt(t *testing.T, dir string) time.Time {
	t.Helper()
	floor, refused := readVerifiedFloor(dir)
	if !refused.IsZero() {
		t.Fatalf("verified floor holds the implausible value %v", refused)
	}
	return floor
}

// The verified floor only moves forward: an older verified time, from a stale
// proof or a slower source, must not undo a newer one.
func TestAdvanceVerifiedFloorRaisesButNeverLowers(t *testing.T) {
	dir := t.TempDir()
	later := floorTestTime.Add(time.Hour)

	if wrote, err := advanceVerifiedFloor(dir, floorTestTime); err != nil || !wrote {
		t.Fatalf("first write = %v, %v; want a write", wrote, err)
	}
	if wrote, err := advanceVerifiedFloor(dir, later); err != nil || !wrote {
		t.Fatalf("later write = %v, %v; want a write", wrote, err)
	}
	if wrote, err := advanceVerifiedFloor(dir, floorTestTime); err != nil || wrote {
		t.Fatalf("earlier write = %v, %v; want it skipped", wrote, err)
	}
	// A burst of proofs must not turn into a flash write per second.
	if wrote, err := advanceVerifiedFloor(dir, later.Add(minFloorAdvance-time.Second)); err != nil || wrote {
		t.Fatalf("sub-minute write = %v, %v; want it skipped", wrote, err)
	}
	if got := verifiedFloorAt(t, dir); !got.Equal(later) {
		t.Errorf("verified floor = %v, want %v", got, later)
	}
	if wrote, err := advanceVerifiedFloor(dir, later.Add(minFloorAdvance)); err != nil || !wrote {
		t.Fatalf("one-minute write = %v, %v; want a write", wrote, err)
	}
}

// A relayed proof carries no nonce of ours, so an old one replayed on the LAN
// still verifies. It must not pull back the verified time the refresh builds on.
func TestApplyVerifiedIgnoresAnOlderProof(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBootClock{nanos: int64(time.Minute)}
	m := NewManager(nil, dir)
	m.apply = func(time.Time) {}
	m.bootTime = boot.now

	m.ApplyVerified(floorTestTime)
	m.ApplyVerified(floorTestTime.Add(-24 * time.Hour)) // a replayed proof
	boot.advance(floorRefreshInterval)
	m.persistVerifiedFloor(time.Time{})

	if want, got := floorTestTime.Add(floorRefreshInterval), verifiedFloorAt(t, dir); !got.Equal(want) {
		t.Errorf("verified floor = %v, want %v", got, want)
	}
}

// A relayed proof dated in the future (a server clock bug, or a leaked key)
// would otherwise park the floor ahead for good, since the floor never lowers on
// its own. A direct consensus answers this device's own nonce, so its upper
// bound caps the floor and its lower bound replaces the verified time.
func TestDirectConsensusLowersAFloorPushedAheadByARelayedProof(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBootClock{nanos: int64(time.Minute)}
	future := floorTestTime.AddDate(1, 0, 0)
	lower := floorTestTime.UnixNano() - boot.nanos
	h := newDirectHarness(1, directResult{consensus: Consensus{
		Confidence:       ConfidenceVerified,
		Quorum:           3,
		LowerOffsetNanos: lower,
		UpperOffsetNanos: lower + int64(10*time.Second),
	}})
	h.manager.configPath = dir
	h.manager.bootTime = boot.now

	h.manager.ApplyVerified(future)
	if got := verifiedFloorAt(t, dir); !got.Equal(future) {
		t.Fatalf("verified floor after the relayed proof = %v, want %v", got, future)
	}
	h.run(t)
	if got := verifiedFloorAt(t, dir); !got.Equal(floorTestTime) {
		t.Fatalf("verified floor after the direct consensus = %v, want it lowered to %v", got, floorTestTime)
	}

	// The refresh builds on the consensus, not on the relayed proof.
	boot.advance(floorRefreshInterval)
	h.manager.persistVerifiedFloor(time.Time{})
	if want, got := floorTestTime.Add(floorRefreshInterval), verifiedFloorAt(t, dir); !got.Equal(want) {
		t.Errorf("verified floor after a refresh = %v, want %v", got, want)
	}
}

// Between direct syncs a fast oscillator makes later results smaller. Each
// direct result replaces the verified time, so drift cannot pile up across
// syncs the way it would if the largest offset were kept.
func TestDirectConsensusReplacesTheVerifiedTime(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBootClock{nanos: int64(time.Minute)}
	m := NewManager(nil, dir)
	m.bootTime = boot.now
	base := floorTestTime.UnixNano() - boot.nanos

	m.noteDirectConsensus(base+int64(2*time.Minute), base+int64(2*time.Minute+10*time.Second))
	m.noteDirectConsensus(base, base+int64(10*time.Second))
	m.mu.RLock()
	got := m.verifiedOffset
	m.mu.RUnlock()
	if got != base {
		t.Errorf("verified offset = %d, want the latest direct lower bound %d", got, base)
	}
}

// A damaged verified floor heals on the next sync instead of blocking it.
func TestAdvanceVerifiedFloorReplacesUnusableValue(t *testing.T) {
	cases := map[string][]byte{
		"all zero":   {0, 0, 0, 0, 0, 0, 0, 0},
		"short":      {0, 0, 0},
		"far future": FloorBytes(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFloorFile(t, filepath.Join(dir, verifiedFloorFile), payload)
			if wrote, err := advanceVerifiedFloor(dir, floorTestTime); err != nil || !wrote {
				t.Fatalf("advanceVerifiedFloor = %v, %v; want the damaged value replaced", wrote, err)
			}
			if got := verifiedFloorAt(t, dir); !got.Equal(floorTestTime) {
				t.Errorf("verified floor = %v, want %v", got, floorTestTime)
			}
		})
	}
}

func TestAdvanceVerifiedFloorRefusesImplausibleTime(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []time.Time{{}, time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if wrote, err := advanceVerifiedFloor(dir, bad); err == nil || wrote {
			t.Errorf("advanceVerifiedFloor(%v) = %v, %v; want a refusal", bad, wrote, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, verifiedFloorFile)); !os.IsNotExist(err) {
		t.Errorf("a refused time must not create the file (stat err = %v)", err)
	}
}

// On a fresh image the agent config directory may not exist yet.
func TestAdvanceVerifiedFloorCreatesPrivateConfigDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "etc", "wendy-agent")
	if _, err := advanceVerifiedFloor(dir, floorTestTime); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 0700", perm)
	}
	info, err = os.Stat(filepath.Join(dir, verifiedFloorFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("verified floor mode = %o, want 0600", perm)
	}
}

// configpartition rewrites clock_floor with the flash date on every boot, so
// the floor applied at boot must be the later of that and the verified floor
// (WDY-3204).
func TestPersistedFloorPrefersTheLaterFloor(t *testing.T) {
	flash := time.Date(2026, 8, 27, 16, 32, 0, 0, time.UTC)
	synced := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name              string
		install, verified []byte
		want              time.Time
	}{
		{"verified is later", FloorBytes(flash), FloorBytes(synced), synced},
		{"install is later", FloorBytes(synced), FloorBytes(flash), synced},
		{"only install", FloorBytes(flash), nil, flash},
		{"only verified", nil, FloorBytes(synced), synced},
		{"verified implausible", FloorBytes(flash), make([]byte, 8), flash},
		{"neither", nil, nil, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.install != nil {
				writeFloorFile(t, filepath.Join(dir, clockFloorFile), tc.install)
			}
			if tc.verified != nil {
				writeFloorFile(t, filepath.Join(dir, verifiedFloorFile), tc.verified)
			}
			if got := NewManager(nil, dir).persistedFloor(); !got.Equal(tc.want) {
				t.Errorf("persistedFloor = %v, want %v", got, tc.want)
			}
		})
	}
}

// A direct sync persists the lower edge of the agreed interval, so the floor a
// later boot applies is never ahead of real time.
func TestRunDirectPersistsConsensusLowerBound(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBootClock{nanos: int64(42 * time.Second)}
	lower := floorTestTime.UnixNano() - boot.nanos
	h := newDirectHarness(1, directResult{consensus: Consensus{
		Confidence:       ConfidenceVerified,
		Quorum:           3,
		LowerOffsetNanos: lower,
		UpperOffsetNanos: lower + int64(10*time.Second),
	}})
	h.manager.configPath = dir
	h.manager.bootTime = boot.now
	h.run(t)

	if len(h.applied) != 1 {
		t.Fatalf("applied %d time(s), want 1", len(h.applied))
	}
	if want := floorTestTime.Add(5 * time.Second); !h.applied[0].Equal(want) {
		t.Errorf("applied %v, want the interval midpoint %v", h.applied[0], want)
	}
	if got := verifiedFloorAt(t, dir); !got.Equal(floorTestTime) {
		t.Errorf("verified floor = %v, want the interval's lower bound %v", got, floorTestTime)
	}
}

// Persisting is best effort. A floor that cannot be written must not stop a
// verified time from setting the clock, or the device stays wrong.
func TestRunDirectAppliesWhenTheFloorCannotBeSaved(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "config")
	writeFloorFile(t, notADir, []byte("a file where the config directory should be"))
	boot := &fakeBootClock{nanos: int64(time.Second)}
	lower := floorTestTime.UnixNano() - boot.nanos
	h := newDirectHarness(1, directResult{consensus: Consensus{
		Confidence:       ConfidenceVerified,
		Quorum:           3,
		LowerOffsetNanos: lower,
		UpperOffsetNanos: lower,
	}})
	h.manager.configPath = notADir
	h.manager.bootTime = boot.now
	h.run(t)

	if len(h.applied) != 1 {
		t.Fatalf("applied %d time(s), want the clock set even though the floor write failed", len(h.applied))
	}
	if len(h.waits) != 1 || h.waits[0] != resyncInterval {
		t.Errorf("waits = %v, want the normal re-sync interval after a successful sync", h.waits)
	}
}

// A relayed proof (multicast or the SyncClock RPC) is the only fix for a device
// with no internet, so it must persist too. Otherwise an air-gapped device loses
// the time again on its next reboot.
func TestApplyVerifiedSetsTheClockThenPersistsIt(t *testing.T) {
	dir := t.TempDir()
	var applied []time.Time
	m := NewManager(nil, dir)
	m.apply = func(t time.Time) { applied = append(applied, t) }
	m.bootTime = (&fakeBootClock{nanos: int64(time.Minute)}).now

	m.ApplyVerified(floorTestTime)

	if len(applied) != 1 || !applied[0].Equal(floorTestTime) {
		t.Fatalf("applied = %v, want [%v]", applied, floorTestTime)
	}
	if got := verifiedFloorAt(t, dir); !got.Equal(floorTestTime) {
		t.Errorf("verified floor = %v, want %v", got, floorTestTime)
	}
}

// Between syncs the floor follows the boot clock forward from the last verified
// time, so a reboot after a day offline comes back a day later, not at the last
// sync.
func TestRunFloorRefreshFollowsTheBootClock(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBootClock{nanos: int64(time.Minute)}
	m := NewManager(nil, dir)
	m.apply = func(time.Time) {}
	m.bootTime = boot.now
	m.ApplyVerified(floorTestTime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	m.sleep = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		if len(waits) > 1 {
			cancel()
			return make(chan time.Time) // the cancelled context wins the select
		}
		boot.advance(floorRefreshInterval)
		fired := make(chan time.Time, 1)
		fired <- time.Time{}
		return fired
	}
	m.RunFloorRefresh(ctx)

	if len(waits) == 0 || waits[0] != floorRefreshInterval {
		t.Errorf("waits = %v, want the refresh interval %v", waits, floorRefreshInterval)
	}
	if want, got := floorTestTime.Add(floorRefreshInterval), verifiedFloorAt(t, dir); !got.Equal(want) {
		t.Errorf("verified floor = %v, want %v", got, want)
	}
}

// With no verified time this boot there is nothing trustworthy to persist: the
// wall clock may be the stale one this floor exists to correct.
func TestRunFloorRefreshWritesNothingBeforeAVerifiedTime(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(nil, dir)
	m.bootTime = (&fakeBootClock{nanos: int64(time.Hour)}).now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	m.sleep = func(time.Duration) <-chan time.Time {
		calls++
		if calls > 1 {
			cancel()
			return make(chan time.Time)
		}
		fired := make(chan time.Time, 1)
		fired <- time.Time{}
		return fired
	}
	m.RunFloorRefresh(ctx)

	if _, err := os.Stat(filepath.Join(dir, verifiedFloorFile)); !os.IsNotExist(err) {
		t.Errorf("verified floor written without a verified time (stat err = %v)", err)
	}
}

// The device certificate's NotBefore is a lower bound the cloud vouched for, so
// boot moves the clock up to it. A missing or implausible date is ignored.
func TestApplyCertificateFloor(t *testing.T) {
	var applied []time.Time
	m := NewManager(nil, t.TempDir())
	m.apply = func(t time.Time) { applied = append(applied, t) }

	m.ApplyCertificateFloor(time.Time{})
	m.ApplyCertificateFloor(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(applied) != 0 {
		t.Fatalf("applied %v for a missing or implausible NotBefore", applied)
	}

	m.ApplyCertificateFloor(floorTestTime)
	if len(applied) != 1 || !applied[0].Equal(floorTestTime) {
		t.Errorf("applied = %v, want [%v]", applied, floorTestTime)
	}
}
