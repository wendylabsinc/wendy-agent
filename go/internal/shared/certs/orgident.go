// Package certs provides certificate and key utilities for mTLS authentication.
package certs

import (
	"crypto/x509"
	"fmt"
	"strconv"
	"strings"
)

const wendyOrgURNPrefix = "urn:wendy:org:"

// WendyIdentity holds the Wendy org and entity identity extracted from a certificate.
type WendyIdentity struct {
	OrgID      int32
	EntityType string // "user" or "asset"
	EntityID   string // numeric ID as string
}

// IdentityKey returns the canonical URN string used as a pin-store key.
func (w WendyIdentity) IdentityKey() string {
	return fmt.Sprintf("urn:wendy:org:%d:%s:%s", w.OrgID, w.EntityType, w.EntityID)
}

// UserURN returns the canonical Wendy identity URN for a user:
// "urn:wendy:org:<org>:user:<userID>". This is the URI SAN a user (CLI)
// certificate carries as its authoritative identity.
func UserURN(orgID int32, userID string) string {
	return WendyIdentity{OrgID: orgID, EntityType: "user", EntityID: userID}.IdentityKey()
}

// AssetURN returns the canonical Wendy identity URN for an asset:
// "urn:wendy:org:<org>:asset:<assetID>". This is the URI SAN a device (agent)
// certificate carries as its authoritative identity.
func AssetURN(orgID, assetID int32) string {
	return WendyIdentity{OrgID: orgID, EntityType: "asset", EntityID: strconv.Itoa(int(assetID))}.IdentityKey()
}

// tenantSPIFFEPrefix is the trust domain and tenant path Wendy Cloud mints
// under. Cloud relays every client leaf through pki-core's "service-identity"
// profile, so the principal kind is always "service" — never "device", which
// pki-core would refuse for a profile-kind mismatch.
const tenantSPIFFEPrefix = "spiffe://wendy.sh/tenant/"

// AssetSPIFFEURI returns the canonical tenant SPIFFE principal for a device:
// "spiffe://wendy.sh/tenant/<tenantUUID>/service/asset-<assetID>".
//
// Cloud refuses to sign a grant unless the CSR carries exactly this URI SAN
// (WDY-2498/WDY-2584), and pki-core then binds the grant principal to it. It is
// carried *alongside* the urn:wendy AssetURN, not instead of it: the urn is what
// the agent's own org gate reads out of a peer certificate, so dropping it would
// silently disarm org-equality enforcement.
func AssetSPIFFEURI(tenantUUID string, assetID int32) string {
	return tenantSPIFFEPrefix + tenantUUID + "/service/asset-" + strconv.Itoa(int(assetID))
}

// UserSPIFFEURI returns the canonical tenant SPIFFE principal for an operator:
// "spiffe://wendy.sh/tenant/<tenantUUID>/service/user-<userID>". See
// AssetSPIFFEURI for why the kind is "service" and why it does not replace UserURN.
func UserSPIFFEURI(tenantUUID, userID string) string {
	return tenantSPIFFEPrefix + tenantUUID + "/service/user-" + userID
}

// ParseIdentityURN parses a canonical Wendy identity URN —
// "urn:wendy:org:<org>:(user|asset):<id>", the exact string IdentityKey
// produces — back into a WendyIdentity.
//
// It exists because that URN is user-facing: it is the key the device pin store
// is filed under and the key an SPKI refusal prints, so `wendy device unpin`
// has to accept it as an argument. Parsing goes through the same
// parseWendyOrgURN the certificate path uses, so what the CLI accepts from a
// user and what it reads out of a certificate can never drift apart — a second
// hand-rolled parser here would be a second definition of what a Wendy identity
// is.
func ParseIdentityURN(urn string) (WendyIdentity, error) {
	return parseWendyOrgURN(strings.TrimSpace(urn))
}

