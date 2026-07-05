package services

import (
	"errors"
	"testing"

	"go.uber.org/zap"
)

// newTestArmer returns an armer with all seams stubbed to safe no-ops; each test
// overrides only what it needs.
func newTestArmer() *redundancyArmer {
	return &redundancyArmer{
		logger:      zap.NewNop(),
		isJetson:    func() bool { return true },
		readEfivar:  func(string) ([]byte, error) { return nil, errors.New("missing") },
		statPath:    func(string) error { return nil },
		writeEfivar: func(string, []byte) error { return nil },
		writeMarker: func(string) error { return nil },
		reboot:      func() error { return nil },
	}
}

func TestDecideNotJetson(t *testing.T) {
	a := newTestArmer()
	a.isJetson = func() bool { return false }
	if got := a.decide(); got != armNotNeeded {
		t.Fatalf("decide() = %v, want armNotNeeded", got)
	}
}

func TestDecideAlreadyArmed(t *testing.T) {
	a := newTestArmer()
	a.readEfivar = func(string) ([]byte, error) {
		return []byte{0x07, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}, nil
	}
	if got := a.decide(); got != armNotNeeded {
		t.Fatalf("decide() = %v, want armNotNeeded", got)
	}
}

func TestDecideNoAppBSlot(t *testing.T) {
	a := newTestArmer()
	a.statPath = func(p string) error {
		if p == appBPartition {
			return errors.New("no such device")
		}
		return nil
	}
	if got := a.decide(); got != armImpossibleNoSlot {
		t.Fatalf("decide() = %v, want armImpossibleNoSlot", got)
	}
}

func TestDecideMarkerAlreadySet(t *testing.T) {
	a := newTestArmer()
	// APP_b exists, marker exists, still unarmed -> arming failed before.
	if got := a.decide(); got != armFailedPreviously {
		t.Fatalf("decide() = %v, want armFailedPreviously", got)
	}
}

func TestDecideArmable(t *testing.T) {
	a := newTestArmer()
	a.statPath = func(p string) error {
		if p == armAttemptMarker {
			return errors.New("no marker yet")
		}
		return nil // APP_b present
	}
	if got := a.decide(); got != armPossible {
		t.Fatalf("decide() = %v, want armPossible", got)
	}
}

func TestArmWritesEfivarAndReboots(t *testing.T) {
	a := newTestArmer()
	writes := map[string][]byte{}
	markerWritten := false
	rebooted := false
	a.writeMarker = func(string) error { markerWritten = true; return nil }
	a.writeEfivar = func(p string, d []byte) error { writes[p] = d; return nil }
	a.reboot = func() error { rebooted = true; return nil }

	if err := a.arm(); err != nil {
		t.Fatalf("arm() error = %v", err)
	}
	if !markerWritten {
		t.Fatal("arm() did not write the attempt marker")
	}
	if got := writes[rootfsRedundancyEfivar]; string(got) != string([]byte{0x07, 0, 0, 0, 0x01, 0, 0, 0}) {
		t.Fatalf("RootfsRedundancyLevel bytes = % x, want 07 00 00 00 01 00 00 00", got)
	}
	if got := writes[rootfsRetryCountMaxEfivar]; string(got) != string([]byte{0x07, 0, 0, 0, 0x03, 0, 0, 0}) {
		t.Fatalf("RootfsRetryCountMax bytes = % x, want 07 00 00 00 03 00 00 00", got)
	}
	if !rebooted {
		t.Fatal("arm() did not reboot")
	}
}
