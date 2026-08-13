package clouddefaults

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// UsesPublicCA reports whether addr targets a public-CA endpoint (the production
// :443 convention). WebPKI verification applies there; installing the Wendy-CA
// VerifyConnection pin instead causes x509 failures (WDY-2434).
func UsesPublicCA(addr string) bool {
	return strings.HasSuffix(addr, ":443")
}

// BrokerTLSConfig builds the client mTLS config for dialing the tunnel broker.
// Non-:443 (local/on-prem): skip hostname verification, pin the chain to the
// Wendy CA in cert.PemCertificateChain via VerifyConnection. :443: leave
// standard WebPKI verification in place.
func BrokerTLSConfig(cert config.CertificateInfo, brokerURL string) (*tls.Config, error) {
	keyPEM, err := cert.PrivateKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("loading client key: %w", err)
	}
	tlsCfg, err := certs.LoadTLSConfig(
		cert.PemCertificate,
		cert.PemCertificateChain,
		keyPEM,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("loading broker TLS config: %w", err)
	}

	if !UsesPublicCA(brokerURL) {
		// For non-standard ports (local/on-prem broker) the server presents a cert
		// signed by the Wendy CA, not a public CA. Skip hostname verification but
		// validate the chain against the stored Wendy CA bundle.
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM([]byte(cert.PemCertificateChain)) {
			return nil, fmt.Errorf("no valid CA certificates in PemCertificateChain")
		}
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // Hostname verification is intentionally skipped for non-standard broker endpoints; VerifyConnection validates the chain against the Wendy CA.
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("broker presented no TLS certificate")
			}
			intermediates := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         caPool,
				Intermediates: intermediates,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		}
	}

	return tlsCfg, nil
}
