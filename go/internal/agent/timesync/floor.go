package timesync

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"time"
)

const clockFloorFile = "clock_floor"

// A floor outside this window is treated as absent. It catches a torn or zeroed
// write, not a wrong-but-plausible one: the CLI writes this file from the
// flashing host's own clock (cli/commands/os_provision.go), so a host that is
// merely months or years out still produces a value inside the window. Since
// AdvanceTo never moves the clock backward, a future floor parks the device there
// until it is reflashed, and only the grossly-wrong cases are caught here.
var (
	floorMin = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	floorMax = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// readFloor reads the Unix-seconds timestamp from configPath/clock_floor. floor
// is the value to apply and is zero when there is nothing to apply — an absent or
// short file, the normal pre-feature case, or a value outside the plausible
// window. Such a value comes back as refused instead, which is non-zero only in
// that case, so it can be logged (an all-zero write, a 1970 host clock and a
// year-2200 value need different remedies) without any path applying it.
func readFloor(configPath string) (floor, refused time.Time) {
	data, err := os.ReadFile(filepath.Join(configPath, clockFloorFile))
	if err != nil || len(data) < 8 {
		return time.Time{}, time.Time{}
	}
	sec := int64(binary.BigEndian.Uint64(data[:8])) //nolint:gosec — range-checked below
	t := time.Unix(sec, 0)
	if t.Before(floorMin) || t.After(floorMax) {
		return time.Time{}, t
	}
	return t, time.Time{}
}

// FloorBytes encodes t as the 8-byte big-endian clock_floor payload.
func FloorBytes(t time.Time) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.Unix())) //nolint:gosec — install-time timestamps are non-negative
	return buf[:]
}

// WriteFloor writes t as a big-endian int64 Unix timestamp to
// configPath/clock_floor. Called by the CLI at install time and during
// config-partition provisioning.
func WriteFloor(configPath string, t time.Time) error {
	return os.WriteFile(filepath.Join(configPath, clockFloorFile), FloorBytes(t), 0o644)
}
