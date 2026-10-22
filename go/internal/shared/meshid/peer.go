package meshid

import (
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// PeerFromPrincipal admits a device or organization operator to the mesh. The
// Device type also carries operator endpoints for the routing API, but preserves
// their full operator principal. DeviceFromPrincipal remains device-only.
// Operator endpoints derive their routing UUID with a separate hash domain;
// they cannot claim a device principal or its certificate-authorized routes.
func PeerFromPrincipal(principal string) (Device, error) {
	if device, err := DeviceFromPrincipal(principal); err == nil {
		return device, nil
	}
	identity, err := certs.ParsePrincipal(principal)
	if err != nil {
		return Device{}, err
	}
	if identity.EntityType != certs.EntityUser || !strings.HasPrefix(principal, "spiffe://wendy.sh/tenant/"+identity.TenantUUID+"/operator/") {
		return Device{}, errors.New("mesh peer must be a device or organization operator")
	}
	tenant, err := ParseTenant(identity.TenantUUID)
	if err != nil {
		return Device{}, err
	}
	sum := sha256.Sum256([]byte("wendy-net/operator-endpoint/1\x00" + principal))
	return Device{tenant: tenant, id: uuid.UUID(sum[:16]), operator: principal}, nil
}
func PeerFromCert(leaf *x509.Certificate) (Device, error) {
	identity, found, err := certs.IdentityFromCert(leaf)
	if err != nil {
		return Device{}, err
	}
	if !found || identity.Principal == "" {
		return Device{}, errors.New("mesh peer lacks a tenant principal")
	}
	return PeerFromPrincipal(identity.Principal)
}
func (d Device) IsOperator() bool { return d.operator != "" }
