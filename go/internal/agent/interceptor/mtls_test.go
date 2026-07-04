package interceptor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// mustParseURL parses a raw URL for use in a cert's SAN URIs, failing the test
// if it cannot be parsed.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing URL %q: %v", raw, err)
	}
	return u
}

// leafOptions configures the hand-constructed leaf certificate used in tests.
type leafOptions struct {
	commonName  string
	uris        []*url.URL
	noClientEKU bool
}

// buildLeaf constructs an *x509.Certificate suitable for the CheckMTLS struct-field
// inspection. CheckMTLS only reads struct fields (the handshake already verified the
// signature) so a literal works — no real signed cert is needed.
func buildLeaf(opts leafOptions) *x509.Certificate {
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: opts.commonName},
		URIs:         opts.uris,
	}
	if !opts.noClientEKU {
		leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	return leaf
}

// ctxWithLeaf builds a gRPC context carrying the given leaf as the peer's single
// presented client certificate.
func ctxWithLeaf(leaf *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{leaf},
			},
		},
	})
}

func TestCheckMTLS_OrgEnforcement(t *testing.T) {
	logger := zap.NewNop()
	const expectedOrg int32 = 7

	tests := []struct {
		name     string
		leaf     *x509.Certificate
		mode     OrgMode
		wantCode codes.Code // codes.OK means no error expected
	}{
		{
			name:     "asset CN org match grace",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/7/5"}),
			mode:     OrgModeGrace,
			wantCode: codes.OK,
		},
		{
			name:     "asset CN org match strict",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/7/5"}),
			mode:     OrgModeStrict,
			wantCode: codes.OK,
		},
		{
			name:     "user SAN org match grace",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:7:user:abc")}}),
			mode:     OrgModeGrace,
			wantCode: codes.OK,
		},
		{
			name:     "user SAN org match strict",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:7:user:abc")}}),
			mode:     OrgModeStrict,
			wantCode: codes.OK,
		},
		{
			name:     "CN org mismatch grace",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/9/5"}),
			mode:     OrgModeGrace,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "CN org mismatch strict",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/9/5"}),
			mode:     OrgModeStrict,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "user SAN mismatch grace",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:9:user:x")}}),
			mode:     OrgModeGrace,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "user SAN mismatch strict",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:9:user:x")}}),
			mode:     OrgModeStrict,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "no-org legacy user cert grace allowed",
			leaf:     buildLeaf(leafOptions{commonName: "wendy/user/abc"}),
			mode:     OrgModeGrace,
			wantCode: codes.OK,
		},
		{
			name:     "no-org legacy user cert strict rejected",
			leaf:     buildLeaf(leafOptions{commonName: "wendy/user/abc"}),
			mode:     OrgModeStrict,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "malformed org claim grace rejected",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:0:user:x")}}),
			mode:     OrgModeGrace,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "malformed org claim strict rejected",
			leaf:     buildLeaf(leafOptions{uris: []*url.URL{mustParseURL(t, "urn:wendy:org:0:user:x")}}),
			mode:     OrgModeStrict,
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "mode off skips org check on mismatch",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/9/5"}),
			mode:     OrgModeOff,
			wantCode: codes.OK,
		},
		{
			name:     "regression no clientAuth EKU rejected in grace",
			leaf:     buildLeaf(leafOptions{commonName: "sh/wendy/7/5", noClientEKU: true}),
			mode:     OrgModeGrace,
			wantCode: codes.Unauthenticated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithLeaf(tc.leaf)
			err := CheckMTLS(ctx, logger, expectedOrg, tc.mode)
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("CheckMTLS code = %v (err=%v); want %v", got, err, tc.wantCode)
			}
		})
	}
}

func TestCheckMTLS_NoPeerCertificates(t *testing.T) {
	logger := zap.NewNop()
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: nil}},
	})
	err := CheckMTLS(ctx, logger, 7, OrgModeGrace)
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("CheckMTLS code = %v (err=%v); want Unauthenticated", got, err)
	}
}

func TestParseOrgMode(t *testing.T) {
	tests := []struct {
		in       string
		wantMode OrgMode
		wantOK   bool
	}{
		{"", OrgModeGrace, true},
		{"grace", OrgModeGrace, true},
		{"GRACE", OrgModeGrace, true},
		{" strict ", OrgModeStrict, true},
		{"strict", OrgModeStrict, true},
		{"off", OrgModeOff, true},
		{"OFF", OrgModeOff, true},
		{"bogus", OrgModeGrace, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			gotMode, gotOK := ParseOrgMode(tc.in)
			if gotMode != tc.wantMode || gotOK != tc.wantOK {
				t.Fatalf("ParseOrgMode(%q) = (%v, %v); want (%v, %v)", tc.in, gotMode, gotOK, tc.wantMode, tc.wantOK)
			}
		})
	}
}

func TestOrgModeString(t *testing.T) {
	tests := []struct {
		mode OrgMode
		want string
	}{
		{OrgModeOff, "off"},
		{OrgModeGrace, "grace"},
		{OrgModeStrict, "strict"},
	}
	for _, tc := range tests {
		if got := tc.mode.String(); got != tc.want {
			t.Fatalf("OrgMode(%d).String() = %q; want %q", tc.mode, got, tc.want)
		}
	}
}
