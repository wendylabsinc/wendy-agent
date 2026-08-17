package containerd

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/snapshots"
	digest "github.com/opencontainers/go-digest"
)

type fakeCacheContentStore struct {
	infos   []content.Info
	updates []content.Info
}

func (f *fakeCacheContentStore) Walk(_ context.Context, fn content.WalkFunc, _ ...string) error {
	for _, info := range f.infos {
		if err := fn(info); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCacheContentStore) Update(_ context.Context, info content.Info, _ ...string) (content.Info, error) {
	f.updates = append(f.updates, info)
	return info, nil
}

type fakeCacheSnapshotter struct {
	infos   []snapshots.Info
	usage   map[string]snapshots.Usage
	updates []snapshots.Info
}

func (f *fakeCacheSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, _ ...string) error {
	for _, info := range f.infos {
		if err := fn(ctx, info); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCacheSnapshotter) Update(_ context.Context, info snapshots.Info, _ ...string) (snapshots.Info, error) {
	f.updates = append(f.updates, info)
	return info, nil
}

func (f *fakeCacheSnapshotter) Usage(_ context.Context, key string) (snapshots.Usage, error) {
	return f.usage[key], nil
}

func TestPruneCacheRootsDryRunSelectsOnlyOldWendyCache(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-10 * time.Minute).Format(time.RFC3339)
	legacySnapshot := digest.FromString("legacy-snapshot").String()

	cs := &fakeCacheContentStore{infos: []content.Info{
		{Digest: digest.FromString("old"), Size: 100, Labels: map[string]string{labelKeyWendyLayer: "true", labelKeyGCRoot: old}},
		{Digest: digest.FromString("recent"), Size: 200, Labels: map[string]string{labelKeyWendyLayer: "true", labelKeyGCRoot: recent}},
		{Digest: digest.FromString("foreign"), Size: 300, Labels: map[string]string{labelKeyGCRoot: old}},
	}}
	sn := &fakeCacheSnapshotter{
		infos: []snapshots.Info{
			{Name: "wendy", Kind: snapshots.KindCommitted, Labels: map[string]string{labelKeyWendySnapshot: "true", labelKeyGCRoot: old}},
			{Name: legacySnapshot, Kind: snapshots.KindCommitted, Labels: map[string]string{labelKeyGCRoot: old}},
			{Name: "recent", Kind: snapshots.KindCommitted, Labels: map[string]string{labelKeyWendySnapshot: "true", labelKeyGCRoot: recent}},
			{Name: "foreign", Kind: snapshots.KindCommitted, Labels: map[string]string{labelKeyGCRoot: old}},
			{Name: "active", Kind: snapshots.KindActive, Labels: map[string]string{labelKeyWendySnapshot: "true", labelKeyGCRoot: old}},
		},
		usage: map[string]snapshots.Usage{"wendy": {Size: 400}, legacySnapshot: {Size: 500}},
	}

	got, err := pruneCacheRoots(context.Background(), cs, sn, now.Add(-time.Hour), true)
	if err != nil {
		t.Fatalf("pruneCacheRoots: %v", err)
	}
	if got.ContentBlobs != 1 || got.ContentBytes != 100 || got.Snapshots != 2 || got.SnapshotBytes != 900 {
		t.Fatalf("result = %+v", got)
	}
	if len(cs.updates) != 0 || len(sn.updates) != 0 {
		t.Fatalf("dry run mutated stores: content=%d snapshots=%d", len(cs.updates), len(sn.updates))
	}
}

func TestPruneCacheRootsRemovesOnlyGCRootLabel(t *testing.T) {
	old := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cs := &fakeCacheContentStore{infos: []content.Info{{
		Digest: digest.FromString("layer"), Size: 12,
		Labels: map[string]string{labelKeyWendyLayer: "true", labelKeyGCRoot: old, "keep": "content"},
	}}}
	sn := &fakeCacheSnapshotter{
		infos: []snapshots.Info{{
			Name: "snapshot", Kind: snapshots.KindCommitted,
			Labels: map[string]string{labelKeyWendySnapshot: "true", labelKeyGCRoot: old, "keep": "snapshot"},
		}},
		usage: map[string]snapshots.Usage{"snapshot": {Size: 34}},
	}

	_, err := pruneCacheRoots(context.Background(), cs, sn, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatalf("pruneCacheRoots: %v", err)
	}
	if len(cs.updates) != 1 || cs.updates[0].Labels["keep"] != "content" {
		t.Fatalf("content update = %+v", cs.updates)
	}
	if _, ok := cs.updates[0].Labels[labelKeyGCRoot]; ok {
		t.Fatal("content GC root was not removed")
	}
	if len(sn.updates) != 1 || sn.updates[0].Labels["keep"] != "snapshot" {
		t.Fatalf("snapshot update = %+v", sn.updates)
	}
	if _, ok := sn.updates[0].Labels[labelKeyGCRoot]; ok {
		t.Fatal("snapshot GC root was not removed")
	}
}

func TestCacheRootOlderThanRejectsInvalidTimestamp(t *testing.T) {
	if cacheRootOlderThan("not-a-time", time.Now()) {
		t.Fatal("invalid timestamp must fail safe")
	}
}
