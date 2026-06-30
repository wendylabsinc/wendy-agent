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

// testGrace is the orphan-age grace used throughout these tests (the production
// default). Tombstones older than this are reapable; younger ones are kept.
const testGrace = 24 * time.Hour

// --- fakes for the mark-and-sweep orchestration (modeled on mockStatter) ---

type fakeImageStore struct {
	imgs []images.Image
	err  error
}

func (f *fakeImageStore) List(_ context.Context, _ ...string) ([]images.Image, error) {
	return f.imgs, f.err
}

type fakeSnapshotter struct {
	infos     map[string]snapshots.Info
	usage     map[string]int64
	removed   []string
	updated   []string
	updateErr error
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

// Update applies each "labels.<key>" fieldpath to the stored Info, reproducing
// containerd's metadata semantics: an empty value deletes the label, a non-empty
// value sets it, and labels outside the named fieldpaths are left untouched (so
// gc.root survives an orphanedAt write).
func (f *fakeSnapshotter) Update(_ context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	if f.updateErr != nil {
		return snapshots.Info{}, f.updateErr
	}
	cur, ok := f.infos[info.Name]
	if !ok {
		return snapshots.Info{}, errdefs.ErrNotFound
	}
	if cur.Labels == nil {
		cur.Labels = map[string]string{}
	}
	applyLabelFieldpaths(cur.Labels, info.Labels, fieldpaths)
	f.infos[info.Name] = cur
	f.updated = append(f.updated, info.Name)
	return cur, nil
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
	infos     map[digest.Digest]content.Info
	deleted   []digest.Digest
	updated   []digest.Digest
	updateErr error
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

func (f *fakeContent) Update(_ context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	if f.updateErr != nil {
		return content.Info{}, f.updateErr
	}
	cur, ok := f.infos[info.Digest]
	if !ok {
		return content.Info{}, errdefs.ErrNotFound
	}
	if cur.Labels == nil {
		cur.Labels = map[string]string{}
	}
	applyLabelFieldpaths(cur.Labels, info.Labels, fieldpaths)
	f.infos[info.Digest] = cur
	f.updated = append(f.updated, info.Digest)
	return cur, nil
}

func (f *fakeContent) Delete(_ context.Context, dgst digest.Digest) error {
	if _, ok := f.infos[dgst]; !ok {
		return errdefs.ErrNotFound
	}
	delete(f.infos, dgst)
	f.deleted = append(f.deleted, dgst)
	return nil
}

// applyLabelFieldpaths mutates dst per the "labels.<key>" fieldpaths, mirroring
// containerd's boltutil.writeMap (empty value -> delete).
func applyLabelFieldpaths(dst, src map[string]string, fieldpaths []string) {
	for _, p := range fieldpaths {
		key, found := strings.CutPrefix(p, "labels.")
		if !found {
			continue
		}
		if v := src[key]; v == "" {
			delete(dst, key)
		} else {
			dst[key] = v
		}
	}
}

// gcRootLabels marks an artifact gc.root so the fake Walk yields it. The value is
// irrelevant to the tombstone GC (only presence matters) and never the age basis.
func gcRootLabels() map[string]string {
	return map[string]string{labelKeyGCRoot: "x"}
}

// tombstone marks an artifact gc.root AND stamps its orphanedAt at ts — i.e. an
// artifact the GC has already observed orphaned.
func tombstone(ts time.Time) map[string]string {
	return map[string]string{
		labelKeyGCRoot:     "x",
		labelKeyOrphanedAt: ts.UTC().Format(time.RFC3339),
	}
}

func TestRunImageGC_ReclaimsAgedOrphans(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour) // orphaned well past grace

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
			c0:     {Name: c0, Labels: gcRootLabels()},
			c1:     {Name: c1, Parent: c0, Labels: gcRootLabels()},
			oldA:   {Name: oldA, Parent: c0, Labels: tombstone(aged)},
			oldB:   {Name: oldB, Parent: oldA, Labels: tombstone(aged)},
			active: {Name: active, Parent: c1}, // no gc.root label
		},
		usage: map[string]int64{oldA: 100, oldB: 200},
	}
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			l0:   {Digest: l0, Size: 10, Labels: gcRootLabels()},
			l1:   {Digest: l1, Size: 20, Labels: gcRootLabels()},
			oldL: {Digest: oldL, Size: 33, Labels: tombstone(aged)},
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
		grace:  testGrace,
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
	if stats.SnapshotsTombstoned != 0 || stats.BlobsTombstoned != 0 || stats.StampsCleared != 0 {
		t.Errorf("unexpected tombstone activity: %+v", stats)
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

func TestRunImageGC_FirstSightingStampsNotReaps(t *testing.T) {
	// An orphan with an ANCIENT gc.root commit time but no orphanedAt tombstone
	// must be stamped (not reaped) on first sighting — this is the core fix: the
	// grace clock starts when a layer becomes orphaned, never from its commit time.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			// gc.root value is years old, but there is no orphanedAt label.
			"orphan": {Name: "orphan", Labels: map[string]string{labelKeyGCRoot: "2020-01-01T00:00:00Z"}},
		},
		usage: map[string]int64{"orphan": 500},
	}
	env := gcEnv{
		images:                &fakeImageStore{},
		snapshots:             sn,
		content:               &fakeContent{infos: map[digest.Digest]content.Info{}},
		resolveImage:          func(_ context.Context, _ images.Image) ([]string, []string, error) { return nil, nil, nil },
		containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
		now:                   func() time.Time { return now },
		grace:                 testGrace,
		logger:                zap.NewNop(),
	}

	stats, err := runImageGC(context.Background(), env, false)
	if err != nil {
		t.Fatalf("runImageGC: %v", err)
	}
	if stats.SnapshotsTombstoned != 1 || stats.SnapshotsRemoved != 0 {
		t.Errorf("tombstoned=%d removed=%d; want 1/0 (stamp, never reap on first sighting)",
			stats.SnapshotsTombstoned, stats.SnapshotsRemoved)
	}
	got := sn.infos["orphan"].Labels[labelKeyOrphanedAt]
	if want := now.UTC().Format(time.RFC3339); got != want {
		t.Errorf("orphanedAt=%q; want %q", got, want)
	}
}

