package identity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// Certificate contains only public issued certificate material.
type Certificate struct {
	OrganizationID      int
	PemCertificate      string
	PemCertificateChain string
	PrincipalURI        string
}

// TenantUUID is the immutable tenant identity, never a realm slug.
func (c Certificate) TenantUUID() string {
	tenant, _, _, _ := parsePrincipal(c.PrincipalURI)
	return tenant
}

func parsePrincipal(principal string) (tenant, kind, name string, err error) {
	const prefix = "spiffe://wendy.sh/tenant/"
	if !strings.HasPrefix(principal, prefix) {
		return "", "", "", fmt.Errorf("invalid tenant SPIFFE principal")
	}
	parts := strings.SplitN(strings.TrimPrefix(principal, prefix), "/", 3)
	if len(parts) != 3 || len(parts[0]) != 36 || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("invalid tenant SPIFFE principal")
	}
	for i, c := range parts[0] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", "", "", fmt.Errorf("invalid tenant UUID")
			}
		} else if !strings.ContainsRune("0123456789abcdef", c) {
			return "", "", "", fmt.Errorf("invalid tenant UUID")
		}
	}
	return parts[0], parts[1], parts[2], nil
}

func (c *Client) RequestIdentityCertificate(
	ctx context.Context,
	endpoint string,
	csrKey platform.IdentityKey,
	key platform.Signer,
	accessToken, expectedTenant, subject string,
) (Certificate, error) {
	if accessToken == "" {
		return Certificate{}, fmt.Errorf("requesting pki-core identity certificate: access token is empty")
	}
	htu, err := CanonicalHTU(endpoint)
	if err != nil {
		return Certificate{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Certificate{}, fmt.Errorf("invalid pki-core identity endpoint %q", endpoint)
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if (u.Scheme != "https" && !(u.Scheme == "http" && loopback)) ||
		u.User != nil || u.Fragment != "" || u.Path != "/v1/identity/certificate" {
		return Certificate{}, fmt.Errorf("invalid pki-core identity endpoint %q: want HTTPS (or loopback HTTP) with path /v1/identity/certificate", endpoint)
	}
	csrPEM, err := csrKey.CertificateRequest(ctx, subject)
	if err != nil {
		return Certificate{}, fmt.Errorf("generating operator CSR: %w", err)
	}
	proof, err := c.DPoPAccessProof(ctx, key, http.MethodPost, htu, accessToken)
	if err != nil {
		return Certificate{}, fmt.Errorf("building pki-core DPoP proof: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(csrPEM))
	if err != nil {
		return Certificate{}, fmt.Errorf("building pki-core identity request: %w", err)
	}
	req.Header.Set("Authorization", "DPoP "+accessToken)
	req.Header.Set("DPoP", proof)
	req.Header.Set("Content-Type", "application/pkcs10")
	req.Header.Set("Accept", "application/pem-certificate-chain")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Certificate{}, fmt.Errorf("calling pki-core identity endpoint %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if readErr != nil {
		return Certificate{}, fmt.Errorf("reading pki-core identity response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusUnauthorized {
			detail = "unauthorized (verify the realm-to-tenant mapping and PKI audience)"
		}
		return Certificate{}, fmt.Errorf("pki-core identity endpoint returned %d: %s", resp.StatusCode, detail)
	}
	if len(body) > 1<<20 {
		return Certificate{}, fmt.Errorf("pki-core identity response exceeds 1 MiB")
	}
	checked, err := csrKey.CheckCertificate(ctx, body)
	if err != nil {
		return Certificate{}, err
	}
	principalURI := checked.PrincipalURI
	tenant, kind, name, err := parsePrincipal(principalURI)
	if err != nil {
		return Certificate{}, fmt.Errorf("parsing pki-core certificate principal: %w", err)
	}
	if kind != "operator" || name != subject {
		return Certificate{}, fmt.Errorf("pki-core returned a certificate for a different operator")
	}
	if expectedTenant != "" && tenant != expectedTenant {
		return Certificate{}, fmt.Errorf("pki-core returned tenant %q while renewing tenant %q", tenant, expectedTenant)
	}
	return Certificate{
		PemCertificate:      checked.CertificatePEM,
		PemCertificateChain: checked.ChainPEM,
		PrincipalURI:        principalURI,
	}, nil
}
