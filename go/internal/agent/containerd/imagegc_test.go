package containerd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	"go.uber.org/zap"
)

// --- fakes for the mark-and-sweep orchestration (modeled on mockStatter) ---

type fakeImageStore struct {
	imgs []images.Image
	err  error
}

func (f *fakeImageStore) List(_ context.Context, _ ...string) ([]images.Image, error) {
	return f.imgs, f.err
}

type fakeSnapshotter struct {
	infos   map[string]snapshots.Info
	usage   map[string]int64
	removed []string
}

func (f *fakeSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, _ ...string) error {
	// Emulate the `labels."containerd.io/gc.root"` filter: yield only gc.root entries.
	for _, info := range f.infos {
		if _, ok := info.Labels[labelKeyGCRoot]; ok {
			if err := fn(ctx, info); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	info, ok := f.infos[key]
	if !ok {
		return snapshots.Info{}, errdefs.ErrNotFound
	}
	return info, nil
}

func (f *fakeSnapshotter) Usage(_ context.Context, key string) (snapshots.Usage, error) {
	return snapshots.Usage{Size: f.usage[key]}, nil
}

func (f *fakeSnapshotter) Remove(_ context.Context, key string) error {
	for _, info := range f.infos {
		if info.Parent == key {
			return errdefs.ErrFailedPrecondition // has-children backstop
		}
	}
	if _, ok := f.infos[key]; !ok {
		return errdefs.ErrNotFound
	}
	delete(f.infos, key)
	f.removed = append(f.removed, key)
	return nil
}

type fakeContent struct {
	infos   map[digest.Digest]content.Info
	deleted []digest.Digest
}

func (f *fakeContent) Walk(_ context.Context, fn content.WalkFunc, _ ...string) error {
	for _, info := range f.infos {
		if _, ok := info.Labels[labelKeyGCRoot]; ok {
			if err := fn(info); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeContent) Delete(_ context.Context, dgst digest.Digest) error {
	if _, ok := f.infos[dgst]; !ok {
		return errdefs.ErrNotFound
	}
	delete(f.infos, dgst)
	f.deleted = append(f.deleted, dgst)
	return nil
}

func gcLabel(ts string) map[string]string {
	return map[string]string{labelKeyGCRoot: ts}
}

func TestRunImageGC_ReclaimsOldVersionData(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	oldTS := now.Add(-time.Hour).UTC().Format(time.RFC3339) // older than grace

	const (
		c0     = "sha256:c0" // base layer of current image
		c1     = "sha256:c1" // top layer of current image
		oldA   = "sha256:oldA"
		oldB   = "sha256:oldB"
		active = "wendy-app" // running container's writable snapshot (no gc.root)
	)
	mD := digest.Digest("sha256:manifest")
	cfgD := digest.Digest("sha256:config")
	l0 := digest.Digest("sha256:layer0")
	l1 := digest.Digest("sha256:layer1")
	oldL := digest.Digest("sha256:oldlayer")

	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			c0:     {Name: c0, Labels: gcLabel(oldTS)},
			c1:     {Name: c1, Parent: c0, Labels: gcLabel(oldTS)},
			oldA:   {Name: oldA, Parent: c0, Labels: gcLabel(oldTS)},
			oldB:   {Name: oldB, Parent: oldA, Labels: gcLabel(oldTS)},
			active: {Name: active, Parent: c1}, // no gc.root label
		},
		usage: map[string]int64{oldA: 100, oldB: 200},
	}
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			l0:   {Digest: l0, Size: 10, Labels: gcLabel(oldTS)},
			l1:   {Digest: l1, Size: 20, Labels: gcLabel(oldTS)},
			oldL: {Digest: oldL, Size: 33, Labels: gcLabel(oldTS)},
		},
	}

	env := gcEnv{
		images:    &fakeImageStore{imgs: []images.Image{{Name: "localhost:5000/app:latest"}}},
		snapshots: sn,
		content:   cs,
		resolveImage: func(_ context.Context, _ images.Image) ([]string, []string, error) {
			return []string{c0, c1}, []string{mD.String(), cfgD.String(), l0.String(), l1.String()}, nil
		},
		containerSnapshotKeys: func(_ context.Context) ([]string, error) {
			return []string{active}, nil
		},
		now:    func() time.Time { return now },
		grace:  imageGCGracePeriod,
		logger: zap.NewNop(),
	}

	stats, err := runImageGC(context.Background(), env, true) // boot-style: full sweep
	if err != nil {
		t.Fatalf("runImageGC: %v", err)
	}
	if stats.SnapshotsRemoved != 2 || stats.SnapshotBytes != 300 {
		t.Errorf("snapshots removed=%d bytes=%d; want 2/300", stats.SnapshotsRemoved, stats.SnapshotBytes)
	}
	if stats.BlobsReclaimed != 1 || stats.BlobBytes != 33 {
		t.Errorf("blobs reclaimed=%d bytes=%d; want 1/33", stats.BlobsReclaimed, stats.BlobBytes)
	}
	if stats.Errors != 0 {
		t.Errorf("errors=%d; want 0", stats.Errors)
	}
	// Old version's data gone; current image + container layers kept.
	for _, k := range []string{oldA, oldB} {
		if _, ok := sn.infos[k]; ok {
			t.Errorf("snapshot %q should have been removed", k)
		}
	}
	for _, k := range []string{c0, c1, active} {
		if _, ok := sn.infos[k]; !ok {
			t.Errorf("snapshot %q must be kept", k)
		}
	}
	if _, ok := cs.infos[oldL]; ok {
		t.Error("blob oldL should have been deleted")
	}
	for _, d := range []digest.Digest{l0, l1} {
		if _, ok := cs.infos[d]; !ok {
			t.Errorf("blob %q must be kept", d)
		}
	}
}

