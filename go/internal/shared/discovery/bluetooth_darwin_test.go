//go:build darwin

package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// TestScanBLEWithContextReturnsResult exercises the happy path: bleScanFn
// returns before ctx is cancelled, so scanBLEWithContext should return its
// result directly.
func TestScanBLEWithContextReturnsResult(t *testing.T) {
	orig := bleScanFn
	defer func() { bleScanFn = orig }()

	want := []models.BluetoothDevice{{ID: "abc", DisplayName: "Wendy Test"}}
	bleScanFn = func(scanSeconds int) ([]models.BluetoothDevice, error) {
		return want, nil
	}

	devices, err := scanBLEWithContext(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != "abc" {
		t.Errorf("expected %+v, got %+v", want, devices)
	}
}

// TestScanBLEWithContextCancelDoesNotWaitForScan is the regression test for
// the exit-hang bug: wendy_ble_scan (bleScanFn's real implementation) blocks
// for the full requested scan duration with no ctx awareness at all — it
// can't be interrupted mid-flight. scanBLEWithContext must still return
// promptly once ctx is cancelled, well before the (here, deliberately slow)
// scan itself finishes, so a caller like the picker's Bluetooth polling
// goroutine isn't blocked out past a Ctrl+C.
func TestScanBLEWithContextCancelDoesNotWaitForScan(t *testing.T) {
	orig := bleScanFn
	defer func() { bleScanFn = orig }()

	const scanDelay = 2 * time.Second
	scanReturned := make(chan struct{})
	bleScanFn = func(scanSeconds int) ([]models.BluetoothDevice, error) {
		time.Sleep(scanDelay)
		close(scanReturned)
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := scanBLEWithContext(ctx, 5)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed >= scanDelay {
		t.Errorf("scanBLEWithContext waited %v for cancellation, expected well under the %v scan delay", elapsed, scanDelay)
	}

	// The abandoned scan goroutine should still run to completion in the
	// background rather than being leaked forever.
	select {
	case <-scanReturned:
	case <-time.After(scanDelay + time.Second):
		t.Errorf("background scan goroutine never completed after being abandoned")
	}
}

// TestScanBLEWithContextAlreadyCancelled ensures a ctx that is cancelled
// before the scan even starts returns immediately rather than launching (or
// at least waiting on) the scan goroutine.
func TestScanBLEWithContextAlreadyCancelled(t *testing.T) {
	orig := bleScanFn
	defer func() { bleScanFn = orig }()

	bleScanFn = func(scanSeconds int) ([]models.BluetoothDevice, error) {
		time.Sleep(2 * time.Second)
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := scanBLEWithContext(ctx, 5)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("scanBLEWithContext took %v to return for an already-cancelled ctx", elapsed)
	}
}
