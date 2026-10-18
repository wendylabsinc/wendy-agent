package mtls

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"go.uber.org/zap"
)

// NewServerTLSConfig is NewTLSConfig plus local operators: a client
// certificate that names no tenant and whose public key is in keys is
// accepted on its own (self-issued, no chain, no issuer), provided it carries
// the clientAuth EKU and is inside its validity window (with the same
// NotBefore floor and lifetime cap as chain certificates). Every other client
// certificate takes the chain path.
//
// chainPEM may be empty when keys is non-nil: the device is not enrolled,
// certPEM is its self-signed certificate (SelfSignedCert) and only local
// operators can connect.
func NewServerTLSConfig(certPEM, chainPEM, keyPEM string, logger *zap.Logger, notBeforeFloor time.Time, keys *trustedkeys.Store) (*tls.Config, error) {
	return newLocalOperatorTLSConfig(certPEM, chainPEM, keyPEM, logger, notBeforeFloor, keys, trustedkeys.RoleViewer)
}

// NewRegistryTLSConfig is NewServerTLSConfig for the image registry: a push
// stores images, so local keys below operator are refused at the handshake.
// The registry has no per-request authorization of its own.
func NewRegistryTLSConfig(certPEM, chainPEM, keyPEM string, logger *zap.Logger, notBeforeFloor time.Time, keys *trustedkeys.Store) (*tls.Config, error) {
	return newLocalOperatorTLSConfig(certPEM, chainPEM, keyPEM, logger, notBeforeFloor, keys, trustedkeys.RoleOperator)
}

func newLocalOperatorTLSConfig(certPEM, chainPEM, keyPEM string, logger *zap.Logger, notBeforeFloor time.Time, keys *trustedkeys.Store, minRole trustedkeys.Role) (*tls.Config, error) {
	if keys == nil {
		return NewTLSConfig(certPEM, chainPEM, keyPEM, logger, notBeforeFloor)
	}
	var (
		cfg         *tls.Config
		chainVerify func([][]byte, [][]*x509.Certificate) error
	)
	if chainPEM == "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("loading X509 key pair: %w", err)
		}
		cfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			MinVersion:   tls.VersionTLS12,
		}
		wireSessionTicketChecks(cfg, notBeforeFloor, time.Now)
	} else {
		var err error
		if cfg, err = NewTLSConfig(certPEM, chainPEM, keyPEM, logger, notBeforeFloor); err != nil {
			return nil, err
		}
		chainVerify = cfg.VerifyPeerCertificate
	}

	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, chains [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("no client certificate presented")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parsing client certificate: %w", err)
		}
		if keys.Dormant() && trustedkeys.SelfSigned(leaf) {
			if _, found, err := certs.IdentityFromCert(leaf); !found && err == nil {
				// Cloud-enrolled: refused like any unknown certificate, with
				// no fallback to the saved keys.
				if logger != nil {
					logger.Warn("local operator certificate refused: cloud-enrolled",
						zap.String("fingerprint", trustedkeys.Fingerprint(leaf.RawSubjectPublicKeyInfo)))
				}
				return errors.New("client certificate is not trusted")
			}
		}
		if role, fp, local := localOperator(leaf, keys); local {
			if !trustedkeys.SelfSigned(leaf) {
				err := errors.New("local operator certificate must be self-signed")
				logCertRejection(logger, leaf, err, time.Now(), time.Now())
				return err
			}
			if role < minRole {
				err := fmt.Errorf("trusted key role %s cannot use this listener (needs %s)", role, minRole)
				logCertRejection(logger, leaf, err, time.Now(), time.Now())
				return err
			}
			if err := verifyLocalOperatorCert(leaf, notBeforeFloor, time.Now()); err != nil {
				logCertRejection(logger, leaf, err, time.Now(), time.Now())
				return err
			}
			if logger != nil {
				logger.Info("local operator certificate accepted",
					zap.String("fingerprint", fp), zap.String("role", role.String()))
			}
			return nil
		}
		if chainVerify == nil {
			return errors.New("client certificate is not signed by a trusted key")
		}
		return chainVerify(rawCerts, chains)
	}
	// VerifyConnection also runs on resumed sessions, where
	// VerifyPeerCertificate does not: re-check the key so a removed (or
	// demoted) key cannot resume, which matters for the registry, which has
	// no per-request check. The direct-PKI tenant-prefix check (previous)
	// applies to chain certificates; a local operator certificate names no
	// tenant by construction.
	previous := cfg.VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) > 0 {
			leaf := cs.PeerCertificates[0]
			if role, _, local := localOperator(leaf, keys); local {
				if role < minRole {
					return fmt.Errorf("trusted key role %s cannot use this listener (needs %s)", role, minRole)
				}
				return nil
			}
			if _, found, err := certs.IdentityFromCert(leaf); !found && err == nil && trustedkeys.SelfSigned(leaf) {
				return errors.New("client certificate key is not trusted")
			}
		}
		if previous != nil {
			return previous(cs)
		}
		return nil
	}
	return cfg, nil
}

// localOperator reports whether leaf is a local operator certificate: it names
// no tenant (no SPIFFE principal, org URN or sh/wendy CN) and its key is
// trusted. A certificate naming a tenant is never local, so a key can not lift
// a tenant certificate into a local role nor a self-issued certificate claim a
// tenant.
func localOperator(leaf *x509.Certificate, keys *trustedkeys.Store) (trustedkeys.Role, string, bool) {
	if _, found, err := certs.IdentityFromCert(leaf); found || err != nil {
		return trustedkeys.RoleNone, "", false
	}
	role, fp := keys.Lookup(leaf.RawSubjectPublicKeyInfo)
	return role, fp, role != trustedkeys.RoleNone
}

func verifyLocalOperatorCert(leaf *x509.Certificate, notBeforeFloor, realNow time.Time) error {
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return errors.New("certificate is not valid for client authentication")
	}
	effectiveNow := effectiveVerificationTime(realNow, notBeforeFloor, leaf.NotBefore)
	if effectiveNow.Before(leaf.NotBefore) || realNow.After(leaf.NotAfter) {
		return fmt.Errorf("certificate not valid at current time (NotBefore=%v NotAfter=%v)", leaf.NotBefore, leaf.NotAfter)
	}
	return checkRevocation(leaf)
}

// SelfSignedCert makes the device's server certificate for a device that is
// not enrolled, from its own key so the SPKI a client pins stays the same
// after enrollment. Clients authenticate it by that pin, not by a chain.
func SelfSignedCert(keyPEM string, now time.Time) (string, error) {
	key, err := certs.ParseSigningPrivateKeyPEM([]byte(keyPEM))
	if err != nil {
		return "", fmt.Errorf("parsing device key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wendy-device-local"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(maxCertLifetime - 2*time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return "", fmt.Errorf("self-signing device certificate: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}
