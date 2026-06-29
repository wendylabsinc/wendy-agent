package containerd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func ctxWithCert(cert *x509.Certificate) context.Context {
	state := tls.ConnectionState{}
	if cert != nil {
		state.PeerCertificates = []*x509.Certificate{cert}
	}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

func TestDeployedByFromContext(t *testing.T) {
	uri, _ := url.Parse("urn:wendy:org:7:user:42")
	withURN := &x509.Certificate{URIs: []*url.URL{uri}}

	if got := deployedByFromContext(ctxWithCert(withURN)); got != "wendy/user/42 (org 7)" {
		t.Errorf("deployedByFromContext = %q; want %q", got, "wendy/user/42 (org 7)")
	}

	// No peer at all → empty (best-effort, must not panic).
	if got := deployedByFromContext(context.Background()); got != "" {
		t.Errorf("no-peer context = %q; want empty", got)
	}

	// Peer present but no client cert → empty.
	if got := deployedByFromContext(ctxWithCert(nil)); got != "" {
		t.Errorf("no-cert context = %q; want empty", got)
	}

	// No structured identity but a CommonName → fall back to the CN. This is the
	// real-world fleet case: certs are issued with CN="wendy/user/<id>".
	cnCert := &x509.Certificate{Subject: pkix.Name{CommonName: "wendy/user/abc123"}}
	if got := deployedByFromContext(ctxWithCert(cnCert)); got != "wendy/user/abc123" {
		t.Errorf("CN fallback = %q; want %q", got, "wendy/user/abc123")
	}

	// Cert with neither identity nor CN → empty.
	if got := deployedByFromContext(ctxWithCert(&x509.Certificate{})); got != "" {
		t.Errorf("empty cert = %q; want empty", got)
	}
}