func TestRunImageGC_SnapshotsOnlySkipsContent(t *testing.T) {
	// The per-deploy async path (includeContent=false) reclaims orphan snapshots
	// but must never touch content blobs — a concurrent deploy's just-pushed blob
	// is legitimately unreferenced until its image record is created.
	oldTS := "2020-01-01T00:00:00Z"
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphanSnap": {Name: "orphanSnap", Labels: gcLabel(oldTS)},
		},
		usage: map[string]int64{"orphanSnap": 50},
	}
	orphanBlob := digest.Digest("sha256:orphanblob")
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			orphanBlob: {Digest: orphanBlob, Size: 99, Labels: gcLabel(oldTS)},
		},
	}
	env := gcEnv{
		images:                &fakeImageStore{},
		snapshots:             sn,
		content:               cs,
		resolveImage:          func(_ context.Context, _ images.Image) ([]string, []string, error) { return nil, nil, nil },
		containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
		now:                   func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) },
		grace:                 imageGCGracePeriod,
		logger:                zap.NewNop(),
	}

	stats, err := runImageGC(context.Background(), env, false) // deploy-style: snapshots only
	if err != nil {
		t.Fatalf("runImageGC: %v", err)
	}
	if stats.SnapshotsRemoved != 1 {
		t.Errorf("snapshots removed=%d; want 1", stats.SnapshotsRemoved)
	}
	if stats.BlobsReclaimed != 0 {
		t.Errorf("blobs reclaimed=%d; want 0 (content sweep deferred to boot)", stats.BlobsReclaimed)
	}
	if _, ok := cs.infos[orphanBlob]; !ok {
		t.Error("content blob must be kept by the snapshots-only path")
	}
}

func TestRunImageGC_FailClosedOnResolveError(t *testing.T) {
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphan": {Name: "orphan", Labels: gcLabel("2020-01-01T00:00:00Z")},
		},
	}
	env := gcEnv{
		images:    &fakeImageStore{imgs: []images.Image{{Name: "app:latest"}}},
		snapshots: sn,
		content:   &fakeContent{infos: map[digest.Digest]content.Info{}},
		resolveImage: func(_ context.Context, _ images.Image) ([]string, []string, error) {
			return nil, nil, errors.New("boom")
		},
		containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
		now:                   func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) },
		grace:                 imageGCGracePeriod,
		logger:                zap.NewNop(),
	}
	stats, err := runImageGC(context.Background(), env, true)
	if err == nil {
		t.Fatal("expected error from fail-closed MARK")
	}
	if stats.SnapshotsRemoved != 0 {
		t.Errorf("removed=%d; want 0 deletions on fail-closed", stats.SnapshotsRemoved)
	}
	if _, ok := sn.infos["orphan"]; !ok {
		t.Error("no snapshot may be deleted when MARK fails")
	}
}

func TestSweep_HasChildrenBackstop(t *testing.T) {
	// p is selected as an orphan but still has a reachable child ch; Remove must
	// fail-precondition and be skipped, not counted as an error.
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"p":  {Name: "p", Labels: gcLabel("2020-01-01T00:00:00Z")},
			"ch": {Name: "ch", Parent: "p", Labels: gcLabel("2020-01-01T00:00:00Z")},
		},
		usage: map[string]int64{},
	}
	env := gcEnv{
		snapshots: sn,
		content:   &fakeContent{infos: map[digest.Digest]content.Info{}},
		now:       func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) },
		grace:     imageGCGracePeriod,
		logger:    zap.NewNop(),
	}
	// ch is reachable; p is not.
	rSnap := keySet("ch")
	stats, err := sweep(context.Background(), env, rSnap, keySet(), true)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.SnapshotsRemoved != 0 || stats.Errors != 0 {
		t.Errorf("removed=%d errors=%d; want 0/0 (benign skip)", stats.SnapshotsRemoved, stats.Errors)
	}
	if _, ok := sn.infos["p"]; !ok {
		t.Error("p must be kept: a snapshot with children is never removed")
	}
}