func TestRunImageGC_ClearOnReachable(t *testing.T) {
	// A snapshot that is reachable again (re-adopted by a deploy) but still carries
	// a stale orphanedAt must have that tombstone cleared and must not be reaped —
	// this resets the retention clock so the layer is never re-uploaded.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	stale := now.Add(-72 * time.Hour) // well past grace, yet reachable -> must clear
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"base": {Name: "base", Labels: tombstone(stale)},
		},
	}
	env := gcEnv{
		images:    &fakeImageStore{imgs: []images.Image{{Name: "app:latest"}}},
		snapshots: sn,
		content:   &fakeContent{infos: map[digest.Digest]content.Info{}},
		resolveImage: func(_ context.Context, _ images.Image) ([]string, []string, error) {
			return []string{"base"}, nil, nil
		},
		containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
		now:                   func() time.Time { return now },
		grace:                 testGrace,
		logger:                zap.NewNop(),
	}

	stats, err := runImageGC(context.Background(), env, false)
	if err != nil {
		t.Fatalf("runImageGC: %v", err)
	}
	if stats.StampsCleared != 1 || stats.SnapshotsRemoved != 0 {
		t.Errorf("cleared=%d removed=%d; want 1/0", stats.StampsCleared, stats.SnapshotsRemoved)
	}
	if _, ok := sn.infos["base"].Labels[labelKeyOrphanedAt]; ok {
		t.Error("orphanedAt must be cleared on a reachable snapshot")
	}
	if _, ok := sn.infos["base"].Labels[labelKeyGCRoot]; !ok {
		t.Error("clearing orphanedAt must preserve the gc.root pin")
	}
}

