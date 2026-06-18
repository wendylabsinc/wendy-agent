//go:build darwin || linux

package rcm

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestBuildDLMiniloaderT264 verifies that BuildDLMiniloader produces a valid
// RCM40 message for a T264-sized payload: correct opcode, version, payload
// placement, and a non-zero CMAC.
func TestBuildDLMiniloaderT264(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 512)
	msg, err := BuildDLMiniloader(payload, [48]byte{})
	if err != nil {
		t.Fatalf("BuildDLMiniloader() error = %v", err)
	}

	// Total length: max(1024, 644+512) = 1156 → pad to 16-byte boundary = 1168.
	wantLen := 1168
	if len(msg) != wantLen {
		t.Errorf("len(msg) = %d, want %d", len(msg), wantLen)
	}

	// Opcode must be CmdDLMiniloader (0x4).
	opcode := binary.LittleEndian.Uint32(msg[msgOffOpcode:])
	if opcode != CmdDLMiniloader {
		t.Errorf("opcode = %#x, want %#x (CmdDLMiniloader)", opcode, CmdDLMiniloader)
	}

	// RCM version must be Version40 (0x00400001).
	ver := binary.LittleEndian.Uint32(msg[msgOffRCMVersion:])
	if ver != VersionT234 {
		t.Errorf("rcm_version = %#x, want %#x (Version40/VersionT234)", ver, VersionT234)
	}

	// Payload must start at offset 0x284 (msgHeaderSize).
	if !bytes.Equal(msg[msgHeaderSize:msgHeaderSize+512], payload) {
		t.Error("payload bytes at offset 0x284 do not match input")
	}

	// len_insecure must equal total message length.
	lenInsecure := binary.LittleEndian.Uint32(msg[0:])
	if lenInsecure != uint32(wantLen) {
		t.Errorf("len_insecure = %d, want %d", lenInsecure, wantLen)
	}

	// payload_len must equal the payload size.
	payloadLen := binary.LittleEndian.Uint32(msg[msgOffPayloadLen:])
	if payloadLen != 512 {
		t.Errorf("payload_len = %d, want 512", payloadLen)
	}

	// CMAC at offset 0x104 must be non-zero (computed over a non-trivial message).
	cmac := msg[msgOffObjectSig : msgOffObjectSig+16]
	if bytes.Equal(cmac, make([]byte, 16)) {
		t.Error("CMAC at 0x104 is all-zero; expected a computed value")
	}
}
