package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func TestParseTunnelArg(t *testing.T) {
	tests := []struct {
		arg        string
		wantLocal  uint32
		wantRemote uint32
		wantErr    bool
	}{
		{"8080", 8080, 8080, false},
		{"3000:8080", 3000, 8080, false},
		{"0", 0, 0, true},
		{"99999", 0, 0, true},
		{"abc", 0, 0, true},
		{"8080:abc", 0, 0, true},
		{"65535", 65535, 65535, false},
		{"1:65535", 1, 65535, false},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			local, remote, err := parseTunnelArg(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTunnelArg(%q) expected error, got none", tt.arg)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTunnelArg(%q) unexpected error: %v", tt.arg, err)
			}
			if local != tt.wantLocal || remote != tt.wantRemote {
				t.Errorf("parseTunnelArg(%q) = (%d, %d), want (%d, %d)", tt.arg, local, remote, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}

func cloudAssetFixture(id int32, name string) *cloudpb.Asset {
	a := &cloudpb.Asset{Id: id}
	if name != "" {
		a.Name = name
	}
	return a
}

func TestResolveCloudAssetByNameAndID(t *testing.T) {
	assets := []*cloudpb.Asset{cloudAssetFixture(41, "playful-reed"), cloudAssetFixture(42, "")}
	got, err := resolveCloudAsset(assets, "playful-reed")
	if err != nil || got.GetId() != 41 {
		t.Fatalf("by name: got %v, err %v", got, err)
	}
	got, err = resolveCloudAsset(assets, "42")
	if err != nil || got.GetId() != 42 {
		t.Fatalf("by id: got %v, err %v", got, err)
	}
	if _, err = resolveCloudAsset(assets, "nope"); err == nil {
		t.Fatal("expected error for unknown device")
	}
}

func TestResolveCloudAssetAmbiguousListsIDs(t *testing.T) {
	assets := []*cloudpb.Asset{cloudAssetFixture(41, "a"), cloudAssetFixture(42, "b")}
	_, err := resolveCloudAsset(assets, "")
	if err == nil {
		t.Fatal("expected ambiguity error with no --device")
	}
	if !strings.Contains(err.Error(), "41") || !strings.Contains(err.Error(), "42") {
		t.Fatalf("error should list candidate IDs, got: %v", err)
	}
}
