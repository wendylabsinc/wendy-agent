package timesync

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"time"
)

const clockFloorFile = "clock_floor"

// readFloor reads the Unix-seconds timestamp from configPath/clock_floor.
// Returns zero time if the file doesn't exist or cannot be read.
func readFloor(configPath string) time.Time {
	data, err := os.ReadFile(filepath.Join(configPath, clockFloorFile))
	if err != nil || len(data) < 8 {
		return time.Time{}
	}
	sec := int64(binary.BigEndian.Uint64(data[:8]))
	return time.Unix(sec, 0)
}

// WriteFloor writes t as a big-endian int64 Unix timestamp to
// configPath/clock_floor. Called by the CLI at install time and during
// config-partition provisioning.
func WriteFloor(configPath string, t time.Time) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.Unix())) //nolint:gosec — Unix() is always non-negative for install-time timestamps
	return os.WriteFile(filepath.Join(configPath, clockFloorFile), buf[:], 0o644)
}
