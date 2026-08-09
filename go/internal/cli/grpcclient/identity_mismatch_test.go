package grpcclient

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// TestIdentityMismatchNilConnection covers the accessor's contract on a
// connection that never installed the sink (plaintext / NewFromConn).
func TestIdentityMismatchNilConnection(t *testing.T) {
	c := &AgentConnection{}
	if got, ok := c.IdentityMismatch(); ok || got != nil {
		t.Fatalf("want (nil, false), got (%v, %v)", got, ok)
	}
}

// TestIdentityMismatchRecorded covers the sink → accessor round trip that the
// dial ladder relies on to distinguish "wrong device" from "handshake failed".
func TestIdentityMismatchRecorded(t *testing.T) {
	c := newAgentConnection(nil)
	sink := identityMismatchSink(c.identityMismatch)
	sink(&certs.IdentityMismatchError{WantOrg: 7, WantAsset: "42", GotOrg: 7, GotAsset: "43"})

	got, ok := c.IdentityMismatch()
	if !ok {
		t.Fatal("want ok=true after sink fired")
	}
	if got.WantAsset != "42" || got.GotAsset != "43" {
		t.Fatalf("want 42→43, got %s→%s", got.WantAsset, got.GotAsset)
	}
}

// TestVerifyConnectionNotVerifyPeerCertificate locks in the resumption-safety
// invariant: a resumed TLS 1.3 handshake skips VerifyPeerCertificate entirely
// and calls only VerifyConnection, so the identity check must live there or it
// silently stops running after the first connect.
func TestVerifyConnectionNotVerifyPeerCertificate(t *testing.T) {
	cfg, err := newAgentTLSConfig("dev.local:50052", testCertInfo(t), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newAgentTLSConfig: %v", err)
	}
	if cfg.VerifyConnection == nil {
		t.Error("VerifyConnection must be set")
	}
	if cfg.VerifyPeerCertificate != nil {
		t.Error("VerifyPeerCertificate must stay nil: a resumed handshake skips it")
	}
}
