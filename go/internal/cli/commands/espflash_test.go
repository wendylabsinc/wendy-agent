package commands

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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
