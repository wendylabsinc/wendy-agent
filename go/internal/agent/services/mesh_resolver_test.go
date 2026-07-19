package services

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"go.uber.org/zap/zaptest"
)

func TestResolverMDNSHitShortCircuits(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "acme"}) // slug only
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("mDNS hit = %d,%v want 215,true", id, ok)
	}
}

func TestResolverIgnitesForeignOrgOnLAN(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 99, AssetID: 700, IsMTLS: true}, // other org
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if _, ok := r.Resolve("brave-dolphin"); ok {
		t.Fatal("must not resolve a same-named device from another org on the LAN")
	}
}

func TestResolverFallsBackToRoster(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{{Name: "calm-otter", AssetId: 216}},
	})
	browse := func(context.Context) ([]models.LANDevice, error) { return nil, nil } // LAN empty
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("calm-otter"); !ok || id != 216 {
		t.Fatalf("roster fallback = %d,%v want 216,true", id, ok)
	}
	if r.OrgSlug() != "acme" {
		t.Fatalf("OrgSlug = %q want acme", r.OrgSlug())
	}
}