func TestRunImageGC_TwoBootsContent(t *testing.T) {
	// Content reclamation takes two boots: the first stamps the orphan blob, a
	// later boot past grace reaps it. The fake carries the label between passes.
	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	orphan := digest.Digest("sha256:orphanblob")
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			orphan: {Digest: orphan, Size: 77, Labels: gcRootLabels()},
		},
	}
	newEnv := func(now time.Time) gcEnv {
		return gcEnv{
			images:                &fakeImageStore{},
			snapshots:             &fakeSnapshotter{infos: map[string]snapshots.Info{}},
			content:               cs,
			resolveImage:          func(_ context.Context, _ images.Image) ([]string, []string, error) { return nil, nil, nil },
			containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
			now:                   func() time.Time { return now },
			grace:                 testGrace,
			logger:                zap.NewNop(),
		}
	}

	// Boot 1: stamp only.
	stats, err := runImageGC(context.Background(), newEnv(t0), true)
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if stats.BlobsTombstoned != 1 || stats.BlobsReclaimed != 0 {
		t.Fatalf("boot 1 tombstoned=%d reclaimed=%d; want 1/0", stats.BlobsTombstoned, stats.BlobsReclaimed)
	}
	if _, ok := cs.infos[orphan]; !ok {
		t.Fatal("boot 1 must not reclaim content")
	}

	// Boot 2, past grace: reap.
	stats, err = runImageGC(context.Background(), newEnv(t0.Add(48*time.Hour)), true)
	if err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	if stats.BlobsReclaimed != 1 || stats.BlobBytes != 77 {
		t.Errorf("boot 2 reclaimed=%d bytes=%d; want 1/77", stats.BlobsReclaimed, stats.BlobBytes)
	}
	if _, ok := cs.infos[orphan]; ok {
		t.Error("boot 2 must reclaim the aged orphan blob")
	}
}

func TestRunImageGC_SnapshotsOnlySkipsContent(t *testing.T) {
	// The per-deploy async path (includeContent=false) reaps aged orphan snapshots
	// but must never touch content blobs — not even to tombstone them — because a
	// concurrent deploy's just-pushed blob is legitimately unreferenced until its
	// image record is created.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour)
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphanSnap": {Name: "orphanSnap", Labels: tombstone(aged)},
		},
		usage: map[string]int64{"orphanSnap": 50},
	}
	orphanBlob := digest.Digest("sha256:orphanblob")
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			orphanBlob: {Digest: orphanBlob, Size: 99, Labels: gcRootLabels()},
		},
	}
	env := gcEnv{
		images:                &fakeImageStore{},
		snapshots:             sn,
		content:               cs,
		resolveImage:          func(_ context.Context, _ images.Image) ([]string, []string, error) { return nil, nil, nil },
		containerSnapshotKeys: func(_ context.Context) ([]string, error) { return nil, nil },
		now:                   func() time.Time { return now },
		grace:                 testGrace,
		logger:                zap.NewNop(),
	}

	stats, err := runImageGC(context.Background(), env, false) // deploy-style: snapshots only
	if err != nil {
		t.Fatalf("runImageGC: %v", err)
	}
	if stats.SnapshotsRemoved != 1 {
		t.Errorf("snapshots removed=%d; want 1", stats.SnapshotsRemoved)
	}
	if stats.BlobsReclaimed != 0 || stats.BlobsTombstoned != 0 {
		t.Errorf("blobs reclaimed=%d tombstoned=%d; want 0/0 (content fully gated)",
			stats.BlobsReclaimed, stats.BlobsTombstoned)
	}
	if len(cs.updated) != 0 {
		t.Errorf("content store was updated %d times; want 0 on the snapshots-only path", len(cs.updated))
	}
	if _, ok := cs.infos[orphanBlob].Labels[labelKeyOrphanedAt]; ok {
		t.Error("the snapshots-only path must not tombstone content")
	}
}

