package commands

import (
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/cloudrelay"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// A request-signing certificate is separate from the ECDSA TLS certificate:
// Go TLS cannot present the ML-DSA-only credential required for tunnel reqsigs.
// Until login can obtain both profiles, an explicitly supplied signing pair
// must belong to exactly the same operator principal as the active session.
func tunnelPrincipalSigner(auth *config.AuthConfig) (func([]byte) ([]byte, error), error) {
	return configuredTunnelSigner(auth, cloudrelay.PrincipalSigner)
}
func liteRelayOptions(auth *config.AuthConfig) (liteclient.RelayOptions, error) {
	options := liteclient.RelayOptions{}
	if auth == nil || len(auth.Certificates) == 0 {
		return options, fmt.Errorf("no operator certificate")
	}
	cert, err := cloudrelay.LiteOperatorIdentity(auth.Certificates[0])
	if err != nil {
		return options, err
	}
	leaves, err := certs.ParseCertsFromPEM([]byte(cert.PemCertificate))
	if err != nil || len(leaves) != 1 {
		return options, fmt.Errorf("invalid Lite operator certificate")
	}
	principal, ok := certs.TenantPrincipalFromCert(leaves[0])
	if !ok || principal != auth.Certificates[0].PrincipalURI {
		return options, fmt.Errorf("Lite operator certificate does not belong to the selected operator")
	}
	key, err := cert.PrivateKeyPEM()
	if err != nil {
		return options, err
	}
	options.SignRequest, err = cloudrelay.LitePrincipalSigner(cert.PemCertificate, []byte(key))
	if err != nil {
		return options, err
	}
	// Freeze key material with the selected certificate, so neither the
	// provider's stored identities nor an environment override can replace it.
	cert.PemPrivateKey = key
	options.OperatorIdentity = cert
	return options, nil
}
func configuredTunnelSigner(auth *config.AuthConfig, signer func(string, []byte) (func([]byte) ([]byte, error), error)) (func([]byte) ([]byte, error), error) {
	if len(auth.Certificates) == 0 {
		return nil, fmt.Errorf("no operator certificate")
	}
	cert := auth.Certificates[0]
	certPath, keyPath := os.Getenv("WENDY_TUNNEL_SIGNING_CERT"), os.Getenv("WENDY_TUNNEL_SIGNING_KEY")
	if certPath == "" && keyPath == "" {
		key, err := cert.PrivateKeyPEM()
		if err != nil {
			return nil, err
		}
		return signer(cert.PemCertificate, []byte(key))
	}
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("set both WENDY_TUNNEL_SIGNING_CERT and WENDY_TUNNEL_SIGNING_KEY")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading tunnel signing certificate: %w", err)
	}
	leafPEM, err := certs.LeafCertificatePEM(string(certPEM))
	if err != nil {
		return nil, err
	}
	leaves, err := certs.ParseCertsFromPEM([]byte(leafPEM))
	if err != nil || len(leaves) != 1 {
		return nil, fmt.Errorf("invalid tunnel signing certificate")
	}
	principal, ok := certs.TenantPrincipalFromCert(leaves[0])
	if !ok || principal != cert.PrincipalURI {
		return nil, fmt.Errorf("tunnel signing certificate does not belong to the logged-in operator")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading tunnel signing key: %w", err)
	}
	defer clear(key)
	return signer(leafPEM, key)
}
