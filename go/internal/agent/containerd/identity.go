package containerd

import (
	"context"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// deployedByFromContext extracts the mTLS client principal from the gRPC peer
// credentials on ctx and formats it as a stable, human-readable provenance
// string. Preference order:
//
//  1. A structured Wendy identity (org URN SAN or legacy sh/wendy CN) →
//     "wendy/<type>/<id>". The org is intentionally omitted — a device belongs
//     to exactly one org, so it adds nothing to the label.
//  2. The raw certificate CommonName — real-world certs use CN="wendy/user/<id>"
//     (the form the issue calls "the client cert CN is the user identity"),
//     which IdentityFromCert doesn't parse but is exactly the principal we want.
//
// It returns "" when there is no peer cert or the connection isn't mTLS —
// provenance is best-effort and must never fail a deploy. This value is recorded
// for audit/display only; it is NOT used for any authorization decision (that is
// enforced separately by the mTLS interceptor).
func deployedByFromContext(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return ""
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return ""
	}
	leaf := tlsInfo.State.PeerCertificates[0]
	if id, hasID, err := certs.IdentityFromCert(leaf); err == nil && hasID {
		return fmt.Sprintf("wendy/%s/%s", id.EntityType, id.EntityID)
	}
	return leaf.Subject.CommonName
}
