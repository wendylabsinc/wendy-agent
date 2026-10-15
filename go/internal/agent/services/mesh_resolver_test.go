package services

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"go.uber.org/zap/zaptest"
)

const (
	resolverTestAsset  = "33333333-3333-4333-8333-333333333333"
	resolverTestDevice = "22222222-2222-4222-8222-222222222222"
)

func TestResolverMDNSHitShortCircuits(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
	roster.applyResponse(&cloudpbv2.GetMeshRosterResponse{OrgSlug: "acme"}) // slug only
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if got, ok := r.Resolve("brave-dolphin"); !ok || got.AssetID != 215 {
		t.Fatalf("mDNS hit = %+v,%v want AssetID 215,true", got, ok)
	}
}

func TestResolverIgnoresForeignOrgOnLAN(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
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

// TestResolverFallsBackToRosterWithV2UUIDs proves the off-LAN half hands
// through the Cloud v2 UUIDs unchanged. A roster entry carries no legacy asset
// id, so the resolved device has no VIP to answer — the mesh DNS server turns
// that into NXDOMAIN until the mesh data plane carries v2 addresses.
func TestResolverFallsBackToRosterWithV2UUIDs(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
	roster.applyResponse(&cloudpbv2.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpbv2.MeshRosterEntry{{Name: "calm-otter", AssetId: resolverTestAsset, DeviceUuid: resolverTestDevice}},
	})
	browse := func(context.Context) ([]models.LANDevice, error) { return nil, nil } // LAN empty
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	got, ok := r.Resolve("calm-otter")
	if !ok {
		t.Fatal("roster fallback must resolve")
	}
	if got.DeviceUUID != resolverTestDevice || got.AssetUUID != resolverTestAsset {
		t.Fatalf("roster fallback = %+v, want device %s asset %s", got, resolverTestDevice, resolverTestAsset)
	}
	if got.AssetID != 0 {
		t.Fatalf("roster fallback invented a legacy asset id: %d", got.AssetID)
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
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
	roster.applyResponse(&cloudpbv2.GetMeshRosterResponse{OrgSlug: "acme"}) // no entries
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
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
	roster.applyResponse(&cloudpbv2.GetMeshRosterResponse{OrgSlug: "acme"}) // no entries
	browse := func(context.Context) ([]models.LANDevice, error) {
		return []models.LANDevice{
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
			{MeshName: "brave-dolphin", OrgID: 42, AssetID: 215, IsMTLS: true},
		}, nil
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	if got, ok := r.Resolve("brave-dolphin"); !ok || got.AssetID != 215 {
		t.Fatalf("repeated identical asset id = %+v,%v want AssetID 215,true", got, ok)
	}
}

func TestResolverUpdateOwnOrgIDReachesFilter(t *testing.T) {
	// Simulates BLE first-boot enrollment: the resolver is constructed with
	// the boot-time snapshot ownOrgID=0, so a LAN peer belonging to the
	// device's real org (42) is initially rejected as "foreign". After
	// UpdateOwnOrgID(42) — what OnProvisioned now calls — the same peer must
	// resolve, proving the identity refresh actually reaches resolveLAN's
	// filter rather than being shadowed by a stale copy.
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
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
	if got, ok := r.Resolve("brave-dolphin"); !ok || got.AssetID != 215 {
		t.Fatalf("after UpdateOwnOrgID(42), Resolve = %+v,%v want AssetID 215,true", got, ok)
	}
}

func TestResolverBrowseErrorFallsBackToRoster(t *testing.T) {
	roster := NewMeshRoster(zaptest.NewLogger(t), "", "", "", "")
	roster.applyResponse(&cloudpbv2.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpbv2.MeshRosterEntry{{Name: "calm-otter", AssetId: resolverTestAsset}},
	})
	browse := func(context.Context) ([]models.LANDevice, error) {
		return nil, errors.New("boom")
	}
	r := NewMeshResolver(zaptest.NewLogger(t), 42, roster, browse)
	got, ok := r.Resolve("calm-otter")
	if !ok || got.AssetUUID != resolverTestAsset {
		t.Fatalf("browse error should degrade to roster fallback = %+v,%v want asset %s,true", got, ok, resolverTestAsset)
	}
}
