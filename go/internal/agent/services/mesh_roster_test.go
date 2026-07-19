package services

import (
	"context"
	"testing"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"go.uber.org/zap/zaptest"
)

func newTestRoster(t *testing.T) *MeshRoster {
	return NewMeshRoster(zaptest.NewLogger(t), "cloud.example:443", 42, 215, "")
}

func TestRosterLookupNormalizes(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{
			{Name: "Brave Dolphin", AssetId: 215},
			{Name: "calm-otter", AssetId: 216},
		},
	})
	if r.OrgSlug() != "acme" {
		t.Fatalf("OrgSlug = %q, want acme", r.OrgSlug())
	}
	if id, ok := r.Lookup("brave-dolphin"); !ok || id != 215 {
		t.Fatalf("Lookup(brave-dolphin) = %d,%v want 215,true", id, ok)
	}
	if id, ok := r.Lookup("calm-otter"); !ok || id != 216 {
		t.Fatalf("Lookup(calm-otter) = %d,%v want 216,true", id, ok)
	}
	if _, ok := r.Lookup("ghost"); ok {
		t.Fatal("Lookup(ghost) should be false")
	}
}

func TestRosterDuplicateNameIsAmbiguous(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{
		OrgSlug: "acme",
		Entries: []*cloudpb.MeshRosterEntry{
			{Name: "Brave Dolphin", AssetId: 215},
			{Name: "brave dolphin", AssetId: 999}, // normalizes to the same label
		},
	})
	if _, ok := r.Lookup("brave-dolphin"); ok {
		t.Fatal("duplicate normalized name must resolve to ok=false")
	}
}

func TestRosterSlugNormalized(t *testing.T) {
	r := newTestRoster(t)
	r.applyResponse(&cloudpb.GetMeshRosterResponse{OrgSlug: "ACME Corp."})
	if r.OrgSlug() != "acme-corp" {
		t.Fatalf("OrgSlug = %q, want acme-corp", r.OrgSlug())
	}
}

// TestRosterSyncSkipsUnprovisionedIdentity proves Sync fails closed without
// attempting a dial when assetID==0 (the boot-time snapshot before BLE
// first-boot enrollment completes): a real dial would hang/error against the
// bogus "cloud.invalid:1" address, but Sync must return nil quickly instead
// because there's no asset cert to authenticate an RPC with.
func TestRosterSyncSkipsUnprovisionedIdentity(t *testing.T) {
	r := NewMeshRoster(zaptest.NewLogger(t), "cloud.invalid:1", 0, 0, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Sync(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sync() with assetID=0 = %v, want nil (no-dial skip)", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Sync() with assetID=0 should return immediately without dialing")
	}
}

// TestRosterUpdateIdentityIsUsedBySync proves UpdateIdentity's write is what
// a subsequent Sync reads: after moving from assetID=0 (skip branch) to a
// real assetID via UpdateIdentity, Sync no longer takes the skip path (it
// proceeds to a real dial attempt against an unreachable address instead of
// returning nil immediately).
func TestRosterUpdateIdentityIsUsedBySync(t *testing.T) {
	r := NewMeshRoster(zaptest.NewLogger(t), "127.0.0.1:1", 0, 0, "")
	r.UpdateIdentity("127.0.0.1:1", 7, 215, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.Sync(ctx)
	// A real dial is attempted now (assetID=215 != 0), so this must NOT be
	// the nil the skip-branch would return; brokerDialOpts/dial/RPC against
	// an unreachable loopback port fails, confirming the identity swap took
	// effect and Sync proceeded past the assetID==0 guard.
	if err == nil {
		t.Fatal("Sync() after UpdateIdentity to a real assetID should attempt a dial and fail against an unreachable address, not silently skip")
	}
}