func TestGCSingleFlight(t *testing.T) {
	c := &Client{logger: zap.NewNop(), imageGCEnabled: true}
	if !c.tryStartGC() {
		t.Fatal("first acquire should win")
	}
	if c.tryStartGC() {
		t.Fatal("second acquire must fail while a pass is running")
	}
	c.finishGC()
	if !c.tryStartGC() {
		t.Fatal("acquire should win again after finish")
	}
	c.finishGC()
}

func TestGarbageCollectImages_DisabledNoOp(t *testing.T) {
	c := &Client{logger: zap.NewNop(), imageGCEnabled: false}
	stats, err := c.GarbageCollectImages(context.Background())
	if err != nil {
		t.Fatalf("GarbageCollectImages disabled: %v", err)
	}
	if stats != (GCStats{}) {
		t.Errorf("stats = %+v; want zero when disabled", stats)
	}
	// Disabled path must not have touched the (nil) containerd client.
}

// keySet builds a reachable-set (set of snapshot keys or digest strings) for tests.
func keySet(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

func names(infos []snapshots.Info) map[string]bool {
	out := make(map[string]bool, len(infos))
	for _, i := range infos {
		out[i.Name] = true
	}
	return out
}

func TestSelectOrphanSnapshots_AllReachable(t *testing.T) {
	gcRoots := []snapshots.Info{{Name: "a"}, {Name: "b"}}
	if got := selectOrphanSnapshots(gcRoots, keySet("a", "b")); len(got) != 0 {
		t.Fatalf("got %d orphans; want 0", len(got))
	}
}

func TestSelectOrphanSnapshots_OldVersionOrphaned(t *testing.T) {
	// a,b are the current image chain (reachable); c,d are an old version's
	// top layers that nothing references any more.
	gcRoots := []snapshots.Info{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	got := names(selectOrphanSnapshots(gcRoots, keySet("a", "b")))
	if len(got) != 2 || !got["c"] || !got["d"] {
		t.Fatalf("orphans = %v; want {c,d}", got)
	}
}

func TestSelectOrphanSnapshots_SharedBaseKept(t *testing.T) {
	// base is still referenced by a current image; only the old top layer is orphaned.
	gcRoots := []snapshots.Info{{Name: "base"}, {Name: "oldtop", Parent: "base"}}
	got := selectOrphanSnapshots(gcRoots, keySet("base"))
	if len(got) != 1 || got[0].Name != "oldtop" {
		t.Fatalf("orphans = %v; want {oldtop}", names(got))
	}
}

func TestOrderLeafFirst_Chain(t *testing.T) {
	// root <- mid <- leaf: removal must proceed leaf, mid, root so a parent is
	// never removed while a child still references it.
	orphans := []snapshots.Info{
		{Name: "root"},
		{Name: "mid", Parent: "root"},
		{Name: "leaf", Parent: "mid"},
	}
	got := orderLeafFirst(orphans)
	want := []string{"leaf", "mid", "root"}
	if len(got) != len(want) {
		t.Fatalf("order = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v; want %v", got, want)
		}
	}
}

func TestOrderLeafFirst_Branching(t *testing.T) {
	// root has two orphan children; root must be ordered after both.
	orphans := []snapshots.Info{
		{Name: "root"},
		{Name: "c1", Parent: "root"},
		{Name: "c2", Parent: "root"},
	}
	pos := map[string]int{}
	for i, k := range orderLeafFirst(orphans) {
		pos[k] = i
	}
	if pos["root"] < pos["c1"] || pos["root"] < pos["c2"] {
		t.Fatalf("root must come after its children: %v", pos)
	}
}

func TestSelectOrphanBlobs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour).UTC().Format(time.RFC3339)         // older than grace
	fresh := now.Add(-1 * time.Minute).UTC().Format(time.RFC3339) // within grace

	d := func(c string) digest.Digest { return digest.Digest("sha256:" + strings.Repeat(c, 64)) }
	dReach, dOld, dFresh, dBad := d("1"), d("2"), d("3"), d("4")

	infos := []content.Info{
		{Digest: dReach, Labels: map[string]string{labelKeyGCRoot: old}},   // reachable -> kept
		{Digest: dOld, Labels: map[string]string{labelKeyGCRoot: old}},     // orphan + old -> selected
		{Digest: dFresh, Labels: map[string]string{labelKeyGCRoot: fresh}}, // orphan + in-grace -> kept
		{Digest: dBad, Labels: map[string]string{labelKeyGCRoot: "nope"}},  // orphan + unparseable -> kept
	}

	got := selectOrphanBlobs(infos, keySet(dReach.String()), now, imageGCGracePeriod)
	if len(got) != 1 || got[0] != dOld {
		t.Fatalf("orphan blobs = %v; want {%s}", got, dOld)
	}
}
