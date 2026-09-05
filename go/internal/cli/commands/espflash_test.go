package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/seriallock"
	"go.bug.st/serial"
)

func TestIsPortBusy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"port busy error", &serial.PortError{}, true}, // zero value: Code() == PortBusy (first iota)
		{"wrapped port busy error", fmt.Errorf("open: %w", &serial.PortError{}), true},
		{"non-port error", errors.New("some other failure"), false},
		{"nil error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPortBusy(tt.err); got != tt.want {
				t.Errorf("isPortBusy(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrPortBusyMatchesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w: opening USB device /dev/fake", errPortBusy)
	if !errors.Is(wrapped, errPortBusy) {
		t.Error("errors.Is(wrapped, errPortBusy) = false, want true")
	}
	// Still matches once the caller has wrapped it further.
	if !errors.Is(fmt.Errorf("flashing failed: %w", wrapped), errPortBusy) {
		t.Error("errors.Is(doubly wrapped, errPortBusy) = false, want true")
	}
	// The *serial.PortError message is deliberately not repeated: errPortBusy
	// already carries "serial port busy".
	if strings.Count(wrapped.Error(), "serial port busy") != 1 {
		t.Errorf("wrapped.Error() = %q, want the busy phrase exactly once", wrapped.Error())
	}
}

// fakeSerialPort is a minimal serial.Port double. Read always fails
// immediately (rather than returning 0, nil) so callers relying on
// SetReadTimeout-based polling — like espFlasher.readByte — fail fast
// instead of waiting out a real timeout.
type fakeSerialPort struct {
	closed bool
}

func (p *fakeSerialPort) SetMode(mode *serial.Mode) error { return nil }
func (p *fakeSerialPort) Read(b []byte) (int, error)      { return 0, errors.New("fake port: no data") }
func (p *fakeSerialPort) Write(b []byte) (int, error)     { return len(b), nil }
func (p *fakeSerialPort) Drain() error                    { return nil }
func (p *fakeSerialPort) ResetInputBuffer() error         { return nil }
func (p *fakeSerialPort) ResetOutputBuffer() error        { return nil }
func (p *fakeSerialPort) SetDTR(dtr bool) error           { return nil }
func (p *fakeSerialPort) SetRTS(rts bool) error           { return nil }
func (p *fakeSerialPort) GetModemStatusBits() (*serial.ModemStatusBits, error) {
	return &serial.ModemStatusBits{}, nil
}
func (p *fakeSerialPort) SetReadTimeout(t time.Duration) error { return nil }
func (p *fakeSerialPort) Close() error                         { p.closed = true; return nil }
func (p *fakeSerialPort) Break(time.Duration) error            { return nil }

func TestOpenPortRetryingRetriesBusy(t *testing.T) {
	origOpen := serialOpenFn
	origInterval := portOpenRetryInterval
	defer func() {
		serialOpenFn = origOpen
		portOpenRetryInterval = origInterval
	}()
	portOpenRetryInterval = time.Millisecond

	// A device that's mid-reboot can make its USB node report busy
	// transiently, not just disappear — so busy has to be retried like any
	// other open failure rather than surfaced immediately.
	calls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		calls++
		if calls < 3 {
			return nil, &serial.PortError{} // zero value => PortBusy
		}
		return &fakeSerialPort{}, nil
	}

	port, err := openPortRetrying("/dev/fake", &serial.Mode{}, time.Second)
	if err != nil {
		t.Fatalf("openPortRetrying() error = %v, want success on the 3rd attempt", err)
	}
	if port == nil {
		t.Fatal("openPortRetrying() returned a nil port on success")
	}
	if calls != 3 {
		t.Errorf("serialOpenFn called %d times, want 3", calls)
	}
}

func TestOpenPortRetryingBusyGivesUpAfterBudget(t *testing.T) {
	origOpen := serialOpenFn
	origInterval := portOpenRetryInterval
	defer func() {
		serialOpenFn = origOpen
		portOpenRetryInterval = origInterval
	}()
	portOpenRetryInterval = time.Millisecond

	calls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		calls++
		return nil, &serial.PortError{} // zero value => PortBusy
	}

	_, err := openPortRetrying("/dev/fake", &serial.Mode{}, 10*time.Millisecond)
	if err == nil {
		t.Fatal("openPortRetrying() = nil error, want the busy error")
	}
	if !isPortBusy(err) {
		t.Errorf("openPortRetrying() error = %v, want a busy PortError", err)
	}
	if calls < 2 {
		t.Errorf("serialOpenFn called %d times, want at least 2 (busy should be retried until budget runs out)", calls)
	}
}