// IdentityFromCert extracts the Wendy org+entity identity from a certificate.
//
// Resolution order:
//  1. SAN URI beginning with "urn:wendy:org:" (authoritative; exactly one allowed)
//  2. CommonName "sh/wendy/<org>/<asset>" (legacy fallback)
//  3. No identity: returns (zero, false, nil)
func IdentityFromCert(leaf *x509.Certificate) (WendyIdentity, bool, error) {
	var wendyURNs []string
	for _, u := range leaf.URIs {
		raw := u.String()
		if strings.HasPrefix(raw, wendyOrgURNPrefix) {
			wendyURNs = append(wendyURNs, raw)
		}
	}
	if len(wendyURNs) > 1 {
		return WendyIdentity{}, false, fmt.Errorf("certificate contains %d wendy org URNs; expected at most one", len(wendyURNs))
	}
	if len(wendyURNs) == 1 {
		id, err := parseWendyOrgURN(wendyURNs[0])
		if err != nil {
			return WendyIdentity{}, false, err
		}
		return id, true, nil
	}

	cn := leaf.Subject.CommonName
	if strings.HasPrefix(cn, "sh/wendy/") {
		id, err := parseShWendyCN(cn)
		if err != nil {
			return WendyIdentity{}, false, err
		}
		return id, true, nil
	}

	return WendyIdentity{}, false, nil
}

// OrgFromClientCert extracts the org ID from a certificate. It is a wrapper
// around IdentityFromCert that drops entity type and ID.
func OrgFromClientCert(leaf *x509.Certificate) (orgID int32, hasOrg bool, err error) {
	id, ok, err := IdentityFromCert(leaf)
	return id.OrgID, ok, err
}

// parseWendyOrgURN parses "urn:wendy:org:<org>:(user|asset):<id>" into a WendyIdentity.
func parseWendyOrgURN(uri string) (WendyIdentity, error) {
	parts := strings.Split(uri, ":")
	if len(parts) != 6 {
		return WendyIdentity{}, fmt.Errorf("invalid wendy URN format (want 6 colon-separated parts): %s", uri)
	}
	if parts[0] != "urn" || parts[1] != "wendy" || parts[2] != "org" {
		return WendyIdentity{}, fmt.Errorf("invalid wendy URN prefix: %s", uri)
	}
	orgID, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil {
		return WendyIdentity{}, fmt.Errorf("invalid organization ID in URN %q: %w", parts[3], err)
	}
	if orgID <= 0 {
		return WendyIdentity{}, fmt.Errorf("organization ID must be positive, got %d", orgID)
	}
	entityType := parts[4]
	if entityType != "user" && entityType != "asset" {
		return WendyIdentity{}, fmt.Errorf("unknown entity type in wendy URN %q: %s", uri, entityType)
	}
	if parts[5] == "" {
		return WendyIdentity{}, fmt.Errorf("empty entity ID in wendy URN: %s", uri)
	}
	return WendyIdentity{OrgID: int32(orgID), EntityType: entityType, EntityID: parts[5]}, nil
}

// parseShWendyCN parses "sh/wendy/<org>/<asset>" into a WendyIdentity.
// Caller must have verified the CN starts with "sh/wendy/".
func parseShWendyCN(cn string) (WendyIdentity, error) {
	parts := strings.Split(cn, "/")
	if len(parts) != 4 {
		return WendyIdentity{}, fmt.Errorf("invalid sh/wendy CommonName (want 4 slash-separated segments): %s", cn)
	}
	orgID, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return WendyIdentity{}, fmt.Errorf("invalid organization ID in CommonName %q: %w", parts[2], err)
	}
	if orgID <= 0 {
		return WendyIdentity{}, fmt.Errorf("organization ID must be positive, got %d", orgID)
	}
	if parts[3] == "" {
		return WendyIdentity{}, fmt.Errorf("empty asset ID in CommonName: %s", cn)
	}
	return WendyIdentity{OrgID: int32(orgID), EntityType: "asset", EntityID: parts[3]}, nil
}
