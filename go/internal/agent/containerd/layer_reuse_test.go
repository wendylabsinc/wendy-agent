package containerd

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"go.uber.org/zap"
)

type reuseWalker struct {
	infos []snapshots.Info
}

type snapshotLabelStore struct {
	info    snapshots.Info
	updates int
}

func (s *snapshotLabelStore) Stat(_ context.Context, _ string) (snapshots.Info, error) {
	return s.info, nil
}

func (s *snapshotLabelStore) Update(_ context.Context, info snapshots.Info, _ ...string) (snapshots.Info, error) {
	s.info = info
	s.updates++
	return info, nil
}

func (w reuseWalker) Walk(ctx context.Context, fn snapshots.WalkFunc, _ ...string) error {
	for _, info := range w.infos {
		if err := fn(ctx, info); err != nil {
			return err
		}
	}
	return nil
}

func TestFindReusableLayerSnapshotUsesCommittedDiffIDMatch(t *testing.T) {
	diffID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	labels := func(id string) map[string]string {
		return map[string]string{labelKeyWendySnapshot: "true", labelKeyWendyDiffID: id}
	}
	w := reuseWalker{infos: []snapshots.Info{
		{Name: "active", Kind: snapshots.KindActive, Labels: labels(diffID)},
		{Name: "wrong-layer", Kind: snapshots.KindCommitted, Labels: labels("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
		{Name: "current-chain", Kind: snapshots.KindCommitted, Labels: labels(diffID)},
		{Name: "reusable-chain", Kind: snapshots.KindCommitted, Labels: labels(diffID)},
	}}

	got, err := findReusableLayerSnapshot(context.Background(), w, diffID, "current-chain")
	if err != nil {
		t.Fatalf("findReusableLayerSnapshot: %v", err)
	}
	if got != "reusable-chain" {
		t.Fatalf("candidate = %q, want reusable-chain", got)
	}
}

func TestFindReusableLayerSnapshotReturnsEmptyWithoutMatch(t *testing.T) {
	w := reuseWalker{infos: []snapshots.Info{
		{Name: "foreign", Kind: snapshots.KindCommitted},
	}}
	got, err := findReusableLayerSnapshot(context.Background(), w, "sha256:missing", "")
	if err != nil {
		t.Fatalf("findReusableLayerSnapshot: %v", err)
	}
	if got != "" {
		t.Fatalf("candidate = %q, want empty", got)
	}
}

func TestSnapshotUpperPathFromBindMount(t *testing.T) {
	got, ok := snapshotUpperPath([]mount.Mount{{Type: "bind", Source: "/var/lib/containerd/snapshots/7/fs"}})
	if !ok || got != "/var/lib/containerd/snapshots/7/fs" {
		t.Fatalf("snapshotUpperPath = (%q, %v)", got, ok)
	}
}

func TestSnapshotUpperPathFromOverlayView(t *testing.T) {
	got, ok := snapshotUpperPath([]mount.Mount{{
		Type: "overlay",
		Options: []string{
			"ro",
			"lowerdir=/var/lib/containerd/snapshots/9/fs:/var/lib/containerd/snapshots/4/fs",
		},
	}})
	if !ok || got != "/var/lib/containerd/snapshots/9/fs" {
		t.Fatalf("snapshotUpperPath = (%q, %v)", got, ok)
	}
}

func TestSnapshotUpperPathRejectsActiveOverlayMount(t *testing.T) {
	_, ok := snapshotUpperPath([]mount.Mount{{
		Type: "overlay",
		Options: []string{
			"lowerdir=/lower",
			"upperdir=/upper",
			"workdir=/work",
		},
	}})
	if ok {
		t.Fatal("active overlay mount must not be treated as an immutable layer source")
	}
}

func TestStandaloneLayerApplyMountsConvertsBindForWhiteouts(t *testing.T) {
	got := standaloneLayerApplyMounts([]mount.Mount{{
		Type: "bind", Source: "/snapshots/7/fs", Options: []string{"rw", "rbind", "nosuid"},
	}})
	if len(got) != 1 || got[0].Type != "overlay" || got[0].Source != "overlay" {
		t.Fatalf("standaloneLayerApplyMounts = %+v", got)
	}
	wantOptions := map[string]bool{"rw": true, "nosuid": true, "upperdir=/snapshots/7/fs": true}
	for _, opt := range got[0].Options {
		delete(wantOptions, opt)
		if opt == "rbind" {
			t.Fatalf("bind-only option survived conversion: %q", opt)
		}
	}
	if len(wantOptions) != 0 {
		t.Fatalf("missing options after conversion: %v", wantOptions)
	}
}

func TestRefreshSnapshotLabelsDoesNotGrandfatherLegacySnapshotForReuse(t *testing.T) {
	store := &snapshotLabelStore{info: snapshots.Info{
		Name: "legacy-chain",
		Labels: map[string]string{
			labelKeyWendySnapshot: "true",
		},
	}}
	c := &Client{logger: zap.NewNop()}
	c.refreshSnapshotLabels(context.Background(), store, "legacy-chain")

	if store.updates != 1 {
		t.Fatalf("updates = %d, want 1", store.updates)
	}
	if store.info.Labels[labelKeyGCRoot] == "" {
		t.Fatal("cache root timestamp was not refreshed")
	}
	if _, ok := store.info.Labels[labelKeyWendyDiffID]; ok {
		t.Fatal("legacy parent-bound snapshot was incorrectly marked parent-independent")
	}
}

func TestRefreshSnapshotLabelsLeavesForeignSnapshotAlone(t *testing.T) {
	store := &snapshotLabelStore{info: snapshots.Info{Name: "foreign"}}
	c := &Client{logger: zap.NewNop()}
	c.refreshSnapshotLabels(context.Background(), store, "foreign")
	if store.updates != 0 {
		t.Fatalf("updates = %d, want 0", store.updates)
	}
}