func TestOpenPortRetryingRetriesTransientFailure(t *testing.T) {
	origOpen := serialOpenFn
	origInterval := portOpenRetryInterval
	defer func() {
		serialOpenFn = origOpen
		portOpenRetryInterval = origInterval
	}()
	portOpenRetryInterval = time.Millisecond

	calls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("no such file or directory")
		}
		return &fakeSerialPort{}, nil
	}

	port, err := openPortRetrying("/dev/fake", &serial.Mode{}, time.Second)
	if err != nil {
		t.Fatalf("openPortRetrying() error = %v, want success on the 3rd attempt", err)
	}
	if port == nil {
		t.Fatal("openPortRetrying() returned a nil port on success")
	}
	if calls != 3 {
		t.Errorf("serialOpenFn called %d times, want 3", calls)
	}
}

func TestOpenPortRetryingGivesUpAfterBudget(t *testing.T) {
	origOpen := serialOpenFn
	origInterval := portOpenRetryInterval
	defer func() {
		serialOpenFn = origOpen
		portOpenRetryInterval = origInterval
	}()
	portOpenRetryInterval = time.Millisecond

	calls := 0
	wantErr := errors.New("no such file or directory")
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		calls++
		return nil, wantErr
	}

	_, err := openPortRetrying("/dev/fake", &serial.Mode{}, 10*time.Millisecond)
	if !errors.Is(err, wantErr) {
		t.Errorf("openPortRetrying() error = %v, want %v", err, wantErr)
	}
	if calls < 2 {
		t.Errorf("serialOpenFn called %d times, want at least 2 (budget should allow more than one attempt)", calls)
	}
}

func TestConnectAttemptGivesUpAfterAttempts(t *testing.T) {
	origOpen := serialOpenFn
	origRetries := connectAttemptRetries
	origBudget := portOpenRetryBudget
	origInterval := portOpenRetryInterval
	defer func() {
		serialOpenFn = origOpen
		connectAttemptRetries = origRetries
		portOpenRetryBudget = origBudget
		portOpenRetryInterval = origInterval
	}()

	connectAttemptRetries = 2
	portOpenRetryBudget = 10 * time.Millisecond
	portOpenRetryInterval = time.Millisecond

	openCalls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		openCalls++
		return &fakeSerialPort{}, nil
	}

	_, err := connectAttempt("/dev/fake", &serial.Mode{BaudRate: initialBaudRate}, discovery.SerialTransportNativeUSB)
	if err == nil {
		t.Fatal("connectAttempt() = nil error, want failure since the fake port never syncs")
	}
	// The port is closed and reopened after every reset pulse (the reset
	// genuinely disconnects the USB device on real hardware), so
	// serialOpenFn is called once for the initial open plus once per retry.
	wantCalls := 1 + connectAttemptRetries
	if openCalls != wantCalls {
		t.Errorf("serialOpenFn called %d times, want %d (1 initial open + %d reopens, one per reset pulse)", openCalls, wantCalls, connectAttemptRetries)
	}
	wantMsg := fmt.Sprintf("did not respond after %d bootloader-reset attempts", connectAttemptRetries)
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("connectAttempt() error = %q, want it to contain %q", err.Error(), wantMsg)
	}
}

func TestConnectAttemptUARTBridgeKeepsPortOpenAcrossRetries(t *testing.T) {
	origOpen := serialOpenFn
	origRetries := connectAttemptRetries
	defer func() {
		serialOpenFn = origOpen
		connectAttemptRetries = origRetries
	}()

	connectAttemptRetries = 2
	port := &fakeSerialPort{}
	openCalls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		openCalls++
		return port, nil
	}

	_, err := connectAttempt("/dev/fake", &serial.Mode{BaudRate: initialBaudRate}, discovery.SerialTransportUARTBridge)
	if err == nil {
		t.Fatal("connectAttempt() = nil error, want failure since the fake port never syncs")
	}
	if openCalls != 1 {
		t.Errorf("serialOpenFn called %d times, want 1 (UART bridge must remain open across ESP resets)", openCalls)
	}
}

