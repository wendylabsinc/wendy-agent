package commands

import (
	"errors"
	"fmt"
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
	wrapped := fmt.Errorf("%w: opening USB device /dev/fake: %w", ErrPortBusy, &serial.PortError{})
	if !errors.Is(wrapped, ErrPortBusy) {
		t.Error("errors.Is(wrapped, ErrPortBusy) = false, want true")
	}
}
