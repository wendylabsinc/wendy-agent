package sigverify

import (
	"crypto/sha256"
	"errors"
	"testing"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	pubPEM, privPEM, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	signer, err := NewSignerFromPEM(privPEM)
	if err != nil {
		t.Fatalf("NewSignerFromPEM: %v", err)
	}
	verifier, err := NewVerifier(pubPEM)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if !verifier.Enabled() {
		t.Fatal("verifier built from a real key should be Enabled")
	}

	// The signed message is the artifact's sha256 digest — the same bytes the
	// agent's finalize() passes to Verify.
	digest := sha256.Sum256([]byte("fake .raw bytes"))
	sig, err := signer.Sign(digest[:])
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if err := verifier.Verify(digest[:], sig); err != nil {
		t.Fatalf("valid signature should verify: %v", err)
	}

	// A different digest (tampered artifact) must not verify.
	other := sha256.Sum256([]byte("different bytes"))
	if err := verifier.Verify(other[:], sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered digest: got %v, want ErrBadSignature", err)
	}

	// A missing signature against an enabled verifier is ErrUnsigned.
	if err := verifier.Verify(digest[:], nil); !errors.Is(err, ErrUnsigned) {
		t.Errorf("empty signature: got %v, want ErrUnsigned", err)
	}

	// A signature from a different key must not verify.
	_, otherPriv, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, _ := NewSignerFromPEM(otherPriv)
	badSig, _ := otherSigner.Sign(digest[:])
	if err := verifier.Verify(digest[:], badSig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong-key signature: got %v, want ErrBadSignature", err)
	}

	// PublicKeyPEM derived from the private key matches the generated public PEM.
	pkPEM, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}
	if string(pkPEM) != string(pubPEM) {
		t.Error("PublicKeyPEM should equal the public PEM from GenerateKeypair")
	}
}

func TestNewSignerFromPEM_Rejects(t *testing.T) {
	if _, err := NewSignerFromPEM(nil); err == nil {
		t.Error("empty PEM should error")
	}
	// A public-key PEM must not be accepted as a signing key.
	pubPEM, _, _ := GenerateKeypair()
	if _, err := NewSignerFromPEM(pubPEM); err == nil {
		t.Error("a public-key PEM should be rejected as a signing key")
	}
}
