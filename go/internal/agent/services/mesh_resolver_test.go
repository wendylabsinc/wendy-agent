package services

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"go.uber.org/zap/zaptest"
)

func TestResolverMDNSHitShortCircuits(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
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

func TestResolverIgnoresForeignOrgOnLAN(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
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
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
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

func TestResolverAmbiguousDifferentAssetIDsOnLAN(t *testing.T) {
	// Roster has a slug but no entry for "brave-dolphin", so if resolveLAN
	// were to (incorrectly) return not-found in a way that didn't actually
	// stem from ambiguity, the roster fallback would also miss and could
	// mask a bug where LAN ambiguity isn't actually being detected. This
	// test only passes if resolveLAN itself returns ok=false due to the
	// two different asset ids, not by coincidence of an empty roster.
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "acme"}) // no entries
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 216, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if _, ok := r.Resolve("brave-dolphin"); ok {
		t.Fatal("two LAN devices with the same name but different asset ids must be ambiguous (ok=false)")
	}
}

func TestResolverSameAssetIDRepeatedNotAmbiguous(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "acme"}) // no entries
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("repeated identical asset id = %d,%v want 215,true", id, ok)
	}
}

func TestResolverUpdateOwnOrgIDReachesFilter(t *testing.T) {
	// Simulates BLE first-boot enrollment: the resolver is constructed with
	// the boot-time snapshot ownOrgID=0, so a LAN peer belonging to the
	// device's real org (42) is initially rejected as "foreign". After
	// UpdateOwnOrgID(42) — what OnProvisioned now calls — the same peer must
	// resolve, proving the identity refresh actually reaches resolveLAN's
	// filter rather than being shadowed by a stale copy.
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 0, 0, "", "", "")
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 0, roster, browse)
	if _, ok := r.Resolve("brave-dolphin"); ok {
		t.Fatal("with ownOrgID=0 (unprovisioned), a peer in org 42 must not resolve")
	}
	r.UpdateOwnOrgID(42)
	if id, ok := r.Resolve("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("after UpdateOwnOrgID(42), Resolve = %d,%v want 215,true", id, ok)
	}
}

func TestResolverBrowseErrorFallsBackToRoster(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", 42, 1, "", "", "")
	roster.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{{Name: "calm-otter", AssetId: 216}},
	})
	browse := func(context.Context) ([]models.LANDevice, error) {
		return nil, errors.New("boom")
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if id, ok := r.Resolve("calm-otter"); !ok || id != 216 {
		t.Fatalf("browse error should degrade to roster fallback = %d,%v want 216,true", id, ok)
	}
}
