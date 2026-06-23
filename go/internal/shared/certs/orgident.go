package certs

import (
	"crypto/x509"
	"fmt"
	"strconv"
	"strings"
)

// wendyOrgURNPrefix is the URI prefix used to encode Wendy org identity in SAN URIs.
const wendyOrgURNPrefix = "urn:wendy:org:"

// OrgFromClientCert extracts the Wendy organization ID a certificate is bound to.
//
// Resolution order:
//  1. If the cert has any SAN URI beginning with "urn:wendy:org:", those URIs are
//     authoritative. Exactly one such URI must be present; more than one returns an
//     error (ambiguous/mis-issued cert). The single URN MUST parse as
//     urn:wendy:org:<org>:(user|asset):<id>; a present-but-malformed wendy URN
//     returns an error (anomalous/suspicious). The org ID must be positive (> 0);
//     zero or negative values return an error. The CN is NOT consulted in this case.
//  2. Otherwise, if the CommonName has the form sh/wendy/<org>/<asset>, the org is
//     parsed from it. A CN that begins with "sh/wendy/" but does not parse cleanly
//     returns an error. The org ID must be positive (> 0).
//  3. Otherwise no org identity is present: returns (0, false, nil). (e.g. a user
//     cert with CN wendy/user/<id> and no SAN.)
//
// Returns:
//
//	(org, true, nil)  — a valid org identity was found
//	(0, false, nil)   — no org identity present (legacy/unknown); not an error
//	(0, false, err)   — an org identity was present but malformed/unparseable,
//	                    the org ID was non-positive, or multiple wendy org URNs
//	                    were found (ambiguous).
func OrgFromClientCert(leaf *x509.Certificate) (orgID int32, hasOrg bool, err error) {
	// Step 1: check SAN URIs for wendy org URNs.
	var wendyURNs []string
	for _, u := range leaf.URIs {
		raw := u.String()
		if strings.HasPrefix(raw, wendyOrgURNPrefix) {
			wendyURNs = append(wendyURNs, raw)
		}
	}
	if len(wendyURNs) > 1 {
		return 0, false, fmt.Errorf("certificate contains %d wendy org URNs; expected at most one", len(wendyURNs))
	}
	if len(wendyURNs) == 1 {
		// Found exactly one wendy URN — it is authoritative; parse it or return error.
		org, err := parseWendyOrgURN(wendyURNs[0])
		if err != nil {
			return 0, false, err
		}
		return org, true, nil
	}

	// Step 2: fall back to CommonName.
	cn := leaf.Subject.CommonName
	if strings.HasPrefix(cn, "sh/wendy/") {
		org, err := parseShWendyCN(cn)
		if err != nil {
			return 0, false, err
		}
		return org, true, nil
	}

	// Step 3: no org identity present.
	return 0, false, nil
}

// parseWendyOrgURN parses a URN of the form urn:wendy:org:<org>:(user|asset):<id>
// and returns the org ID as int32.
func parseWendyOrgURN(uri string) (int32, error) {
	parts := strings.Split(uri, ":")
	if len(parts) != 6 {
		return 0, fmt.Errorf("invalid wendy URN format (want 6 colon-separated parts): %s", uri)
	}
	if parts[0] != "urn" || parts[1] != "wendy" || parts[2] != "org" {
		return 0, fmt.Errorf("invalid wendy URN prefix: %s", uri)
	}
	orgID, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid organization ID in URN %q: %w", parts[3], err)
	}
	if orgID <= 0 {
		return 0, fmt.Errorf("organization ID must be positive, got %d", orgID)
	}
	entityType := parts[4]
	if entityType != "user" && entityType != "asset" {
		return 0, fmt.Errorf("unknown entity type in wendy URN %q: %s", uri, entityType)
	}
	if parts[5] == "" {
		return 0, fmt.Errorf("empty entity ID in wendy URN: %s", uri)
	}
	return int32(orgID), nil
}

// parseShWendyCN parses a CommonName of the form sh/wendy/<org>/<asset>
// and returns the org ID as int32. The caller must have already verified
// the CN starts with "sh/wendy/".
func parseShWendyCN(cn string) (int32, error) {
	parts := strings.Split(cn, "/")
	// Expect exactly ["sh", "wendy", "<org>", "<asset>"].
	if len(parts) != 4 {
		return 0, fmt.Errorf("invalid sh/wendy CommonName (want 4 slash-separated segments): %s", cn)
	}
	orgID, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid organization ID in CommonName %q: %w", parts[2], err)
	}
	if orgID <= 0 {
		return 0, fmt.Errorf("organization ID must be positive, got %d", orgID)
	}
	if parts[3] == "" {
		return 0, fmt.Errorf("empty asset ID in CommonName: %s", cn)
	}
	return int32(orgID), nil
}
