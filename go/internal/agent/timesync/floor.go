package timesync

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/atomicfile"
)

const clockFloorFile = "clock_floor"

// verifiedFloorFile holds the latest verified time this agent has seen, so a
// device that reboots without network starts no earlier than that. It is
// separate from clockFloorFile on purpose: configpartition copies the
// install-time floor over clockFloorFile on every boot, which would reset a
// floor the agent advanced in the same file back to the flash date (WDY-3204).
const verifiedFloorFile = "clock_floor_verified"

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
	return readFloorFile(filepath.Join(configPath, clockFloorFile))
}

// readVerifiedFloor reads configPath/clock_floor_verified with the same rules
// as readFloor.
func readVerifiedFloor(configPath string) (floor, refused time.Time) {
	return readFloorFile(filepath.Join(configPath, verifiedFloorFile))
}

func readFloorFile(path string) (floor, refused time.Time) {
	data, err := os.ReadFile(path)
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

// minFloorAdvance is the smallest step advanceVerifiedFloor writes. The floor
// only has to keep a rebooted device roughly current, and the step bounds flash
// writes to one a minute however many time proofs arrive.
const minFloorAdvance = time.Minute

// advanceVerifiedFloor raises configPath/clock_floor_verified to t and never
// lowers it, reporting whether it wrote. It skips a step smaller than
// minFloorAdvance. A missing, short or implausible existing value counts as no
// floor and is replaced, so a damaged file heals on the next sync. The write is
// atomic and durable, so a power cut mid-write cannot leave a torn value that
// the next boot would discard. configPath is created (0700) if needed: on a
// fresh image the agent config directory may not exist yet. Callers serialize
// writes to one configPath (Manager.floorMu).
func advanceVerifiedFloor(configPath string, t time.Time) (bool, error) {
	if t.Before(floorMin) || t.After(floorMax) {
		return false, fmt.Errorf("timesync: refusing implausible verified clock floor %s", t.UTC().Format(time.RFC3339))
	}
	if cur, _ := readVerifiedFloor(configPath); !cur.IsZero() && t.Before(cur.Add(minFloorAdvance)) {
		return false, nil
	}
	if err := writeVerifiedFloor(configPath, t); err != nil {
		return false, err
	}
	return true, nil
}

// writeVerifiedFloor writes t to configPath/clock_floor_verified whatever it
// held before. Only a direct Roughtime consensus may lower the floor this way;
// every other writer goes through advanceVerifiedFloor.
func writeVerifiedFloor(configPath string, t time.Time) error {
	if err := os.MkdirAll(configPath, 0o700); err != nil {
		return fmt.Errorf("timesync: creating config dir for verified clock floor: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(configPath, verifiedFloorFile), FloorBytes(t), 0o600); err != nil {
		return fmt.Errorf("timesync: writing verified clock floor: %w", err)
	}
	return nil
}
