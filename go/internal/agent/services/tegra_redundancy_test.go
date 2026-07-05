package services

import (
	"errors"
	"testing"
)

func gateWith(isJetson bool, efivar []byte, efivarErr error) redundancyGate {
	return redundancyGate{
		isJetson:   func() bool { return isJetson },
		readEfivar: func(string) ([]byte, error) { return efivar, efivarErr },
	}
}

func TestBlocksUpdateNotJetson(t *testing.T) {
	if gateWith(false, nil, errors.New("missing")).blocksUpdate() {
		t.Fatal("non-Jetson must not block the update")
	}
}

func TestBlocksUpdateJetsonUnarmedMissingVar(t *testing.T) {
	if !gateWith(true, nil, errors.New("no such file")).blocksUpdate() {
		t.Fatal("unarmed Jetson (missing var) must block the update")
	}
}

func TestBlocksUpdateJetsonUnarmedZeroLevel(t *testing.T) {
	// var exists but level bytes are zero (firmware materializes it volatile=0).
	if !gateWith(true, []byte{0x06, 0, 0, 0, 0, 0, 0, 0}, nil).blocksUpdate() {
		t.Fatal("unarmed Jetson (zero level) must block the update")
	}
}

func TestBlocksUpdateJetsonArmed(t *testing.T) {
	if gateWith(true, []byte{0x07, 0, 0, 0, 0x01, 0, 0, 0}, nil).blocksUpdate() {
		t.Fatal("armed Jetson must not block the update")
	}
}

func TestBlocksUpdateJetsonShortPayload(t *testing.T) {
	// A truncated (<8 byte) payload is not "armed" — must block.
	if !gateWith(true, []byte{0x07, 0, 0, 0}, nil).blocksUpdate() {
		t.Fatal("unarmed Jetson (short payload) must block the update")
	}
}
