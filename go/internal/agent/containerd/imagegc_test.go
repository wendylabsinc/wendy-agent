package containerd

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/diskspace"
)

// testGrace is the concurrency-safety grace used throughout these tests. A
// gc.root artifact whose commit timestamp is older than this is reap-eligible;
// a fresher one is spared as potentially belonging to an in-flight deploy.
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

// gcRootAt marks an artifact gc.root with a commit timestamp of ts — the value
// the GC reads to decide whether an orphan is fresh (spared) or aged (reapable).
func gcRootAt(ts time.Time) map[string]string {
	return map[string]string{labelKeyGCRoot: ts.UTC().Format(time.RFC3339)}
}

func TestRunImageGC_ReclaimsAgedOrphans(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour) // gc.root committed well past grace

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
			c0:     {Name: c0, Labels: gcRootAt(now)},
			c1:     {Name: c1, Parent: c0, Labels: gcRootAt(now)},
			oldA:   {Name: oldA, Parent: c0, Labels: gcRootAt(aged)},
			oldB:   {Name: oldB, Parent: oldA, Labels: gcRootAt(aged)},
			active: {Name: active, Parent: c1}, // no gc.root label
		},
		usage: map[string]int64{oldA: 100, oldB: 200},
	}
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			l0:   {Digest: l0, Size: 10, Labels: gcRootAt(now)},
			l1:   {Digest: l1, Size: 20, Labels: gcRootAt(now)},
			oldL: {Digest: oldL, Size: 33, Labels: gcRootAt(aged)},
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
	// The per-deploy async path (includeContent=false) reaps aged orphan snapshots
	// but must never touch content blobs — because a deploy that re-uses a
	// dedup-hit layer never refreshes that blob's gc.root timestamp, so only the
	// quiescent boot sweep may reclaim content.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour)
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphanSnap": {Name: "orphanSnap", Labels: gcRootAt(aged)},
		},
		usage: map[string]int64{"orphanSnap": 50},
	}
	orphanBlob := digest.Digest("sha256:orphanblob")
	cs := &fakeContent{
		infos: map[digest.Digest]content.Info{
			orphanBlob: {Digest: orphanBlob, Size: 99, Labels: gcRootAt(aged)},
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
	if stats.BlobsReclaimed != 0 {
		t.Errorf("blobs reclaimed=%d; want 0 (content fully gated on the deploy path)", stats.BlobsReclaimed)
	}
	if len(cs.deleted) != 0 {
		t.Errorf("content store was deleted from %d times; want 0 on the snapshots-only path", len(cs.deleted))
	}
	if _, ok := cs.infos[orphanBlob]; !ok {
		t.Error("the snapshots-only path must not delete content")
	}
}

func TestRunImageGC_FailClosedOnResolveError(t *testing.T) {
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"orphan": {Name: "orphan", Labels: gcRootAt(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))},
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
	if len(sn.removed) != 0 {
		t.Errorf("no snapshot may be removed when MARK fails; got %v", sn.removed)
	}
	if _, ok := sn.infos["orphan"]; !ok {
		t.Error("no snapshot may be deleted when MARK fails")
	}
}

