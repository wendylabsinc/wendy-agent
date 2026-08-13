package commands

import (
	"errors"
	"fmt"
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

func TestResolveCloudAssetNotFoundIsTyped(t *testing.T) {
	t.Run("miss among non-empty assets", func(t *testing.T) {
		assets := []*cloudpb.Asset{cloudAssetFixture(41, "playful-reed")}
		_, err := resolveCloudAsset(assets, "nope")
		var notFound *errCloudDeviceNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("expected errCloudDeviceNotFound, got %T: %v", err, err)
		}
		wantMsg := `no device named or with id "nope" found; run 'wendy cloud discover --json' to list ids`
		if err.Error() != wantMsg {
			t.Fatalf("Error() = %q, want %q", err.Error(), wantMsg)
		}
	})

	t.Run("empty list with a device name", func(t *testing.T) {
		_, err := resolveCloudAsset(nil, "playful-reed")
		var notFound *errCloudDeviceNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("expected errCloudDeviceNotFound, got %T: %v", err, err)
		}
		wantMsg := `no device named or with id "playful-reed" found; run 'wendy cloud discover --json' to list ids`
		if err.Error() != wantMsg {
			t.Fatalf("Error() = %q, want %q", err.Error(), wantMsg)
		}
	})

	t.Run("empty list without a device name stays untyped", func(t *testing.T) {
		_, err := resolveCloudAsset(nil, "")
		var notFound *errCloudDeviceNotFound
		if errors.As(err, &notFound) {
			t.Fatalf("unnamed empty-list case should NOT be typed as errCloudDeviceNotFound, got %v", err)
		}
		wantMsg := "no enrolled devices found for this org; enroll a device with 'wendy device enroll' first"
		if err.Error() != wantMsg {
			t.Fatalf("Error() = %q, want %q", err.Error(), wantMsg)
		}
	})
}

// fetchAllStub is an injectable stand-in for the real
// fetchCloudAssetsFiltered(ctx, auth, false) call, so
// TestUpgradeOfflineResolveErr can run as a pure function test with no
// network access. called records whether upgradeOfflineResolveErr actually
// invoked it (used to assert passthrough cases short-circuit before the
// offline re-query).
type fetchAllStub struct {
	assets []*cloudpb.Asset
	err    error
	called bool
}

func (s *fetchAllStub) fetch() ([]*cloudpb.Asset, error) {
	s.called = true
	return s.assets, s.err
}

func TestUpgradeOfflineResolveErr(t *testing.T) {
	t.Run("offline hit upgrades the message", func(t *testing.T) {
		resolveErr := &errCloudDeviceNotFound{name: "playful-reed"}
		stub := &fetchAllStub{assets: []*cloudpb.Asset{cloudAssetFixture(41, "playful-reed")}}

		got := upgradeOfflineResolveErr(resolveErr, "playful-reed", stub.fetch)

		if !stub.called {
			t.Fatal("expected fetchAll to be called")
		}
		if !strings.Contains(got.Error(), "enrolled but currently reported offline") {
			t.Fatalf("got %q, want it to mention being enrolled but offline", got.Error())
		}
	})

	t.Run("truly missing device keeps the original error", func(t *testing.T) {
		resolveErr := &errCloudDeviceNotFound{name: "playful-reed"}
		stub := &fetchAllStub{assets: []*cloudpb.Asset{cloudAssetFixture(41, "other-device")}}

		got := upgradeOfflineResolveErr(resolveErr, "playful-reed", stub.fetch)

		if !stub.called {
			t.Fatal("expected fetchAll to be called")
		}
		if got != error(resolveErr) {
			t.Fatalf("got %v, want the original resolveErr unchanged", got)
		}
	})

	t.Run("fetchAll error keeps the original error", func(t *testing.T) {
		resolveErr := &errCloudDeviceNotFound{name: "playful-reed"}
		stub := &fetchAllStub{err: fmt.Errorf("network down")}

		got := upgradeOfflineResolveErr(resolveErr, "playful-reed", stub.fetch)

		if !stub.called {
			t.Fatal("expected fetchAll to be called")
		}
		if got != error(resolveErr) {
			t.Fatalf("got %v, want the original resolveErr unchanged", got)
		}
	})

	t.Run("ambiguity error passes through without calling fetchAll", func(t *testing.T) {
		resolveErr := fmt.Errorf("multiple cloud devices found; rerun with --device <id|name> (41=a, 42=b)")
		stub := &fetchAllStub{assets: []*cloudpb.Asset{cloudAssetFixture(41, "a"), cloudAssetFixture(42, "b")}}

		got := upgradeOfflineResolveErr(resolveErr, "", stub.fetch)

		if stub.called {
			t.Fatal("expected fetchAll NOT to be called for an ambiguity error")
		}
		if got != resolveErr {
			t.Fatalf("got %v, want the original resolveErr unchanged", got)
		}
	})

	t.Run("unnamed empty-list bonus: all enrolled devices offline", func(t *testing.T) {
		resolveErr := errNoCloudDevicesEnrolled
		stub := &fetchAllStub{assets: []*cloudpb.Asset{cloudAssetFixture(41, "a"), cloudAssetFixture(42, "b"), cloudAssetFixture(43, "c")}}

		got := upgradeOfflineResolveErr(resolveErr, "", stub.fetch)

		if !stub.called {
			t.Fatal("expected fetchAll to be called")
		}
		if !strings.Contains(got.Error(), "all 3 enrolled devices are currently reported offline") {
			t.Fatalf("got %q, want it to mention all 3 enrolled devices being offline", got.Error())
		}
	})
}
