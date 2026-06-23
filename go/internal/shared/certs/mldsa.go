package certs

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"time"

	circlSign "github.com/cloudflare/circl/sign"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/mldsa/mldsa87"
)

var (
	oidMLDSA65 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 18}
	oidMLDSA87 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 19}
)

type mldsaAlgID struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type mldsaCertOuter struct {
	TBSCertificate     asn1.RawValue
	SignatureAlgorithm mldsaAlgID
	Signature          asn1.BitString
}

type mldsaSPKI struct {
	Algorithm mldsaAlgID
	PublicKey asn1.BitString
}

// ParseCertsFromPEM parses all CERTIFICATE blocks from a PEM bundle, handling
// ML-DSA certificates that produce "trailing data" errors from Go's standard
// x509 parser by stripping to the exact outer ASN.1 SEQUENCE.
func ParseCertsFromPEM(chainPEM []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := chainPEM
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			var raw asn1.RawValue
			if _, asn1Err := asn1.Unmarshal(block.Bytes, &raw); asn1Err == nil {
				cert, err = x509.ParseCertificate(raw.FullBytes)
			}
		}
		if err != nil {
			continue
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

func mldsaCertSigAlgOID(cert *x509.Certificate) (asn1.ObjectIdentifier, error) {
	var outer mldsaCertOuter
	if _, err := asn1.Unmarshal(cert.Raw, &outer); err != nil {
		return nil, fmt.Errorf("parsing certificate ASN.1: %w", err)
	}
	return outer.SignatureAlgorithm.Algorithm, nil
}

func mldsaScheme(oid asn1.ObjectIdentifier) (circlSign.Scheme, error) {
	switch {
	case oid.Equal(oidMLDSA65):
		return mldsa65.Scheme(), nil
	case oid.Equal(oidMLDSA87):
		return mldsa87.Scheme(), nil
	default:
		return nil, fmt.Errorf("unsupported ML-DSA OID: %v", oid)
	}
}

func mldsaIssuerPublicKeyBytes(issuer *x509.Certificate) ([]byte, error) {
	var s mldsaSPKI
	if _, err := asn1.Unmarshal(issuer.RawSubjectPublicKeyInfo, &s); err != nil {
		return nil, fmt.Errorf("parsing SubjectPublicKeyInfo: %w", err)
	}
	return s.PublicKey.Bytes, nil
}

func verifyMLDSASignature(issuer, cert *x509.Certificate) error {
	sigOID, err := mldsaCertSigAlgOID(cert)
	if err != nil {
		return err
	}
	scheme, err := mldsaScheme(sigOID)
	if err != nil {
		return err
	}
	pubKeyBytes, err := mldsaIssuerPublicKeyBytes(issuer)
	if err != nil {
		return err
	}
	pk, err := scheme.UnmarshalBinaryPublicKey(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("parsing ML-DSA public key: %w", err)
	}
	opts := &circlSign.SignatureOpts{Context: ""}
	if !scheme.Verify(pk, cert.RawTBSCertificate, cert.Signature, opts) {
		return fmt.Errorf("ML-DSA signature verification failed")
	}
	return nil
}

// BuildServerVerifyConnection returns a VerifyConnection callback that verifies
// the server's certificate against chainPEM with ML-DSA fallback support.
// It is intended for use alongside InsecureSkipVerify: true to bypass Go's
// built-in hostname and chain verification while still performing full ML-DSA-
// aware chain validation against the Wendy PKI.
// Returns an error if chainPEM is empty or contains no parseable CA certificates.
func BuildServerVerifyConnection(chainPEM string) (func(tls.ConnectionState) error, error) {
	if chainPEM == "" {
		return nil, fmt.Errorf("chain PEM is required to verify device server certificate")
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM([]byte(chainPEM))
	caCerts, err := ParseCertsFromPEM([]byte(chainPEM))
	if err != nil {
		return nil, fmt.Errorf("parsing chain PEM: %w", err)
	}
	if len(caCerts) == 0 {
		return nil, fmt.Errorf("no valid CA certificates found in chain PEM")
	}

	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("device presented no TLS certificate")
		}
		leaf := cs.PeerCertificates[0]

		intermediates := x509.NewCertPool()
		for _, cert := range cs.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}

		// Try standard Go verification first (handles ECDSA/RSA chains).
		_, stdErr := leaf.Verify(x509.VerifyOptions{
			Roots:         caPool,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if stdErr == nil {
			return nil
		}

		// Fall back to ML-DSA verification when the leaf is ML-DSA-signed.
		sigOID, oidErr := mldsaCertSigAlgOID(leaf)
		if oidErr != nil {
			return stdErr
		}
		if _, schemeErr := mldsaScheme(sigOID); schemeErr != nil {
			return stdErr
		}
		return verifyMLDSAServerCert(leaf, caCerts)
	}, nil
}

// verifyMLDSAServerCert verifies a server leaf cert against trusted CAs using
// ML-DSA signature verification, requiring ExtKeyUsageServerAuth.
func verifyMLDSAServerCert(leaf *x509.Certificate, trustedCAs []*x509.Certificate) error {
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("server certificate not valid at current time (NotBefore=%v NotAfter=%v)", leaf.NotBefore, leaf.NotAfter)
	}

	if len(leaf.ExtKeyUsage) > 0 {
		hasServerAuth := false
		for _, eku := range leaf.ExtKeyUsage {
			if eku == x509.ExtKeyUsageServerAuth || eku == x509.ExtKeyUsageAny {
				hasServerAuth = true
				break
			}
		}
		if !hasServerAuth {
			return fmt.Errorf("device certificate is not valid for server authentication")
		}
	}

	var lastErr error
	foundSubject := false
	for _, ca := range trustedCAs {
		if !bytes.Equal(ca.RawSubject, leaf.RawIssuer) {
			continue
		}
		foundSubject = true
		if now.Before(ca.NotBefore) || now.After(ca.NotAfter) {
			lastErr = fmt.Errorf("CA certificate %q not valid at current time", ca.Subject.CommonName)
			continue
		}
		if !ca.BasicConstraintsValid || !ca.IsCA {
			lastErr = fmt.Errorf("certificate %q is not a CA", ca.Subject.CommonName)
			continue
		}
		if ca.KeyUsage != 0 && ca.KeyUsage&x509.KeyUsageCertSign == 0 {
			lastErr = fmt.Errorf("certificate %q is not permitted to sign certificates", ca.Subject.CommonName)
			continue
		}
		if err := verifyMLDSASignature(ca, leaf); err != nil {
			lastErr = fmt.Errorf("invalid signature from CA %q: %w", ca.Subject.CommonName, err)
			continue
		}
		return nil
	}

	if !foundSubject {
		return fmt.Errorf("device certificate issuer not found in trusted CA pool")
	}
	return lastErr
}
