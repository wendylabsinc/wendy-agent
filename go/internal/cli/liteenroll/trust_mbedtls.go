package liteenroll

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
)

// validateMbedTLSTrustBundle checks the certificate algorithms understood by the
// current Lite firmware. Keep this separate from PEM/CA validation: firmware
// with a different verifier will need its own advertised compatibility profile.
// Go's ability to parse a certificate does not establish device compatibility.
func validateMbedTLSTrustBundle(data []byte) error {
	if err := validateTrustBundle(data); err != nil {
		return err
	}
	for rest := bytes.TrimSpace(data); len(rest) > 0; {
		block, tail := pem.Decode(rest)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("invalid certificate: %w", err)
		}
		if !mbedTLSCertificateAlgorithms(cert) {
			return fmt.Errorf("CA public key %s and signature %s are not supported by the current Wendy Lite mbedTLS firmware; the PKI deployment needs an mbedTLS-compatible certificate chain using RSA or ECDSA; no cloud asset was reserved", cert.PublicKeyAlgorithm, cert.SignatureAlgorithm)
		}
		rest = bytes.TrimSpace(tail)
	}
	return nil
}

func mbedTLSCertificateAlgorithms(cert *x509.Certificate) bool {
	switch cert.PublicKeyAlgorithm {
	case x509.RSA, x509.ECDSA:
	default:
		return false
	}
	// Match the RSA and ECDSA signature OIDs parsed by Mbed TLS 3.6, including
	// older root self-signatures. This is a parser compatibility check, not a
	// new certificate verification policy. In particular, SHA-224 OIDs are
	// supported by Mbed TLS even though Go reports an unknown signature enum.
	var outer struct {
		TBS       asn1.RawValue
		Algorithm pkix.AlgorithmIdentifier
		Signature asn1.BitString
	}
	if rest, err := asn1.Unmarshal(cert.Raw, &outer); err != nil || len(rest) != 0 {
		return false
	}
	switch outer.Algorithm.Algorithm.String() {
	case "1.2.840.113549.1.1.4", // RSA with MD5
		"1.2.840.113549.1.1.5", "1.3.14.3.2.29", // RSA with SHA-1
		"1.2.840.113549.1.1.14", // RSA with SHA-224
		"1.2.840.113549.1.1.11", // RSA with SHA-256
		"1.2.840.113549.1.1.12", // RSA with SHA-384
		"1.2.840.113549.1.1.13", // RSA with SHA-512
		"1.2.840.113549.1.1.10", // RSA-PSS
		"1.2.840.10045.4.1",     // ECDSA with SHA-1
		"1.2.840.10045.4.3.1",   // ECDSA with SHA-224
		"1.2.840.10045.4.3.2",   // ECDSA with SHA-256
		"1.2.840.10045.4.3.3",   // ECDSA with SHA-384
		"1.2.840.10045.4.3.4":   // ECDSA with SHA-512
		return true
	default:
		return false
	}
}
