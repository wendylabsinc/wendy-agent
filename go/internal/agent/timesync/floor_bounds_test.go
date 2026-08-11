package timesync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// /config/clock_floor is eight bytes on a FAT partition that anyone flashing a
// card can write. Since AdvanceTo only moves the clock forward, a value in the
// future parks the device there and every certificate reads as expired until it
// is reflashed.
func TestReadFloor_RejectsImplausibleValues(t *testing.T) {
	cases := map[string][]byte{
		"all zero":    {0, 0, 0, 0, 0, 0, 0, 0},
		"all ones":    {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"far future":  FloorBytes(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)),
		"before 2024": FloorBytes(time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, clockFloorFile), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			got, rejected := readFloor(dir)
			if !got.IsZero() {
				t.Errorf("readFloor = %v, want zero time", got)
			}
			if !rejected {
				t.Error("an implausible value must report rejected so it can be logged")
			}
		})
	}
}

// A short or absent file is the normal pre-feature case, not an anomaly, and must
// not be reported as a rejected value.
func TestReadFloor_AbsentOrShortIsNotRejected(t *testing.T) {
	dir := t.TempDir()
	if got, rejected := readFloor(dir); !got.IsZero() || rejected {
		t.Errorf("absent file: got %v rejected=%v, want zero/false", got, rejected)
	}
	if err := os.WriteFile(filepath.Join(dir, clockFloorFile), []byte{0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, rejected := readFloor(dir); !got.IsZero() || rejected {
		t.Errorf("short file: got %v rejected=%v, want zero/false", got, rejected)
	}
}

func TestReadFloor_AcceptsPlausibleValue(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2026, 8, 7, 0, 33, 2, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, clockFloorFile), FloorBytes(want), 0o600); err != nil {
		t.Fatal(err)
	}
	got, rejected := readFloor(dir)
	if !got.Equal(want) || rejected {
		t.Errorf("readFloor = %v rejected=%v, want %v/false", got, rejected, want)
	}
}
