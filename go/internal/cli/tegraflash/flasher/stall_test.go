package flasher

import (
	"testing"
	"time"
)

const testWindow = 10 * time.Minute

func TestStallDetector_ProgressAdvanceResets(t *testing.T) {
	t0 := time.Unix(0, 0)
	s := newStallDetector(testWindow, t0)

	if s.observe(t0.Add(9*time.Minute), 20, 0) {
		t.Fatal("stalled despite push bytes advancing")
	}
	if s.observe(t0.Add(18*time.Minute), 20, 0) {
		t.Fatal("stalled 9m after the last advance (window is 10m)")
	}
	if !s.observe(t0.Add(19*time.Minute+time.Second), 20, 0) {
		t.Fatal("no stall 10m+ after the last advance")
	}
}

func TestStallDetector_LogGrowthResets(t *testing.T) {
	t0 := time.Unix(0, 0)
	s := newStallDetector(testWindow, t0)

	// Push bytes frozen, but the log keeps growing → never stalls.
	for i := 1; i <= 5; i++ {
		if s.observe(t0.Add(time.Duration(i)*9*time.Minute), 100, int64(i)) {
			t.Fatalf("stalled at step %d despite log growth", i)
		}
	}
	// Freeze both → stalls one window later.
	last := t0.Add(5 * 9 * time.Minute)
	if s.observe(last.Add(testWindow-time.Second), 100, 5) {
		t.Fatal("stalled before the window elapsed")
	}
	if !s.observe(last.Add(testWindow), 100, 5) {
		t.Fatal("no stall once both signals were quiet for the window")
	}
}

func TestStallDetector_BothQuietStalls(t *testing.T) {
	t0 := time.Unix(0, 0)
	s := newStallDetector(testWindow, t0)

	if s.observe(t0.Add(testWindow-time.Second), 0, 0) {
		t.Fatal("stalled just before the window boundary")
	}
	if !s.observe(t0.Add(testWindow), 0, 0) {
		t.Fatal("no stall at the window boundary")
	}
}

func TestStallDetector_CounterResetIsProgress(t *testing.T) {
	t0 := time.Unix(0, 0)
	s := newStallDetector(testWindow, t0)

	if s.observe(t0.Add(time.Minute), 100, 0) {
		t.Fatal("stalled on first advance")
	}
	// A shrinking counter means a new push started — that is progress too.
	if s.observe(t0.Add(11*time.Minute), 40, 0) {
		t.Fatal("counter reset treated as a stall")
	}
	if s.observe(t0.Add(20*time.Minute), 40, 0) {
		t.Fatal("stalled 9m after the counter reset")
	}
}

func TestStallDetector_UnknownLogSizeStable(t *testing.T) {
	t0 := time.Unix(0, 0)
	s := newStallDetector(testWindow, t0)

	// The -1 "couldn't stat the log" sentinel must not read as fake progress:
	// after it settles, only real signal changes reset the window.
	if s.observe(t0.Add(time.Minute), 0, -1) {
		t.Fatal("stalled on the first sentinel observation")
	}
	if s.observe(t0.Add(5*time.Minute), 10, -1) {
		t.Fatal("stalled despite push bytes advancing")
	}
	if s.observe(t0.Add(14*time.Minute), 10, -1) {
		t.Fatal("stalled 9m after the last advance")
	}
	if !s.observe(t0.Add(15*time.Minute+time.Second), 10, -1) {
		t.Fatal("no stall with a stable sentinel and frozen push bytes")
	}
}
