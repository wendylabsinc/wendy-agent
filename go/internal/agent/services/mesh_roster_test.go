package services

import (
	"testing"

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