func TestOpenLockedPortDetectsExistingLock(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "espflash-lock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	path := f.Name()
	f.Close()

	// Simulate another program (idf.py monitor, esptool, ...) already
	// holding the port.
	holder, err := seriallock.Acquire(path)
	if err != nil {
		t.Fatalf("seriallock.Acquire() error = %v", err)
	}
	defer holder.Release()

	_, err = openLockedPort(path, &serial.Mode{BaudRate: initialBaudRate})
	if err == nil {
		t.Fatal("openLockedPort() = nil error, want failure while another lock is held")
	}
	if !errors.Is(err, errPortBusy) {
		t.Errorf("openLockedPort() error = %v, want it to satisfy errors.Is(err, errPortBusy)", err)
	}
}

func TestOpenLockedPortMissingDeviceIsNotBusy(t *testing.T) {
	// A device that doesn't exist yet (mid re-enumeration during a reboot)
	// is a different failure mode from "someone else holds it" — it must
	// stay retryable rather than being misclassified as busy (which
	// openPortRetrying treats as terminal).
	path := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := openLockedPort(path, &serial.Mode{BaudRate: initialBaudRate})
	if err == nil {
		t.Fatal("openLockedPort() = nil error, want failure on a nonexistent path")
	}
	if errors.Is(err, errPortBusy) {
		t.Errorf("openLockedPort() error = %v, want it to NOT satisfy errors.Is(err, errPortBusy) for a missing device", err)
	}
}

func TestIsPermissionDeniedFromRawSyscallError(t *testing.T) {
	// openLockedPort's own pre-open (via seriallock.Acquire) can fail with a
	// raw syscall error rather than a *serial.PortError when it can't even
	// open the device to take the lock.
	err := fmt.Errorf("open serial: %w", syscall.EACCES)
	if !isPermissionDenied(err) {
		t.Errorf("isPermissionDenied(%v) = false, want true", err)
	}
}

func TestOpenLockedPortReleasesLockOnOpenFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "espflash-lock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	path := f.Name()
	f.Close()

	// path is a plain file, not a real serial device, so serial.Open fails
	// after the flock succeeds — openLockedPort must release that flock
	// rather than leak it.
	_, err = openLockedPort(path, &serial.Mode{BaudRate: initialBaudRate})
	if err == nil {
		t.Fatal("openLockedPort() = nil error, want serial.Open to fail on a plain file")
	}
	if errors.Is(err, errPortBusy) {
		t.Errorf("openLockedPort() error = %v, want a serial.Open failure, not a lock failure", err)
	}

	lock, err := seriallock.Acquire(path)
	if err != nil {
		t.Fatalf("seriallock.Acquire() error = %v, want success — openLockedPort should have released its own lock", err)
	}
	lock.Release()
}

func TestOpenPortRetryingDoesNotRetryLockFailure(t *testing.T) {
	origOpen := serialOpenFn
	defer func() { serialOpenFn = origOpen }()

	calls := 0
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		calls++
		return nil, fmt.Errorf("%w: serial port %s is in use by another program (idf.py monitor?)", errPortBusy, portPath)
	}

	_, err := openPortRetrying("/dev/fake", &serial.Mode{}, time.Second)
	if err == nil {
		t.Fatal("openPortRetrying() = nil error, want the lock failure")
	}
	if !errors.Is(err, errPortBusy) {
		t.Errorf("openPortRetrying() error = %v, want errPortBusy", err)
	}
	if calls != 1 {
		t.Errorf("serialOpenFn called %d times, want 1 (a lock failure is terminal, unlike raw kernel busy)", calls)
	}
}

func TestConnectAttemptPassesThroughLockFailure(t *testing.T) {
	origOpen := serialOpenFn
	defer func() { serialOpenFn = origOpen }()

	wantErr := fmt.Errorf("%w: serial port /dev/fake is in use by another program (idf.py monitor?)", errPortBusy)
	serialOpenFn = func(portPath string, mode *serial.Mode) (serial.Port, error) {
		return nil, wantErr
	}

	_, err := connectAttempt("/dev/fake", &serial.Mode{BaudRate: initialBaudRate}, discovery.SerialTransportNativeUSB)
	if !errors.Is(err, errPortBusy) {
		t.Errorf("connectAttempt() error = %v, want errPortBusy", err)
	}
	if err.Error() != wantErr.Error() {
		t.Errorf("connectAttempt() error = %q, want it passed through unchanged as %q", err.Error(), wantErr.Error())
	}
}
