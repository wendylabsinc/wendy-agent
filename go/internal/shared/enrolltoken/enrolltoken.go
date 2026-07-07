// Package enrolltoken decodes the (unverified) claims payload of a Wendy
// enrollment token. It never validates the signature — that is the cloud's
// job at certificate-issuance time. It exists so the CLI and the agent derive
// org/asset identity from a token identically.
package enrolltoken

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims holds the fields Wendy embeds in an enrollment token payload.
type Claims struct {
	OrganizationID int32  `json:"org_id"`
	AssetID        int32  `json:"asset_id"`
	UserID         string `json:"user_id"`
	Type           string `json:"type"`
}

// Parse decodes the base64url JSON payload (the second dot-separated segment)
// of an enrollment token. It does not verify the signature.
func Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return Claims{}, fmt.Errorf("invalid enrollment token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decoding token payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, fmt.Errorf("decoding token claims: %w", err)
	}
	return c, nil
}

// ParseAsset decodes an asset-enrollment token and returns its org and asset
// IDs. It errors on any other token type or missing IDs.
func ParseAsset(token string) (orgID, assetID int32, err error) {
	c, err := Parse(token)
	if err != nil {
		return 0, 0, err
	}
	if c.Type != "asset_enrollment" {
		return 0, 0, fmt.Errorf("not an asset enrollment token (type %q)", c.Type)
	}
	if c.OrganizationID == 0 || c.AssetID == 0 {
		return 0, 0, fmt.Errorf("asset enrollment token missing org_id or asset_id")
	}
	return c.OrganizationID, c.AssetID, nil
}
