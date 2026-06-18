package sigheader_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"os"
	"testing"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/sigheader"
)

// TestAppendSigHeaderSkeleton verifies the structural fields of the BCH without
// checking digest values.
func TestAppendSigHeaderSkeleton(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 1000)
	out, err := sigheader.AppendSigHeader(payload, [4]byte{'M', 'B', '1', 'B'}, "mb1_bootloader")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0x2000+len(payload) {
		t.Fatalf("len=%d", len(out))
	}
	if string(out[0:4]) != "NVDA" {
		t.Errorf("magic=%x", out[0:4])
	}
	if binary.LittleEndian.Uint32(out[0x1AA0:]) != 1 {
		t.Error("header_version != 1")
	}
	// type id (default MB1B) and image length in stage1_components[0]
	if !bytes.Equal(out[0x1EE0:0x1EE4], []byte{'M', 'B', '1', 'B'}) {
		t.Error("type id")
	}
	if binary.LittleEndian.Uint32(out[0x1EE4:]) != uint32(len(payload)) {
		t.Error("image len")
	}
	if l, e := sigheader.LoadEntryFor("mb1_bootloader"); l != 0x40040000 || e != 0x40040000 {
		t.Errorf("load/entry = %x/%x", l, e)
	}
	if !bytes.Equal(out[0x2000:], payload) {
		t.Error("payload not at 0x2000")
	}
}

// TestSigHeaderDigests checks that all four SHA-512 digest fields are computed
// and placed correctly.
//
// Note on the image digest at 0x1F30: the golden differential test establishes
// that tegrahost_v2 hashes the payload body (all bytes except the last 64),
// not the full aligned payload. The signed-section and header digests use
// sha512(out[0xFC0:0x2000]) and sha512(out[0x44:0x2000]) respectively, which
// is the coverage specified in tegrahost_v2.md section 2.
func TestSigHeaderDigests(t *testing.T) {
	payload := bytes.Repeat([]byte{0xCD}, 4096)
	out, err := sigheader.AppendSigHeader(payload, [4]byte{'M', 'B', '1', 'B'}, "mb1_bootloader")
	if err != nil {
		t.Fatal(err)
	}

	// Image digest: sha512 of payload body (excluding the last 64 alignment bytes).
	img := sha512.Sum512(out[0x2000 : len(out)-64])
	if !bytes.Equal(out[0x1F30:0x1F70], img[:]) {
		t.Error("image digest @0x1F30")
	}

	// Signed-section digest: sha512 of buf[0xFC0:0x2000].
	sec := sha512.Sum512(out[0xFC0:0x2000])
	if !bytes.Equal(out[0x50:0x90], sec[:]) {
		t.Error("signed-section digest @0x50")
	}

	// Header digest: sha512 of buf[0x44:0x2000].
	hdr := sha512.Sum512(out[0x44:0x2000])
	if !bytes.Equal(out[0x04:0x44], hdr[:]) {
		t.Error("header digest @0x04")
	}
}

// firstDiff returns the index of the first byte that differs between a and b,
// or -1 if they are equal. If the slices have different lengths the length
// difference is noted via a separate check in the caller.
func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// TestAppendSigHeaderMatchesGolden performs a byte-exact differential test
// against the captured output of tegrahost_v2 --appendsigheader ... zerosbk.
//
// Golden files:
//   - ../testdata/golden/payload_aligned.bin       (4096-byte aligned input)
//   - ../testdata/golden/payload_aligned_sigheader.bin  (12288-byte expected output)
func TestAppendSigHeaderMatchesGolden(t *testing.T) {
	in, err := os.ReadFile("../testdata/golden/payload_aligned.bin")
	if err != nil {
		t.Skip("golden aligned input not present")
	}
	want, err := os.ReadFile("../testdata/golden/payload_aligned_sigheader.bin")
	if err != nil {
		t.Skip("golden sigheader not present")
	}

	// The golden was produced with '--magicid MB1B' and no image-type argument.
	// Inspection of the golden at 0x1EE8/0x1EEC confirms load=0x00000000 and
	// entry=0x00000000: tegrahost_v2 skips the type-name strncmp path entirely
	// when no image type is given, leaving load and entry zero. LoadEntryFor("")
	// is defined to return (0,0) to reproduce this behaviour.
	got, err := sigheader.AppendSigHeader(in, [4]byte{'M', 'B', '1', 'B'}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(want))
	}
	if !bytes.Equal(got, want) {
		idx := firstDiff(got, want)
		t.Fatalf("sigheader mismatch: first diff at offset 0x%04x (%d)", idx, idx)
	}
}

// TestAppendSigHeaderMatchesGoldenMultiSize confirms the header is byte-exact
// across multiple payload sizes. This locks in the non-obvious rule (verified
// against the real tool at 1008, 4096, and 5008 byte payloads) that the
// stage1_components[0] image digest at 0x1F30 covers payload[:len-64] while the
// length field (0x1EE4) and the component digest (0x1460) use the full length.
func TestAppendSigHeaderMatchesGoldenMultiSize(t *testing.T) {
	for _, base := range []string{"payload_1008_aligned", "payload_5008_aligned"} {
		in, err := os.ReadFile("../testdata/golden/" + base + ".bin")
		if err != nil {
			t.Skip("golden input not present: " + base)
		}
		want, err := os.ReadFile("../testdata/golden/" + base + "_sigheader.bin")
		if err != nil {
			t.Skip("golden sigheader not present: " + base)
		}
		got, err := sigheader.AppendSigHeader(in, [4]byte{'M', 'B', '1', 'B'}, "")
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if !bytes.Equal(got, want) {
			idx := firstDiff(got, want)
			t.Fatalf("%s: sigheader mismatch at offset 0x%04x (%d)", base, idx, idx)
		}
	}
}