func TestSweep_HasChildrenBackstop(t *testing.T) {
	// p is reap-eligible (aged gc.root, unreachable) but still has a reachable
	// child ch; Remove must fail-precondition and be skipped, not counted an error.
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-48 * time.Hour)
	sn := &fakeSnapshotter{
		infos: map[string]snapshots.Info{
			"p":  {Name: "p", Labels: gcRootAt(aged)},
			"ch": {Name: "ch", Parent: "p", Labels: gcRootAt(now)},
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

// --- disk-pressure gate + single-flight ---

func TestUnderDiskPressure(t *testing.T) {
	mk := func(pct float64, ok bool) *Client {
		return &Client{
			containerdRoot: "/x",
			diskFreePct:    func(string) (float64, bool) { return pct, ok },
		}
	}
	if !mk(diskspace.WarnFreePct-1, true).underDiskPressure() {
		t.Error("free % below the warn threshold must report disk pressure")
	}
	if mk(diskspace.WarnFreePct+1, true).underDiskPressure() {
		t.Error("free % above the warn threshold must not report disk pressure")
	}
	if mk(0, false).underDiskPressure() {
		t.Error("an unavailable measurement must not report disk pressure")
	}
}

func TestGarbageCollectImages_DisabledNoOp(t *testing.T) {
	var called atomic.Bool
	c := &Client{
		logger:         zap.NewNop(),
		imageGCEnabled: false,
		containerdRoot: "/x",
		diskFreePct:    func(string) (float64, bool) { return 1, true }, // under pressure, yet disabled
		gcPass:         func(context.Context, bool) (GCStats, error) { called.Store(true); return GCStats{}, nil },
	}
	stats, err := c.GarbageCollectImages(context.Background())
	if err != nil {
		t.Fatalf("GarbageCollectImages disabled: %v", err)
	}
	if called.Load() {
		t.Error("no GC pass may run when GC is disabled")
	}
	if stats != (GCStats{}) {
		t.Errorf("stats = %+v; want zero when disabled", stats)
	}
}

func TestGarbageCollectImages_SkipsWhenHealthy(t *testing.T) {
	var called atomic.Bool
	c := &Client{
		logger:         zap.NewNop(),
		imageGCEnabled: true,
		containerdRoot: "/x",
		diskFreePct:    func(string) (float64, bool) { return 50, true }, // roomy
		gcPass: func(context.Context, bool) (GCStats, error) {
			called.Store(true)
			return GCStats{SnapshotsRemoved: 1}, nil
		},
	}
	stats, err := c.GarbageCollectImages(context.Background())
	if err != nil {
		t.Fatalf("GarbageCollectImages: %v", err)
	}
	if called.Load() {
		t.Error("GC must not run when the device has free space")
	}
	if stats != (GCStats{}) {
		t.Errorf("stats = %+v; want zero when healthy", stats)
	}
}

func TestGarbageCollectImages_RunsUnderPressure(t *testing.T) {
	var gotContent bool
	var called atomic.Bool
	c := &Client{
		logger:         zap.NewNop(),
		imageGCEnabled: true,
		containerdRoot: "/x",
		diskFreePct:    func(string) (float64, bool) { return 1, true }, // under pressure
		gcPass: func(_ context.Context, includeContent bool) (GCStats, error) {
			called.Store(true)
			gotContent = includeContent
			return GCStats{SnapshotsRemoved: 2, BlobsReclaimed: 1}, nil
		},
	}
	stats, err := c.GarbageCollectImages(context.Background())
	if err != nil {
		t.Fatalf("GarbageCollectImages: %v", err)
	}
	if !called.Load() {
		t.Fatal("GC must run under disk pressure")
	}
	if !gotContent {
		t.Error("the boot sweep must include content (includeContent=true)")
	}
	if stats.SnapshotsRemoved != 2 || stats.BlobsReclaimed != 1 {
		t.Errorf("stats = %+v; want 2 snapshots / 1 blob", stats)
	}
}

func TestTriggerImageGCAsync_HealthySkips(t *testing.T) {
	var called atomic.Bool
	c := &Client{
		logger:         zap.NewNop(),
		imageGCEnabled: true,
		containerdRoot: "/x",
		diskFreePct:    func(string) (float64, bool) { return 50, true }, // roomy
		gcPass:         func(context.Context, bool) (GCStats, error) { called.Store(true); return GCStats{}, nil },
	}
	c.triggerImageGCAsync()
	if called.Load() {
		t.Error("no GC pass may run on a roomy device")
	}
	// A healthy device returns before spawning any goroutine, so the single-flight
	// guard must be free.
	if !c.tryStartGC() {
		t.Error("single-flight guard should be free when healthy (no pass was started)")
	}
	c.finishGC()
}

func TestTriggerImageGCAsync_RunsUnderPressureSnapshotsOnly(t *testing.T) {
	got := make(chan bool, 1)
	c := &Client{
		logger:         zap.NewNop(),
		imageGCEnabled: true,
		containerdRoot: "/x",
		diskFreePct:    func(string) (float64, bool) { return 5, true }, // under pressure
		gcPass: func(_ context.Context, includeContent bool) (GCStats, error) {
			got <- includeContent
			return GCStats{}, nil
		},
	}
	c.triggerImageGCAsync()
	select {
	case includeContent := <-got:
		if includeContent {
			t.Error("the deploy pass must be snapshots-only (includeContent=false)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GC pass did not run under disk pressure")
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

// --- pure classifiers ---

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

func TestClassifySnapshots_ReachableKept(t *testing.T) {
	// A reachable snapshot is never reaped, even with an ancient gc.root stamp.
	gcRoots := []snapshots.Info{
		{Name: "live", Labels: gcRootAt(snapNow.Add(-72 * time.Hour))},
	}
	if reap := classifySnapshots(gcRoots, keySet("live"), snapNow, testGrace); len(reap) != 0 {
		t.Errorf("reachable snapshot must never be reaped; got %v", names(reap))
	}
}

func TestClassifySnapshots_AgedOrphanReaped(t *testing.T) {
	// base is still referenced; oldtop's gc.root is past grace -> reap.
	gcRoots := []snapshots.Info{
		{Name: "base", Labels: gcRootAt(snapNow)},
		{Name: "oldtop", Parent: "base", Labels: gcRootAt(snapNow.Add(-48 * time.Hour))},
	}
	reap := names(classifySnapshots(gcRoots, keySet("base"), snapNow, testGrace))
	if len(reap) != 1 || !reap["oldtop"] {
		t.Errorf("reap = %v; want {oldtop}", reap)
	}
}

func TestClassifySnapshots_FreshGcRootKept(t *testing.T) {
	// An orphan whose gc.root stamp is within grace is spared (it may belong to an
	// in-flight deploy); an aged sibling is reaped.
	gcRoots := []snapshots.Info{
		{Name: "fresh", Labels: gcRootAt(snapNow.Add(-time.Minute))},
		{Name: "old", Labels: gcRootAt(snapNow.Add(-48 * time.Hour))},
	}
	reap := names(classifySnapshots(gcRoots, keySet(), snapNow, testGrace))
	if reap["fresh"] {
		t.Error("an orphan whose gc.root stamp is within grace must be kept")
	}
	if !reap["old"] {
		t.Error("an orphan whose gc.root stamp is past grace must be reaped")
	}
}

func TestClassifySnapshots_UnparseableGcRootReaped(t *testing.T) {
	// A missing/unparseable gc.root value is treated as aged (an in-flight deploy
	// always writes a fresh, parseable stamp), so an unreferenced one is reaped.
	gcRoots := []snapshots.Info{
		{Name: "bad", Labels: map[string]string{labelKeyGCRoot: "not-a-time"}},
	}
	reap := names(classifySnapshots(gcRoots, keySet(), snapNow, testGrace))
	if len(reap) != 1 || !reap["bad"] {
		t.Errorf("reap = %v; want {bad}", reap)
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

	d := func(c string) digest.Digest { return digest.Digest("sha256:" + strings.Repeat(c, 64)) }
	dReach, dOld, dFresh, dBad := d("1"), d("2"), d("3"), d("4")

	infos := []content.Info{
		{Digest: dReach, Labels: gcRootAt(now.Add(-72 * time.Hour))},   // reachable -> keep
		{Digest: dOld, Labels: gcRootAt(now.Add(-48 * time.Hour))},     // orphan aged -> reap
		{Digest: dFresh, Labels: gcRootAt(now.Add(-time.Minute))},      // orphan within grace -> keep
		{Digest: dBad, Labels: map[string]string{labelKeyGCRoot: "x"}}, // orphan unparseable -> reap
	}

	reap := classifyBlobs(infos, keySet(dReach.String()), now, testGrace)
	got := map[digest.Digest]bool{}
	for _, i := range reap {
		got[i.Digest] = true
	}
	if !got[dOld] || !got[dBad] {
		t.Errorf("reap must include the aged and unparseable orphans; got %v", got)
	}
	if got[dReach] || got[dFresh] {
		t.Errorf("reap must exclude the reachable and fresh blobs; got %v", got)
	}
	if len(reap) != 2 {
		t.Errorf("reap size = %d; want 2", len(reap))
	}
}