func TestSweep_StampFailureDoesNotReap(t *testing.T) {
	// A failed tombstone Update is counted as an error but never removes the
	// artifact (it is in the stamp bucket, structurally excluded from reaping).
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	sn := &fakeSnapshotter{
		infos:     map[string]snapshots.Info{"orphan": {Name: "orphan", Labels: gcRootLabels()}},
		updateErr: errors.New("update boom"),
	}
	env := gcEnv{
		snapshots: sn,
		content:   &fakeContent{infos: map[digest.Digest]content.Info{}},
		now:       func() time.Time { return now },
		grace:     testGrace,
		logger:    zap.NewNop(),
	}
	stats, err := sweep(context.Background(), env, keySet(), keySet(), false)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.Errors != 1 || stats.SnapshotsTombstoned != 0 || stats.SnapshotsRemoved != 0 {
		t.Errorf("errors=%d tombstoned=%d removed=%d; want 1/0/0", stats.Errors, stats.SnapshotsTombstoned, stats.SnapshotsRemoved)
	}
	if _, ok := sn.infos["orphan"]; !ok {
		t.Error("artifact must be retained when its tombstone write fails")
	}
}

func TestRunImageGC_FailClosedOnResolveError(t *testing.T) {
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphan": {Name: "orphan", Labels: tombstone(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))},
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
		grace:                 testGrace,
		logger:                zap.NewNop(),
	}
	stats, err := runImageGC(context.Background(), env, true)
	if err == nil {
		t.Fatal("expected error from fail-closed MARK")
	}
	if stats.SnapshotsRemoved != 0 {
		t.Errorf("removed=%d; want 0 deletions on fail-closed", stats.SnapshotsRemoved)
	}
	if len(sn.updated) != 0 {
		t.Errorf("no label may be written when MARK fails; got %d updates", len(sn.updated))
	}
	if _, ok := sn.infos["orphan"]; !ok {
		t.Error("no snapshot may be deleted when MARK fails")
	}
}

