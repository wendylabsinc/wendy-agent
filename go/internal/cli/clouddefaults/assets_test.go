package clouddefaults

import (
	"testing"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// cloudAssetFixture mirrors the fixture helper in
// commands/cloud_tunnel_test.go (unexported there, so duplicated here for
// this package's own tests).
func cloudAssetFixture(id int32, name string) *cloudpb.Asset {
	a := &cloudpb.Asset{Id: id}
	if name != "" {
		a.Name = name
	}
	return a
}

func TestFindAssetByNameOrID(t *testing.T) {
	assets := []*cloudpb.Asset{cloudAssetFixture(41, "playful-reed"), cloudAssetFixture(42, "")}

	t.Run("case-insensitive name match", func(t *testing.T) {
		got := FindAssetByNameOrID(assets, "Playful-Reed")
		if got == nil || got.GetId() != 41 {
			t.Fatalf("FindAssetByNameOrID(%q) = %v, want id 41", "Playful-Reed", got)
		}
	})

	t.Run("numeric id match", func(t *testing.T) {
		got := FindAssetByNameOrID(assets, "42")
		if got == nil || got.GetId() != 42 {
			t.Fatalf("FindAssetByNameOrID(%q) = %v, want id 42", "42", got)
		}
	})

	t.Run("miss returns nil", func(t *testing.T) {
		got := FindAssetByNameOrID(assets, "nope")
		if got != nil {
			t.Fatalf("FindAssetByNameOrID(%q) = %v, want nil", "nope", got)
		}
	})

	t.Run("empty needle does not match an asset with an empty name", func(t *testing.T) {
		got := FindAssetByNameOrID(assets, "")
		if got != nil {
			t.Fatalf("FindAssetByNameOrID(%q) = %v, want nil", "", got)
		}
	})
}
