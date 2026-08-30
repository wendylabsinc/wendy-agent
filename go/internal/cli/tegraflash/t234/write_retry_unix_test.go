//go:build darwin || linux

package t234

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDeviceNotReady(t *testing.T) {
	if !deviceNotReady(&os.PathError{Op: "open", Err: syscall.ENXIO}) {
		t.Error("ENXIO should be treated as not-ready")
	}
	if !deviceNotReady(&os.PathError{Op: "read", Err: syscall.ENODEV}) {
		t.Error("ENODEV should be treated as not-ready")
	}
	if deviceNotReady(&os.PathError{Op: "open", Err: syscall.ENOENT}) {
		t.Error("ENOENT should not be treated as not-ready")
	}
	if deviceNotReady(nil) {
		t.Error("nil should not be treated as not-ready")
	}
}

// TestDumpDeviceRetriesOnNotReady proves dumpDevice re-opens the device while it
// reports the transient not-ready state and produces the full contents once the
// device is available (WDY-2621).
func TestDumpDeviceRetriesOnNotReady(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "device.bin")
	want := bytes.Repeat([]byte("wendy-flashpkg-"), 4096) // 60 KiB of known bytes
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	origOpen, origDelay := openDevice, dumpDeviceRetryDelay
	t.Cleanup(func() { openDevice, dumpDeviceRetryDelay = origOpen, origDelay })
	dumpDeviceRetryDelay = 0

	var calls int
	openDevice = func(name string) (*os.File, error) {
		calls++
		if calls < 3 { // fail the first two opens with ENXIO
			return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ENXIO}
		}
		return os.Open(name)
	}

	out := filepath.Join(dir, "dump.bin")
	if err := dumpDevice(WriterOptions{Device: src, DumpTo: out, DumpBytes: int64(len(want))}); err != nil {
		t.Fatalf("dumpDevice: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 open attempts (2 not-ready + 1 success), got %d", calls)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("dumped %d bytes, want %d and equal contents", len(got), len(want))
	}
}

// TestDumpDeviceGivesUpAfterRetries confirms a persistent not-ready device
// eventually returns the error instead of looping forever.
func TestDumpDeviceGivesUpAfterRetries(t *testing.T) {
	dir := t.TempDir()
	origOpen, origDelay := openDevice, dumpDeviceRetryDelay
	t.Cleanup(func() { openDevice, dumpDeviceRetryDelay = origOpen, origDelay })
	dumpDeviceRetryDelay = 0

	var calls int
	openDevice = func(name string) (*os.File, error) {
		calls++
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ENXIO}
	}

	err := dumpDevice(WriterOptions{Device: "/dev/whatever", DumpTo: filepath.Join(dir, "dump.bin"), DumpBytes: 16})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls != dumpDeviceRetries {
		t.Errorf("expected %d attempts, got %d", dumpDeviceRetries, calls)
	}
}
