package services

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func cdrFrame(b []byte) []byte {
	h := make([]byte, 4)
	binary.LittleEndian.PutUint32(h, uint32(len(b)))
	return append(h, b...)
}

func TestReadCDRFrames(t *testing.T) {
	in := bytes.NewReader(append(cdrFrame([]byte{0x00, 0x01}), cdrFrame([]byte{0x02, 0x03, 0x04})...))
	var got [][]byte
	if err := readCDRFrames(in, func(cdr []byte) error {
		got = append(got, append([]byte(nil), cdr...))
		return nil
	}); err != nil {
		t.Fatalf("readCDRFrames: %v", err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], []byte{0, 1}) || !bytes.Equal(got[1], []byte{2, 3, 4}) {
		t.Fatalf("frames = %v", got)
	}
}

func TestReadCDRFrames_EmptyIsCleanEOF(t *testing.T) {
	if err := readCDRFrames(bytes.NewReader(nil), func([]byte) error { return nil }); err != nil {
		t.Fatalf("empty stream should be a clean EOF, got %v", err)
	}
}

func TestReadCDRFrames_ShortHeader(t *testing.T) {
	// Two bytes is a truncated 4-byte length prefix: must error, not panic.
	err := readCDRFrames(bytes.NewReader([]byte{0x02, 0x00}), func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "truncated frame header") {
		t.Fatalf("want truncated-header error, got %v", err)
	}
}

func TestReadCDRFrames_ShortPayload(t *testing.T) {
	// Header claims 5 bytes but only 2 follow.
	in := append(make([]byte, 0), 0x05, 0x00, 0x00, 0x00, 0xAA, 0xBB)
	err := readCDRFrames(bytes.NewReader(in), func([]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "truncated frame payload") {
		t.Fatalf("want truncated-payload error, got %v", err)
	}
}

func TestReadCDRFrames_EmitError(t *testing.T) {
	sentinel := io.ErrClosedPipe
	err := readCDRFrames(bytes.NewReader(cdrFrame([]byte{1})), func([]byte) error { return sentinel })
	if err != sentinel {
		t.Fatalf("emit error must propagate, got %v", err)
	}
}
