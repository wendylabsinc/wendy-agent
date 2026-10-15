package localmesh

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// Credentials reuses the agent's provisioned signer and ML-DSA-aware trust
// verifier. Reconstruct this object (and the session/cache) when provisioning or
// trust changes. No mesh-specific private key or trust-on-first-use is created.
//
// The mesh identity is the leaf's device principal
// "spiffe://wendy.sh/tenant/<tenant-uuid>/device/<device-uuid>". Nothing here
// carries an organization number, an asset number, or a hash of either: a Cloud
// asset UUID is a database record ID and never a mesh identity.
type Credentials struct {
	Device      meshid.Device
	Certificate tls.Certificate
	Signer      crypto.Signer
	Verify      IdentityVerifier
	peerTLS     func(meshid.Device) (*tls.Config, error)
}

// NewCredentials loads the provisioned identity and derives the local-mesh
// identity from its leaf. Certificates without a device principal — asset, user,
// service, or legacy numeric principals — are refused outright; there is no
// fallback identity.
func NewCredentials(certPEM, chainPEM, keyPEM string) (*Credentials, error) {
	base, err := mtls.NewClientTLSConfig(certPEM, chainPEM, keyPEM, nil)
	if err != nil {
		return nil, err
	}
	if len(base.Certificates) != 1 {
		return nil, errors.New("missing local-mesh identity")
	}
	cert := base.Certificates[0]
	if len(cert.Certificate) == 0 || len(cert.Certificate) > 16 {
		return nil, errors.New("invalid local-mesh identity bundle")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	self, err := meshid.DeviceFromCert(leaf)
	if err != nil {
		return nil, err
	}
	// The identity's own chain must validate under current trust before this
	// instance advertises anything.
	if err := base.VerifyPeerCertificate(cert.Certificate, nil); err != nil {
		return nil, err
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("identity key cannot sign")
	}
	verifyChain, err := mtls.NewClientTLSConfig(certPEM, chainPEM, keyPEM, nil)
	if err != nil {
		return nil, err
	}
	trustChain, err := certs.ParseCertsFromPEM([]byte(chainPEM))
	if err != nil {
		return nil, err
	}
	verify := func(chain [][]byte, now time.Time) (Identity, error) {
		if len(chain) == 0 || len(chain) > 16 {
			return Identity{}, errors.New("invalid identity bundle")
		}
		if err := verifyChain.VerifyPeerCertificate(chain, nil); err != nil {
			return Identity{}, err
		}
		peerLeaf, err := x509.ParseCertificate(chain[0])
		if err != nil {
			return Identity{}, err
		}
		device, err := meshid.DeviceFromCert(peerLeaf)
		if err != nil {
			return Identity{}, err
		}
		if device.Tenant() != self.Tenant() {
			return Identity{}, errors.New("local-mesh peer is outside the tenant")
		}
		if now.Before(peerLeaf.NotBefore) || !now.Before(peerLeaf.NotAfter) {
			return Identity{}, errors.New("local-mesh identity is not currently valid")
		}
		expires := peerLeaf.NotAfter
		for _, certificate := range trustChain {
			if certificate.NotAfter.Before(expires) {
				expires = certificate.NotAfter
			}
		}
		return Identity{Device: device, NotAfter: expires}, nil
	}
	peerTLS := func(peer meshid.Device) (*tls.Config, error) {
		config, err := mtls.NewClientTLSConfig(certPEM, chainPEM, keyPEM, nil)
		if err != nil {
			return nil, err
		}
		pinPeer(config, peer)
		config.MinVersion = tls.VersionTLS13
		config.MaxVersion = tls.VersionTLS13
		config.NextProtos = []string{LinkALPN}
		config.ClientAuth = tls.RequireAnyClientCert
		config.ClientSessionCache = nil
		config.SessionTicketsDisabled = true
		return config, nil
	}
	return &Credentials{Device: self, Certificate: cert, Signer: signer, Verify: verify, peerTLS: peerTLS}, nil
}

// pinPeer requires the handshake leaf to carry exactly the configured peer's
// principal. mtls.NewClientTLSConfig already re-checks chains on resumed
// sessions; wrapping both hooks keeps that guarantee and adds the pin.
func pinPeer(config *tls.Config, peer meshid.Device) {
	chainVerify := config.VerifyPeerCertificate
	pinned := func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if err := chainVerify(rawCerts, verifiedChains); err != nil {
			return err
		}
		if len(rawCerts) == 0 {
			return errors.New("missing local-mesh peer certificate")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		device, err := meshid.DeviceFromCert(leaf)
		if err != nil {
			return err
		}
		if device.Principal() != peer.Principal() {
			return errors.New("local-mesh peer identity mismatch")
		}
		return nil
	}
	config.VerifyPeerCertificate = pinned
	resumeVerify := config.VerifyConnection
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if resumeVerify != nil {
			if err := resumeVerify(state); err != nil {
				return err
			}
		}
		if !state.DidResume {
			return nil
		}
		// A resumed handshake presents no fresh certificates: re-run the pin
		// against the ones restored from the session ticket.
		raw := make([][]byte, len(state.PeerCertificates))
		for i, certificate := range state.PeerCertificates {
			raw[i] = certificate.Raw
		}
		return pinned(raw, nil)
	}
}

// PeerTLS builds the client configuration for one configured peer, pinned to
// that peer's full device principal.
func (c *Credentials) PeerTLS(peer meshid.Device) (*tls.Config, error) {
	if peer.Principal() == c.Device.Principal() {
		return nil, errors.New("self link")
	}
	if peer.Tenant() != c.Device.Tenant() {
		return nil, errors.New("local-mesh peer is outside the tenant")
	}
	return c.peerTLS(peer)
}
