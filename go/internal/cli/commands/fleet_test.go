package commands

import (
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func TestValidateGroupName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"cameras", true},
		{"robots-east", true},
		{"cam_01", true},
		{"a", true},
		{"", false},
		{"-leading", false},
		{"has space", false},
		{"has,comma", false},
		{"emoji😀", false},
	}
	for _, tt := range tests {
		err := validateGroupName(tt.name)
		if tt.ok && err != nil {
			t.Errorf("validateGroupName(%q) = %v, want nil", tt.name, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("validateGroupName(%q) = nil, want error", tt.name)
		}
	}
}

func TestAddTag(t *testing.T) {
	got, changed := addTag([]string{"a", "b"}, "cameras")
	if !changed {
		t.Error("addTag: changed = false, want true")
	}
	if want := []string{"a", "b", "cameras"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addTag = %v, want %v", got, want)
	}

	got, changed = addTag([]string{"a", "cameras"}, "cameras")
	if changed {
		t.Error("addTag (already present): changed = true, want false")
	}
	if want := []string{"a", "cameras"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addTag (already present) = %v, want %v", got, want)
	}

	// Adding to an empty/nil tag set.
	got, changed = addTag(nil, "cameras")
	if !changed || !reflect.DeepEqual(got, []string{"cameras"}) {
		t.Errorf("addTag(nil) = %v, changed=%v", got, changed)
	}
}

func TestRemoveTag(t *testing.T) {
	got, changed := removeTag([]string{"a", "cameras", "b"}, "cameras")
	if !changed {
		t.Error("removeTag: changed = false, want true")
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("removeTag = %v, want %v", got, want)
	}

	got, changed = removeTag([]string{"a", "b"}, "cameras")
	if changed {
		t.Error("removeTag (absent): changed = true, want false")
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("removeTag (absent) = %v, want %v", got, want)
	}

	// Removing the only tag yields an empty (non-nil) slice.
	got, changed = removeTag([]string{"cameras"}, "cameras")
	if !changed {
		t.Error("removeTag (last): changed = false, want true")
	}
	if len(got) != 0 {
		t.Errorf("removeTag (last) = %v, want empty", got)
	}
}

func TestGroupCountsAndMembers(t *testing.T) {
	assets := []*cloudpb.Asset{
		{Id: 1, Name: "cam-01", Tags: []string{"cameras", "prod"}},
		{Id: 2, Name: "cam-02", Tags: []string{"cameras"}},
		{Id: 3, Name: "robot-01", Tags: []string{"robots"}},
		{Id: 4, Name: "spare", Tags: nil},
	}

	counts := groupCounts(assets)
	if counts["cameras"] != 2 || counts["robots"] != 1 || counts["prod"] != 1 {
		t.Errorf("groupCounts = %v", counts)
	}
	if _, ok := counts[""]; ok {
		t.Error("groupCounts should not contain an empty group")
	}

	members := assetsInGroup(assets, "cameras")
	if len(members) != 2 {
		t.Fatalf("assetsInGroup(cameras) = %d members, want 2", len(members))
	}
	if members[0].GetName() != "cam-01" || members[1].GetName() != "cam-02" {
		t.Errorf("assetsInGroup preserved order wrong: %q, %q", members[0].GetName(), members[1].GetName())
	}

	if got := assetsInGroup(assets, "nope"); len(got) != 0 {
		t.Errorf("assetsInGroup(nope) = %v, want empty", got)
	}
}
