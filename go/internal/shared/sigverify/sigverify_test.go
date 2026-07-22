package sigverify

import (
	"crypto/rand"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

func mustKeypair(t *testing.T) (pubPEM []byte, sign func(msg []byte) []byte) {
	t.Helper()
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pub.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM = marshalPinnedKeyPEM(raw) // helper in sigverify.go
	sign = func(msg []byte) []byte {
		sig := make([]byte, mldsa65.SignatureSize)
		if err := mldsa65.SignTo(priv, msg, nil, false, sig); err != nil {
			t.Fatal(err)
		}
		return sig
	}
	return
}

func TestVerifier_DisabledWhenNoKey(t *testing.T) {
	v, err := NewVerifier(nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Enabled() {
		t.Fatal("verifier should be disabled with no key")
	}
	if err := v.Verify([]byte("x"), nil); err != nil {
		t.Fatalf("disabled verify must skip (nil), got %v", err)
	}
}

func TestVerifier_AcceptsValidRejectsTamperedAndUnsigned(t *testing.T) {
	pubPEM, sign := mustKeypair(t)
	v, err := NewVerifier(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Enabled() {
		t.Fatal("verifier should be enabled with a key")
	}
	msg := []byte("the-agent-binary-bytes")
	sig := sign(msg)
	if err := v.Verify(msg, sig); err != nil {
		t.Fatalf("valid sig rejected: %v", err)
	}
	if err := v.Verify([]byte("tampered"), sig); err == nil {
		t.Fatal("tampered content accepted")
	}
	if err := v.Verify(msg, nil); err == nil {
		t.Fatal("missing signature accepted while enabled")
	}
}