func TestSweep_HasChildrenBackstop(t *testing.T) {
	// p is reap-eligible (aged tombstone, unreachable) but still has a reachable
	// child ch; Remove must fail-precondition and be skipped, not counted an error.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour)
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"p":  {Name: "p", Labels: tombstone(aged)},
			"ch": {Name: "ch", Parent: "p", Labels: gcRootLabels()},
		},
		usage: map[string]int64{},
	}
	env := gcEnv{
		snapshots: sn,
		content:   &fakeContent{infos: map[digest.Digest]content.Info{}},
		now:       func() time.Time { return now },
		grace:     testGrace,
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

// snapNow is the fixed "now" used by the classify tests.
var snapNow = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

func TestClassifySnapshots_ReachableClearsTombstone(t *testing.T) {
	gcRoots := []snapshots.Info{
		{Name: "a", Labels: tombstone(snapNow.Add(-time.Hour))}, // reachable + stamped -> clear
		{Name: "b", Labels: gcRootLabels()},                     // reachable, no stamp -> no-op
	}
	c := classifySnapshots(gcRoots, keySet("a", "b"), snapNow, testGrace)
	if got := names(c.clear); len(got) != 1 || !got["a"] {
		t.Errorf("clear = %v; want {a}", got)
	}
	if len(c.stamp) != 0 || len(c.reap) != 0 {
		t.Errorf("stamp=%v reap=%v; want both empty", names(c.stamp), names(c.reap))
	}
}

func TestClassifySnapshots_FirstSightingStamps(t *testing.T) {
	// Ancient gc.root, no orphanedAt: must be stamped, never reaped (commit age is
	// no longer a reap trigger).
	gcRoots := []snapshots.Info{
		{Name: "a", Labels: gcRootLabels()}, {Name: "b", Labels: gcRootLabels()}, // reachable
		{Name: "c", Labels: gcRootLabels()}, {Name: "d", Labels: gcRootLabels()}, // orphaned, unstamped
	}
	c := classifySnapshots(gcRoots, keySet("a", "b"), snapNow, testGrace)
	if got := names(c.stamp); len(got) != 2 || !got["c"] || !got["d"] {
		t.Errorf("stamp = %v; want {c,d}", got)
	}
	if len(c.reap) != 0 {
		t.Errorf("reap = %v; want empty", names(c.reap))
	}
}

func TestClassifySnapshots_AgedOrphanReaped(t *testing.T) {
	// base is still referenced; oldtop has an aged tombstone -> reap.
	gcRoots := []snapshots.Info{
		{Name: "base", Labels: gcRootLabels()},
		{Name: "oldtop", Parent: "base", Labels: tombstone(snapNow.Add(-48 * time.Hour))},
	}
	c := classifySnapshots(gcRoots, keySet("base"), snapNow, testGrace)
	if got := names(c.reap); len(got) != 1 || !got["oldtop"] {
		t.Errorf("reap = %v; want {oldtop}", got)
	}
}

func TestClassifySnapshots_WithinGraceKept(t *testing.T) {
	// An orphan tombstoned within grace is neither re-stamped nor reaped; an aged
	// sibling is reaped.
	gcRoots := []snapshots.Info{
		{Name: "fresh", Labels: tombstone(snapNow.Add(-time.Minute))},
		{Name: "old", Labels: tombstone(snapNow.Add(-48 * time.Hour))},
	}
	c := classifySnapshots(gcRoots, keySet(), snapNow, testGrace)
	if names(c.reap)["fresh"] {
		t.Error("an orphan tombstoned within grace must be kept")
	}
	if names(c.stamp)["fresh"] {
		t.Error("an already-tombstoned orphan must not be re-stamped")
	}
	if !names(c.reap)["old"] {
		t.Error("an orphan tombstoned past grace must be reaped")
	}
}

func TestClassifySnapshots_UnparseableStampReStamped(t *testing.T) {
	// A corrupt orphanedAt is treated as "not yet stamped" -> re-stamped (kept),
	// so a bad value can only ever delay reclamation.
	gcRoots := []snapshots.Info{
		{Name: "bad", Labels: map[string]string{labelKeyGCRoot: "x", labelKeyOrphanedAt: "not-a-time"}},
	}
	c := classifySnapshots(gcRoots, keySet(), snapNow, testGrace)
	if got := names(c.stamp); len(got) != 1 || !got["bad"] {
		t.Errorf("stamp = %v; want {bad}", got)
	}
	if len(c.reap) != 0 {
		t.Errorf("reap = %v; want empty", names(c.reap))
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

func TestClassifyBlobs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	aged := tombstone(now.Add(-48 * time.Hour)) // orphaned past grace
	fresh := tombstone(now.Add(-time.Minute))   // orphaned within grace

	d := func(c string) digest.Digest { return digest.Digest("sha256:" + strings.Repeat(c, 64)) }
	dReach, dOld, dFresh, dNew := d("1"), d("2"), d("3"), d("4")

	infos := []content.Info{
		{Digest: dReach, Labels: tombstone(now.Add(-48 * time.Hour))}, // reachable + stamped -> clear
		{Digest: dOld, Labels: aged},                                  // orphan + aged -> reap
		{Digest: dFresh, Labels: fresh},                               // orphan + in-grace -> keep
		{Digest: dNew, Labels: gcRootLabels()},                        // orphan + unstamped -> stamp
	}

	c := classifyBlobs(infos, keySet(dReach.String()), now, testGrace)
	if len(c.reap) != 1 || c.reap[0].Digest != dOld {
		t.Errorf("reap = %v; want {%s}", c.reap, dOld)
	}
	if len(c.clear) != 1 || c.clear[0].Digest != dReach {
		t.Errorf("clear = %v; want {%s}", c.clear, dReach)
	}
	if len(c.stamp) != 1 || c.stamp[0].Digest != dNew {
		t.Errorf("stamp = %v; want {%s}", c.stamp, dNew)
	}
}
