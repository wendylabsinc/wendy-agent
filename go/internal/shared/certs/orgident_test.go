package certs

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"
)

// mustParseURL is a test helper that parses a URL or fatals.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestOrgFromClientCert(t *testing.T) {
	tests := []struct {
		name       string
		cn         string
		uris       []string
		wantOrg    int32
		wantHasOrg bool
		wantErr    bool
	}{
		// SAN URI: user type.
		{
			name:       "SAN user URN",
			uris:       []string{"urn:wendy:org:7:user:abc"},
			wantOrg:    7,
			wantHasOrg: true,
		},
		// SAN URI: asset type.
		{
			name:       "SAN asset URN",
			uris:       []string{"urn:wendy:org:42:asset:5"},
			wantOrg:    42,
			wantHasOrg: true,
		},
		// Non-wendy URI before valid wendy URN — the wendy URN is still found.
		{
			name:       "non-wendy URI before wendy URN",
			uris:       []string{"spiffe://x", "urn:wendy:org:99:asset:1"},
			wantOrg:    99,
			wantHasOrg: true,
		},
		// No URIs, device CN.
		{
			name:       "no URIs device CN sh/wendy/7/5",
			cn:         "sh/wendy/7/5",
			wantOrg:    7,
			wantHasOrg: true,
		},
		// No URIs, user CN — no org (not an error).
		{
			name:       "no URIs user CN wendy/user/abc",
			cn:         "wendy/user/abc",
			wantOrg:    0,
			wantHasOrg: false,
		},
		// No URIs, empty CN.
		{
			name:       "no URIs empty CN",
			cn:         "",
			wantOrg:    0,
			wantHasOrg: false,
		},
		// No URIs, unrelated CN.
		{
			name:       "no URIs unrelated CN",
			cn:         "example.com",
			wantOrg:    0,
			wantHasOrg: false,
		},
		// Malformed wendy URN: non-numeric org.
		{
			name:    "malformed URN non-numeric org",
			uris:    []string{"urn:wendy:org:notanumber:user:x"},
			wantErr: true,
		},
		// Malformed wendy URN: wrong segment count (5 parts).
		{
			name:    "malformed URN wrong segment count",
			uris:    []string{"urn:wendy:org:7:user"},
			wantErr: true,
		},
		// Unknown entity type.
		{
			name:    "URN unknown entity type",
			uris:    []string{"urn:wendy:org:7:group:x"},
			wantErr: true,
		},
		// Valid wendy URN present — CN with different sh/wendy value must NOT be consulted.
		{
			name:       "SAN wins over CN",
			cn:         "sh/wendy/100/200",
			uris:       []string{"urn:wendy:org:7:asset:1"},
			wantOrg:    7,
			wantHasOrg: true,
		},
		// No URIs, CN sh/wendy/abc/5 — bad org in CN.
		{
			name:    "CN sh/wendy bad org",
			cn:      "sh/wendy/abc/5",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cert := &x509.Certificate{
				Subject: pkix.Name{CommonName: tc.cn},
			}
			for _, raw := range tc.uris {
				cert.URIs = append(cert.URIs, mustParseURL(t, raw))
			}

			org, hasOrg, err := OrgFromClientCert(cert)

			if tc.wantErr {
				if err == nil {
					t.Errorf("OrgFromClientCert() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("OrgFromClientCert() unexpected error: %v", err)
			}
			if org != tc.wantOrg {
				t.Errorf("OrgFromClientCert() orgID = %d, want %d", org, tc.wantOrg)
			}
			if hasOrg != tc.wantHasOrg {
				t.Errorf("OrgFromClientCert() hasOrg = %v, want %v", hasOrg, tc.wantHasOrg)
			}
		})
	}
}
