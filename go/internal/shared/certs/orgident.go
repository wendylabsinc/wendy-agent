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
