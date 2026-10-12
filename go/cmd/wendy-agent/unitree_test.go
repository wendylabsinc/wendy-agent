package main

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

func TestUnitreePairingOwner(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scope    certs.Scope
		enrolled bool
		want     string
	}{
		{"local", certs.Scope{}, false, "local"},
		{"unknown enrolled", certs.Scope{}, true, ""},
		{"legacy tenant", certs.Scope{OrgID: 42}, true, "org:42"},
		{"dual tenant", certs.Scope{OrgID: 42, TenantUUID: "3fd28c47-700a-4ef6-9f43-19be3d13418e"}, true, "tenant:3fd28c47-700a-4ef6-9f43-19be3d13418e"},
		{"PKI tenant", certs.Scope{TenantUUID: "3fd28c47-700a-4ef6-9f43-19be3d13418e"}, true, "tenant:3fd28c47-700a-4ef6-9f43-19be3d13418e"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unitreePairingOwner(tc.scope, tc.enrolled); got != tc.want {
				t.Fatalf("owner = %q, want %q", got, tc.want)
			}
		})
	}
}
