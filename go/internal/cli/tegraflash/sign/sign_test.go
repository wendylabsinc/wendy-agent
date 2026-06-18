package sign

import (
	"bytes"
	"strings"
	"testing"
)

// TestZeroCMAC verifies AES-128-CMAC with the all-zero key over the empty input.
//
// NOTE: The task brief cited bb1d6929e95937287fa37d129b756746 as the expected
// value, attributing it to "the published AES-128-CMAC of the empty message
// under the all-zero key." This is incorrect. bb1d6929... is Appendix D
// Example 1 from RFC 4493, but it uses the RFC example key
// 2b7e151628aed2a6abf7158809cf4f3c, NOT the all-zero key. The correct
// AES-128-CMAC of the empty message with the all-zero key (computed per
// RFC 4493 with K = 00...00) is 4387c14b46ef7e176dceefa862d72ff9, derived as:
//
//	L  = AES_0(0^128) = 66e94bd4ef8a2c3b884cfa59ca342b2e
//	K1 = cdd297a9df1458771099f4b39468565c  (shift-left L, MSB=0 so no XOR)
//	K2 = 9ba52f53be28b0ee2133e96728d0ac3f  (shift-left K1, MSB=1 so XOR last byte 0x87)
//	M1 = 80000000000000000000000000000000  (0x80 pad for empty block)
//	Y  = 0^128 XOR M1 XOR K2 = 1ba52f53be28b0ee2133e96728d0ac3f
//	T  = AES_0(Y) = 4387c14b46ef7e176dceefa862d72ff9
//
// The tegrasign zero-CMAC is not verified by the Boot ROM (it is cosmetic in
// ODM-open mode), so the exact value does not affect correctness.
func TestZeroCMAC(t *testing.T) {
	got := ZeroCMAC(nil)
	// Correct AES-128-CMAC of empty message with all-zero key per RFC 4493.
	// (Not bb1d6929..., which is the RFC 4493 Appendix D Example 1 vector with
	// the non-zero example key 2b7e151628aed2a6abf7158809cf4f3c.)
	want := [16]byte{0x43, 0x87, 0xc1, 0x4b, 0x46, 0xef, 0x7e, 0x17, 0x6d, 0xce, 0xef, 0xa8, 0x62, 0xd7, 0x2f, 0xf9}
	if got != want {
		t.Errorf("ZeroCMAC(nil) = %x, want %x", got, want)
	}
}

// TestZeroCMACRFC4493 verifies the AES-CMAC implementation against RFC 4493
// Appendix D Example 1: empty message, key = 2b7e151628aed2a6abf7158809cf4f3c,
// expected output = bb1d6929e95937287fa37d129b756746. This confirms the algorithm
// is correct independent of the key; ZeroCMAC uses the all-zero key instead.
func TestZeroCMACRFC4493(t *testing.T) {
	rfcKey := []byte{
		0x2b, 0x7e, 0x15, 0x16, 0x28, 0xae, 0xd2, 0xa6,
		0xab, 0xf7, 0x15, 0x88, 0x09, 0xcf, 0x4f, 0x3c,
	}
	got := aesCMAC128(rfcKey, nil) // empty message
	want := [16]byte{0xbb, 0x1d, 0x69, 0x29, 0xe9, 0x59, 0x37, 0x28, 0x7f, 0xa3, 0x7d, 0x12, 0x9b, 0x75, 0x67, 0x46}
	if got != want {
		t.Errorf("RFC4493 CMAC(empty, 2b7e...) = %x, want %x", got, want)
	}
}

// TestZeroCMACTruncation verifies that ZeroCMAC truncates input to a multiple
// of 16 bytes before processing, replicating tegrasign_v3's NumAesBlocks
// truncation (int(length/16) * 16).
func TestZeroCMACTruncation(t *testing.T) {
	// 17 bytes: the 17th byte must not affect the result — it is truncated to 16.
	data16 := make([]byte, 16)
	data17 := make([]byte, 17)
	data17[16] = 0xff // trailing byte that must be ignored

	got16 := ZeroCMAC(data16)
	got17 := ZeroCMAC(data17)
	if got16 != got17 {
		t.Errorf("ZeroCMAC should truncate 17 bytes to 16: got16=%x got17=%x", got16, got17)
	}
}

// TestSHA512 checks the SHA-512 of the empty input against the well-known value.
func TestSHA512(t *testing.T) {
	got := SHA512(nil)
	// SHA-512 of empty input per FIPS 180-4.
	wantHex := "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
	gotHex := SHA512Hex(nil)
	if gotHex != wantHex {
		t.Errorf("SHA512Hex(nil) = %s, want %s", gotHex, wantHex)
	}
	// The full known value (0xcf83e135... 64 bytes).
	knownEmpty := [64]byte{
		0xcf, 0x83, 0xe1, 0x35, 0x7e, 0xef, 0xb8, 0xbd,
		0xf1, 0x54, 0x28, 0x50, 0xd6, 0x6d, 0x80, 0x07,
		0xd6, 0x20, 0xe4, 0x05, 0x0b, 0x57, 0x15, 0xdc,
		0x83, 0xf4, 0xa9, 0x21, 0xd3, 0x6c, 0xe9, 0xce,
		0x47, 0xd0, 0xd1, 0x3c, 0x5d, 0x85, 0xf2, 0xb0,
		0xff, 0x83, 0x18, 0xd2, 0x87, 0x7e, 0xec, 0x2f,
		0x63, 0xb9, 0x31, 0xbd, 0x47, 0x41, 0x7a, 0x81,
		0xa5, 0x38, 0x32, 0x7a, 0xf9, 0x27, 0xda, 0x3e,
	}
	if got != knownEmpty {
		t.Errorf("SHA512(nil) = %x, want %x", got, knownEmpty)
	}
}

// TestWriteSignedManifest verifies that WriteSignedManifest emits the required
// XML attributes for a BR BCT entry (offset 0x1600, length 0xa00).
func TestWriteSignedManifest(t *testing.T) {
	var b bytes.Buffer
	err := WriteSignedManifest(&b, []SignEntry{
		{
			Name:     "br_bct_BR.bct",
			Offset:   0x1600, // 5632
			Length:   0xa00,  // 2560
			HashFile: "br_bct_BR.bct.hash",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := b.String()
	for _, want := range []string{
		`mode="sbk"`,
		`name="br_bct_BR.bct"`,
		`offset="5632"`,
		`length="2560"`,
		`digest_type="sha512"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q\n%s", want, s)
		}
	}
}
