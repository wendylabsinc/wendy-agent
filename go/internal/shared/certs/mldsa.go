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

// OrgMismatchError is returned by the VerifyConnection callback when the
// server certificate's org ID does not match ExpectedOrgID.
type OrgMismatchError struct {
	Want int32 // client's expected org; 0 if client carries no org identity
	Got  int32 // org found in the server certificate
}

func (e *OrgMismatchError) Error() string {
	return fmt.Sprintf("server certificate belongs to org %d, expected org %d", e.Got, e.Want)
}

// PinChecker is satisfied by *devicepin.Store. Defined here as an interface
// so shared/certs does not import shared/devicepin (which would be circular).
type PinChecker interface {
	CheckAndUpdate(leaf *x509.Certificate, displayName string) error
}

// ServerVerifyOpts configures the server certificate verification callback
// returned by BuildServerVerifyConnection.
type ServerVerifyOpts struct {
	ChainPEM      string     // required: PEM-encoded CA chain for ML-DSA-aware chain verification
	ExpectedOrgID int32      // 0 = accept any org (still extracted for pinning key)
	PinStore      PinChecker // nil = skip pinning
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

// BuildServerVerifyConnection returns a VerifyConnection callback that:
//  1. Verifies the server cert chain with ML-DSA fallback (see mldsa.go)
//  2. Extracts the server's Wendy org identity (IdentityFromCert)
//  3. Returns OrgMismatchError if opts.ExpectedOrgID != 0 and orgs differ
//  4. Calls opts.PinStore.CheckAndUpdate if PinStore is non-nil
//
// InsecureSkipVerify must be true on the tls.Config — this callback is the
// actual verification. Go's built-in verifier cannot parse ML-DSA chain certs
// and there is no TLS hostname over L2CAP or passthrough gRPC targets.
func BuildServerVerifyConnection(opts ServerVerifyOpts) (func(tls.ConnectionState) error, error) {
	if opts.ChainPEM == "" {
		return nil, fmt.Errorf("chain PEM is required to verify device server certificate")
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM([]byte(opts.ChainPEM))
	caCerts, err := ParseCertsFromPEM([]byte(opts.ChainPEM))
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

		// Step 1: ML-DSA-aware chain verification.
		intermediates := x509.NewCertPool()
		for _, cert := range cs.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, stdErr := leaf.Verify(x509.VerifyOptions{
			Roots:         caPool,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if stdErr != nil {
			sigOID, oidErr := mldsaCertSigAlgOID(leaf)
			if oidErr != nil {
				return stdErr
			}
			if _, schemeErr := mldsaScheme(sigOID); schemeErr != nil {
				return stdErr
			}
			if mldsaErr := verifyMLDSAServerCert(leaf, caCerts); mldsaErr != nil {
				return mldsaErr
			}
		}

		// Step 2: org identity check.
		identity, hasIdentity, idErr := IdentityFromCert(leaf)
		if idErr != nil {
			return fmt.Errorf("extracting server cert identity: %w", idErr)
		}
		// Grace mode: only reject when the server cert carries a Wendy identity AND it
		// belongs to a different org. A cert with no Wendy identity (e.g. a legacy
		// device not yet re-provisioned) is accepted, mirroring the server-side
		// OrgModeGrace behaviour in interceptor/mtls.go.
		if hasIdentity && opts.ExpectedOrgID != 0 && identity.OrgID != opts.ExpectedOrgID {
			return &OrgMismatchError{Want: opts.ExpectedOrgID, Got: identity.OrgID}
		}

		// Step 3: SPKI pin check/update.
		if opts.PinStore != nil && hasIdentity && identity.EntityType == "asset" {
			displayName := leaf.Subject.CommonName
			if displayName == "" {
				displayName = identity.IdentityKey()
			}
			if pinErr := opts.PinStore.CheckAndUpdate(leaf, displayName); pinErr != nil {
				// Log but don't block — pin I/O failure is not a security failure
				// when the chain has already been verified above.
				_ = pinErr // callers that care about pinning use a Store that logs internally
			}
		}

		return nil
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
