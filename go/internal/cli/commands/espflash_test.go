package commands

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

	_, err := connectAttempt("/dev/fake", &serial.Mode{BaudRate: initialBaudRate})
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
