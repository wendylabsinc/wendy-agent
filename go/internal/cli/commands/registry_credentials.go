package commands

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// registryTLSConfig chooses the client credential for a device's image
// registry exactly as the gRPC dial chose it for the device (WDY-3882): the
// cloud operator certificate the connection authenticated with, the local
// operator key when the connection is a local-operator one, and nil only for
// a plaintext connection, whose device serves no TLS at all. An mTLS device
// without a usable credential is refused; it never gets a plaintext push.
//
// Every registry path takes its TLS from here: the Swift/Apple Container and
// OCI-layout pushes through the TLS-terminating loopback proxies, the cloud
// tunnel's TLS dialer, and the docker buildx proxy for local-operator devices.
func registryTLSConfig(conn *grpcclient.AgentConnection) (*tls.Config, error) {
	switch {
	case conn.IsLocalOperator:
		return localRegistryTLSConfig(conn)
	case conn.IsMTLS:
		certInfo := conn.CertInfo
		if certInfo == nil {
			certInfo = loadCLICert()
		}
		if certInfo == nil || certInfo.PemCertificate == "" || !certInfo.HasPrivateKey() {
			return nil, commandErrorf(errRegistryAuth, "mTLS connection but no CLI certificates available")
		}
		keyPEM, err := certInfo.PrivateKeyPEM()
		if err != nil {
			return nil, fmt.Errorf("loading client key: %w", err)
		}
		return cloudRegistryTLSConfig(certInfo.PemCertificate, keyPEM, certInfo.PemCertificateChain)
	default:
		return nil, nil
	}
}

// localRegistryTLSConfig presents the local operator key's self-issued
// certificate (the agent's registry admits keys with role operator or owner)
// and accepts only the device key the gRPC connection verified: the device
// serves its registry under the same self-signed key.
func localRegistryTLSConfig(conn *grpcclient.AgentConnection) (*tls.Config, error) {
	deviceKey := conn.LocalDeviceKey()
	if deviceKey == nil {
		return nil, commandErrorf(errRegistryAuth, "the device's key was not verified on this connection; cannot authenticate its registry")
	}
	key, err := localOperatorKeyFn()
	if err != nil {
		return nil, commandErrorf(errRegistryAuth, "loading the local operator key: %v", err)
	}
	cert, err := localkey.Certificate(key, time.Now())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Authenticated by the device key in VerifyConnection, not a chain.
		InsecureSkipVerify: true, //nolint:gosec
		MinVersion:         tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("device registry presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if _, found, err := certs.IdentityFromCert(leaf); found || err != nil {
				return errors.New("device registry presented a cloud-enrolled certificate to a local-operator connection")
			}
			if !bytes.Equal(leaf.RawSubjectPublicKeyInfo, deviceKey) {
				return errors.New("device registry presented a different key than the device")
			}
			return leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature)
		},
	}, nil
}

// cloudRegistryTLSConfig is the cloud operator certificate's registry TLS:
// the parseable leaf and its key, the server chain-validated against the
// operator's Wendy CA chain (wendyRegistryServerVerifier).
func cloudRegistryTLSConfig(certPEM, keyPEM, caPEM string) (*tls.Config, error) {
	leafPEM, err := certs.LeafCertificatePEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("extracting leaf certificate: %w", err)
	}
	tlsCert, err := tls.X509KeyPair([]byte(leafPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("loading client certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	if certs.AppendChainToPool(caPool, caPEM) == 0 {
		return nil, fmt.Errorf("no valid CA certificates found in caPEM")
	}
	return &tls.Config{
		// Hostname check only; full chain validation happens in VerifyConnection.
		InsecureSkipVerify: true, //nolint:gosec
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{tlsCert},
		VerifyConnection:   wendyRegistryServerVerifier(caPool),
	}, nil
}

// tlsDialerWith wraps dial so every connection is upgraded to TLS with cfg
// before use.
func tlsDialerWith(cfg *tls.Config, dial func(context.Context) (net.Conn, error)) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		raw, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(raw, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("TLS handshake with device registry: %w", err)
		}
		return tlsConn, nil
	}
}
