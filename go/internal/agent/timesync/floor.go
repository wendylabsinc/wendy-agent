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
