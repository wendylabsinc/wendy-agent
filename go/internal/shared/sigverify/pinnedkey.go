package sigverify

import _ "embed"

// pinnedKeyPEMEmbed is the build-embedded pinned ML-DSA65 public key. In
// this PR the embedded file (pinned_signing_key.pem) is an empty
// placeholder, so DefaultVerifier is disabled until a real key ships.
//
//go:embed pinned_signing_key.pem
var pinnedKeyPEMEmbed []byte

// DefaultVerifier is built from the build-embedded pinned key. In this PR
// the embedded file is an empty placeholder, so DefaultVerifier.Enabled()
// == false and every call site skips verification until a real key ships.
var DefaultVerifier = func() *Verifier {
	v, err := NewVerifier(pinnedKeyPEMEmbed)
	if err != nil {
		panic("sigverify: embedded pinned key is malformed: " + err.Error())
	}
	return v
}()

// Disabled returns a fresh, always-disabled Verifier (Enabled() == false, so
// Verify is a fail-safe skip). Use this for trust seams that must NOT
// inherit the build-embedded Wendy key (DefaultVerifier) — e.g. per-org
// artifact verification — until their own key is wired in.
func Disabled() *Verifier {
	v, _ := NewVerifier(nil)
	return v
}
