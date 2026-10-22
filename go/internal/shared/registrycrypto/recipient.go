package registrycrypto

import (
	"bytes"
	"crypto/mldsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

const (
	registrationPayloadType = "wendycloud.v2.RegisterEncryptionKeyRequest"
	registerDeviceKey       = "wendycloud.v2.KeyDirectoryService/RegisterDeviceEncryptionKey"
	registerUserKey         = "wendycloud.v2.KeyDirectoryService/RegisterUserEncryptionKey"
	maxRegistrationJWS      = 32768
)

// ErrRegistration is every refusal of a served key registration. Cloud
// serves registrations and is untrusted, so the reason never matters to the
// caller: the recipient is not wrapped to.
var ErrRegistration = errors.New("recipient key registration does not verify")

// VerifyRegistration checks a RecipientKey's registration as the client that
// will wrap to it: the JWS against its x5c leaf, that leaf through chainDER
// (intermediates up to the tenant CA) to the pinned platform root at the
// JWS's signed iat, the leaf identity against principal and its tenant
// against organizationID, the operation against the principal kind, and the
// body digest against the payload. Only ML-DSA-65 is accepted from devices
// (the tier A/B device-signer profile; tier C has no wrap suite yet), ML-DSA-65
// or -87 from operators. It returns the verified payload, a
// RegisterEncryptionKeyRequest, for the caller to decode; the caller then
// recomputes key_id from its public_key and checks its organization_id.
func VerifyRegistration(reg *cloudpbv2.SignedRequest, chainDER [][]byte, root *x509.Certificate, principal, organizationID string) ([]byte, error) {
	payload, err := verifyRegistration(reg, chainDER, root, principal, organizationID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegistration, err)
	}
	return payload, nil
}

func verifyRegistration(reg *cloudpbv2.SignedRequest, chainDER [][]byte, root *x509.Certificate, principal, organizationID string) ([]byte, error) {
	if reg == nil || root == nil {
		return nil, errors.New("no registration or pinned root")
	}
	if reg.GetPayloadType() != registrationPayloadType {
		return nil, errors.New("wrong payload type")
	}
	id, err := certs.ParsePrincipal(principal)
	if err != nil || id.TenantUUID != organizationID {
		return nil, errors.New("principal is not in the organization")
	}
	operation := map[string]string{certs.EntityAsset: registerDeviceKey, certs.EntityUser: registerUserKey}[id.EntityType]
	if operation == "" {
		return nil, errors.New("principal is neither a device nor a user")
	}

	compact := string(reg.GetSignature())
	parts := strings.Split(compact, ".")
	if len(compact) > maxRegistrationJWS || len(parts) != 3 {
		return nil, errors.New("malformed JWS")
	}
	rawHeader, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var header struct {
		Alg string   `json:"alg"`
		X5C []string `json:"x5c"`
	}
	dec := json.NewDecoder(bytes.NewReader(rawHeader))
	dec.DisallowUnknownFields() // kid and crit are refused here
	if err := dec.Decode(&header); err != nil || len(header.X5C) != 1 {
		return nil, errors.New("header must carry alg and exactly one x5c leaf")
	}
	leafDER, err := base64.StdEncoding.Strict().DecodeString(header.X5C[0])
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, err
	}
	if san, ok := certs.TenantPrincipalFromCert(leaf); !ok || san != principal {
		return nil, errors.New("leaf identity is not the principal")
	}

	// The algorithm is fixed by the signer, never by the header.
	pub, ok := leaf.PublicKey.(*mldsa.PublicKey)
	if !ok {
		return nil, errors.New("leaf key is not ML-DSA")
	}
	switch {
	case header.Alg == "ML-DSA-65" && pub.Parameters() == mldsa.MLDSA65():
	case header.Alg == "ML-DSA-87" && pub.Parameters() == mldsa.MLDSA87() && id.EntityType == certs.EntityUser:
	default:
		return nil, errors.New("signature algorithm not allowed for this signer")
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	if err := mldsa.Verify(pub, []byte(parts[0]+"."+parts[1]), sig, nil); err != nil {
		return nil, errors.New("signature does not verify")
	}

	rawClaims, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims struct {
		Operation string `json:"operation"`
		Digest    string `json:"body_sha256"`
		Iat       int64  `json:"iat"`
		Target    struct {
			Tenant   string `json:"tenant"`
			Resource string `json:"resource"`
		} `json:"target"`
	}
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(reg.GetPayload())
	if claims.Operation != operation || claims.Target.Tenant != organizationID || claims.Digest != base64.RawURLEncoding.EncodeToString(sum[:]) {
		return nil, errors.New("claims do not bind this registration")
	}
	if id.EntityType == certs.EntityAsset && claims.Target.Resource != principal {
		return nil, errors.New("device registration names another device")
	}

	// The issuing CA must be the ML-DSA Device Identity CA (or operator CA):
	// a P-256 leaf under it, or any leaf under the Lite CA, is refused above
	// or here.
	if len(chainDER) == 0 {
		return nil, errors.New("no chain to the pinned root")
	}
	chain := make([]*x509.Certificate, 0, len(chainDER))
	for _, der := range chainDER {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		chain = append(chain, c)
	}
	if _, ok := chain[0].PublicKey.(*mldsa.PublicKey); !ok || !bytes.Equal(chain[0].RawSubject, leaf.RawIssuer) {
		return nil, errors.New("issuing CA is not an ML-DSA CA")
	}
	at := time.Unix(claims.Iat, 0)
	if err := certs.VerifyPeerCertificateChain(leaf, chain, []*x509.Certificate{root}, x509.ExtKeyUsageClientAuth, at, at); err != nil {
		return nil, err
	}
	return reg.GetPayload(), nil
}
