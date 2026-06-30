package containerd

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	"go.uber.org/zap"
)

// imageGCTimeout bounds a single asynchronous (post-deploy) GC pass so a wedged
// containerd call can't pin the single-flight guard forever.
const imageGCTimeout = 5 * time.Minute

// imageLister is the subset of images.Store the GC mark phase needs.
type imageLister interface {
	List(ctx context.Context, filters ...string) ([]images.Image, error)
}

// snapshotGC is the subset of snapshots.Snapshotter the GC needs. Update is used
// to write/clear the labelKeyOrphanedAt tombstone; the fieldpaths argument scopes
// the mutation to that one label so the gc.root pin is preserved.
type snapshotGC interface {
	Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error
	Stat(ctx context.Context, key string) (snapshots.Info, error)
	Usage(ctx context.Context, key string) (snapshots.Usage, error)
	Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error)
	Remove(ctx context.Context, key string) error
}

// contentGC is the subset of content.Store the GC needs. Update writes/clears the
// labelKeyOrphanedAt tombstone (fieldpath-scoped, preserving gc.root).
type contentGC interface {
	Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error
	Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error)
	Delete(ctx context.Context, dgst digest.Digest) error
}

// gcEnv collects the dependencies of one mark-and-sweep pass behind narrow
// seams so the orchestration is unit-testable without a live containerd. The
// concrete *Client wires these to the real containerd services.
type gcEnv struct {
	images                imageLister
	snapshots             snapshotGC
	content               contentGC
	resolveImage          func(ctx context.Context, img images.Image) (chainIDs []string, blobs []string, err error)
	containerSnapshotKeys func(ctx context.Context) ([]string, error)
	now                   func() time.Time
	grace                 time.Duration
	logger                *zap.Logger
}

// gcRootFilter is the containerd label filter that selects only gc.root-pinned
// entries — the only snapshots/blobs this GC ever considers for removal.
const gcRootFilter = `labels."` + labelKeyGCRoot + `"`

// runImageGC performs one mark-and-sweep pass over gc.root snapshots — and, when
// includeContent is set, gc.root content blobs. It computes the reachable set
// from current images and containers (fail-closed), then applies the two-phase
// tombstone in sweep: stamp newly-orphaned artifacts, clear re-adopted ones, and
// reap only those that have been continuously orphaned past the grace window.
//
// includeContent gates the ENTIRE content section (stamp, clear, and reap) and
// must be true ONLY when no deploy can be in flight (the boot sweep). A gc.root
// layer blob is written by WriteLayer *before* AssembleImage creates the image
// record that references it, so during a concurrent deploy a just-pushed blob is
// legitimately unreferenced; only quiescence makes blob reclamation provably
// safe, and per-deploy passes must not so much as tombstone content. Chain-ID
// snapshots have the favorable ordering — UnpackImage commits them *after* the
// image record exists — so the snapshot section always runs.
func runImageGC(ctx context.Context, env gcEnv, includeContent bool) (GCStats, error) {
	rSnap, rBlob, err := buildReachableSet(ctx, env)
	if err != nil {
		return GCStats{}, err
	}
	return sweep(ctx, env, rSnap, rBlob, includeContent)
}

// buildReachableSet (MARK) returns the chain-ID snapshot keys and content
// digests that must be kept: everything referenced by a current image record or
// an existing container. Any error aborts the whole pass with empty sets so the
// caller never sweeps on a partial reachable set (which could delete live data).
func buildReachableSet(ctx context.Context, env gcEnv) (rSnap, rBlob map[string]struct{}, err error) {
	rSnap = make(map[string]struct{})
	rBlob = make(map[string]struct{})

	imgs, err := env.images.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing images: %w", err)
	}
	for _, img := range imgs {
		chainIDs, blobs, err := env.resolveImage(ctx, img)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving image %q: %w", img.Name, err)
		}
		for _, id := range chainIDs {
			rSnap[id] = struct{}{}
		}
		for _, b := range blobs {
			rBlob[b] = struct{}{}
		}
	}

	keys, err := env.containerSnapshotKeys(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing container snapshots: %w", err)
	}
	for _, key := range keys {
		for key != "" {
			rSnap[key] = struct{}{}
			info, statErr := env.snapshots.Stat(ctx, key)
			if statErr != nil {
				if errdefs.IsNotFound(statErr) {
					break // active snapshot raced a teardown; its parents are still covered by images
				}
				return nil, nil, fmt.Errorf("stat snapshot %q: %w", key, statErr)
			}
			key = info.Parent
		}
	}
	return rSnap, rBlob, nil
}

// sweep applies the two-phase tombstone against gc.root snapshots — and, when
// includeContent is set, gc.root content blobs. Per artifact it either stamps a
// fresh orphanedAt tombstone, clears the tombstone of a re-adopted artifact, or
// reaps one that has been continuously orphaned past the grace window. Label
// (stamp/clear) and removal failures are logged and counted in stats.Errors but
// never abort the pass; a "has children" precondition error on removal is a
// benign skip (the snapshot is still referenced — the structural safety
// backstop). The stamp timestamp comes from env.now so passes are deterministic
// under test.
func sweep(ctx context.Context, env gcEnv, rSnap, rBlob map[string]struct{}, includeContent bool) (GCStats, error) {
	var stats GCStats
	now := env.now()
	stamp := now.UTC().Format(time.RFC3339)

	var gcRootSnaps []snapshots.Info
	err := env.snapshots.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		gcRootSnaps = append(gcRootSnaps, info)
		return nil
	}, gcRootFilter)
	if err != nil {
		return stats, fmt.Errorf("walking snapshots: %w", err)
	}

	sc := classifySnapshots(gcRootSnaps, rSnap, now, env.grace)
	for _, info := range sc.stamp {
		if uerr := stampSnapshot(ctx, env.snapshots, info.Name, stamp); uerr != nil {
			env.logger.Warn("failed to tombstone snapshot", zap.String("snapshot", info.Name), zap.Error(uerr))
			stats.Errors++
			continue
		}
		stats.SnapshotsTombstoned++
	}
	for _, info := range sc.clear {
		if uerr := stampSnapshot(ctx, env.snapshots, info.Name, ""); uerr != nil {
			env.logger.Warn("failed to clear snapshot tombstone", zap.String("snapshot", info.Name), zap.Error(uerr))
			stats.Errors++
			continue
		}
		stats.StampsCleared++
	}
	for _, key := range orderLeafFirst(sc.reap) {
		var size int64
		if u, uerr := env.snapshots.Usage(ctx, key); uerr == nil {
			size = u.Size
		}
		if rerr := env.snapshots.Remove(ctx, key); rerr != nil {
			if errdefs.IsFailedPrecondition(rerr) {
				env.logger.Debug("skipping snapshot with children", zap.String("snapshot", key))
				continue
			}
			env.logger.Warn("failed to remove orphan snapshot", zap.String("snapshot", key), zap.Error(rerr))
			stats.Errors++
			continue
		}
		stats.SnapshotsRemoved++
		stats.SnapshotBytes += size
	}

	if !includeContent {
		return stats, nil
	}

	var gcRootBlobs []content.Info
	err = env.content.Walk(ctx, func(info content.Info) error {
		gcRootBlobs = append(gcRootBlobs, info)
		return nil
	}, gcRootFilter)
	if err != nil {
		return stats, fmt.Errorf("walking content: %w", err)
	}

	bc := classifyBlobs(gcRootBlobs, rBlob, now, env.grace)
	for _, info := range bc.stamp {
		if uerr := stampBlob(ctx, env.content, info.Digest, stamp); uerr != nil {
			env.logger.Warn("failed to tombstone blob", zap.String("digest", info.Digest.String()), zap.Error(uerr))
			stats.Errors++
			continue
		}
		stats.BlobsTombstoned++
	}
	for _, info := range bc.clear {
		if uerr := stampBlob(ctx, env.content, info.Digest, ""); uerr != nil {
			env.logger.Warn("failed to clear blob tombstone", zap.String("digest", info.Digest.String()), zap.Error(uerr))
			stats.Errors++
			continue
		}
		stats.StampsCleared++
	}
	for _, info := range bc.reap {
		if derr := env.content.Delete(ctx, info.Digest); derr != nil {
			if errdefs.IsNotFound(derr) {
				continue
			}
			env.logger.Warn("failed to delete orphan blob", zap.String("digest", info.Digest.String()), zap.Error(derr))
			stats.Errors++
			continue
		}
		stats.BlobsReclaimed++
		stats.BlobBytes += info.Size
	}
	return stats, nil
}

// stampSnapshot writes (or, with an empty ts, clears) the labelKeyOrphanedAt
// tombstone on a snapshot. The "labels.<key>" fieldpath scopes the update to that
// one label so the gc.root pin — and any other labels — survive; an empty value
// deletes the label.
func stampSnapshot(ctx context.Context, sn snapshotGC, name, ts string) error {
	_, err := sn.Update(ctx, snapshots.Info{
		Name:   name,
		Labels: map[string]string{labelKeyOrphanedAt: ts},
	}, "labels."+labelKeyOrphanedAt)
	return err
}

// stampBlob writes (or, with an empty ts, clears) the labelKeyOrphanedAt
// tombstone on a content blob, fieldpath-scoped exactly like stampSnapshot.
func stampBlob(ctx context.Context, cs contentGC, dgst digest.Digest, ts string) error {
	_, err := cs.Update(ctx, content.Info{
		Digest: dgst,
		Labels: map[string]string{labelKeyOrphanedAt: ts},
	}, "labels."+labelKeyOrphanedAt)
	return err
}

// The image GC's grace window is an ORPHAN-AGE window: a gc.root snapshot or
// content blob must stay continuously unreferenced for at least env.grace —
// measured from its labelKeyOrphanedAt tombstone (when the GC first observed it
// unreferenced), not its gc.root commit time — before a sweep may reap it. A
// layer re-adopted by a later deploy has its tombstone cleared, resetting the
// clock, so a long-lived base layer reused after a brief gap is never reaped and
// re-uploaded. The grace duration is configured per Client (imageGCGrace,
// WENDY_IMAGE_GC_GRACE_PERIOD) and clamped to a floor in NewClient.
//
// Safety under concurrent deploys does NOT depend on the grace length: the first
// sweep that sees an orphan only stamps it (classifySnapshots/classifyBlobs route
// an un-tombstoned orphan to the stamp bucket, never reap), so reaping always
// requires a later sweep to still find it orphaned with an aged tombstone. A
// snapshot's image record exists before the snapshot is committed (UnpackImage
// runs after is.Create), and content is reaped only at boot/quiescence, so an
// in-flight deploy's artifacts are re-marked reachable and cleared before they can
// age out. The floor is therefore pure defense-in-depth against a pathologically
// small configured value.

// GCStats summarises what one garbage-collection pass did. It exists purely for
// observability — callers log it; no control flow branches on it. The Tombstoned
// and StampsCleared counters surface the two-phase activity (an orphan is stamped
// on one pass and only reaped on a later pass past grace), so a pass that reclaims
// nothing yet is still observable.
type GCStats struct {
	SnapshotsRemoved    int
	SnapshotBytes       int64
	BlobsReclaimed      int
	BlobBytes           int64
	SnapshotsTombstoned int
	BlobsTombstoned     int
	StampsCleared       int
	Errors              int
}

// tryStartGC acquires the single-flight guard. It returns false when a pass is
// already running, in which case the caller must not start another.
func (c *Client) tryStartGC() bool {
	return c.gcRunning.CompareAndSwap(false, true)
}

// finishGC releases the single-flight guard.
func (c *Client) finishGC() {
	c.gcRunning.Store(false)
}

// GarbageCollectImages runs one synchronous full mark-and-sweep pass (snapshots
// AND content blobs). It is a no-op when GC is disabled or a pass is already
// running (a skip is not an error). Used for the boot sweep, which is safe to
// reclaim content because it runs before the agent serves any deploy RPC — no
// deploy can be in flight. The per-deploy hook (triggerImageGCAsync) reclaims
// snapshots only, since a concurrent deploy's just-pushed blobs are not yet
// referenced by an image record.
func (c *Client) GarbageCollectImages(ctx context.Context) (GCStats, error) {
	if !c.imageGCEnabled {
		return GCStats{}, nil
	}
	if !c.tryStartGC() {
		return GCStats{}, nil
	}
	defer c.finishGC()

	stats, err := c.garbageCollectImages(ctx, true)
	if err == nil {
		logGCStats(c.logger, stats)
	}
	return stats, err
}

// triggerImageGCAsync kicks off a snapshot-only mark-and-sweep pass in the
// background after a successful deploy. It returns immediately so it never blocks
// (or fails) the deploy. The pass runs on a detached, time-bounded context
// because the deploy's request context is gone once the RPC returns. Single-flight
// via the gcRunning guard means concurrent/back-to-back deploys never overlap
// passes. Content blobs are intentionally left to the boot sweep (see
// GarbageCollectImages / runImageGC).
func (c *Client) triggerImageGCAsync() {
	if !c.imageGCEnabled {
		return
	}
	if !c.tryStartGC() {
		return // a pass is already running; it (or the next deploy/boot) covers this
	}
	go func() {
		defer c.finishGC()
		ctx, cancel := context.WithTimeout(context.Background(), imageGCTimeout)
		defer cancel()
		stats, err := c.garbageCollectImages(ctx, false)
		if err != nil {
			c.logger.Warn("image GC pass failed", zap.Error(err))
			return
		}
		logGCStats(c.logger, stats)
	}()
}

// garbageCollectImages wires the concrete containerd services into a gcEnv and
// runs one pass. It assumes the single-flight guard is already held.
func (c *Client) garbageCollectImages(ctx context.Context, includeContent bool) (GCStats, error) {
	ctx = c.withNamespace(ctx)
	env := gcEnv{
		images:                c.client.ImageService(),
		snapshots:             c.client.SnapshotService(c.snapshotter),
		content:               c.client.ContentStore(),
		resolveImage:          c.resolveImageChain,
		containerSnapshotKeys: c.containerSnapshotKeys,
		now:                   time.Now,
		grace:                 c.imageGCGrace,
		logger:                c.logger,
	}
	return runImageGC(ctx, env, includeContent)
}

// resolveImageChain resolves an image record to the chain-ID snapshots and the
// content digests (manifest, config, layers) it references. ctx must already
// carry the containerd namespace.
func (c *Client) resolveImageChain(ctx context.Context, img images.Image) (chainIDs []string, blobs []string, err error) {
	cs := c.client.ContentStore()
	manifest, err := images.Manifest(ctx, cs, img.Target, platforms.Default())
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest: %w", err)
	}
	diffIDs, err := images.RootFS(ctx, cs, manifest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("reading rootfs: %w", err)
	}
	chainIDs = chainIDsForDiffIDs(diffIDs)
	blobs = make([]string, 0, len(manifest.Layers)+2)
	blobs = append(blobs, img.Target.Digest.String(), manifest.Config.Digest.String())
	for _, l := range manifest.Layers {
		blobs = append(blobs, l.Digest.String())
	}
	return chainIDs, blobs, nil
}

// containerSnapshotKeys returns the active snapshot key of EVERY container
// (running or stopped), not just app containers: ROS2 sidecars and any other
// container carry their own snapshot chains and must be kept too. The mark phase
// walks each key's parent chain to keep the read-only layers a container could
// still need. Enumerating all containers makes the reachable set the primary
// safety mechanism rather than leaning on the has-children removal backstop. ctx
// must already carry the containerd namespace.
func (c *Client) containerSnapshotKeys(ctx context.Context) ([]string, error) {
	ctrs, err := c.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(ctrs))
	for _, ctr := range ctrs {
		info, infoErr := ctr.Info(ctx)
		if infoErr != nil {
			return nil, fmt.Errorf("reading container %q info: %w", ctr.ID(), infoErr)
		}
		if info.SnapshotKey != "" {
			keys = append(keys, info.SnapshotKey)
		}
	}
	return keys, nil
}

// logGCStats emits a summary of one pass. A pass that actually reclaimed data (or
// hit errors) logs at Info; a pass that only tombstoned or cleared stamps — the
// routine two-phase bookkeeping on most deploys — logs at Debug to avoid Info
// noise. A fully quiet pass stays silent.
func logGCStats(logger *zap.Logger, stats GCStats) {
	reaped := stats.SnapshotsRemoved + stats.BlobsReclaimed
	tombstoneActivity := stats.SnapshotsTombstoned + stats.BlobsTombstoned + stats.StampsCleared
	if reaped == 0 && stats.Errors == 0 && tombstoneActivity == 0 {
		return
	}
	fields := []zap.Field{
		zap.Int("snapshots_removed", stats.SnapshotsRemoved),
		zap.Int64("snapshot_bytes", stats.SnapshotBytes),
		zap.Int("blobs_reclaimed", stats.BlobsReclaimed),
		zap.Int64("blob_bytes", stats.BlobBytes),
		zap.Int("snapshots_tombstoned", stats.SnapshotsTombstoned),
		zap.Int("blobs_tombstoned", stats.BlobsTombstoned),
		zap.Int("stamps_cleared", stats.StampsCleared),
		zap.Int("errors", stats.Errors),
	}
	if reaped == 0 && stats.Errors == 0 {
		logger.Debug("image GC tombstoned stale image data", fields...)
		return
	}
	logger.Info("image GC reclaimed stale image data", fields...)
}

// snapClass is the per-pass partition of gc.root snapshots produced by
// classifySnapshots: which to tombstone, which to clear, and which to reap.
type snapClass struct {
	stamp []snapshots.Info // orphaned, not yet tombstoned -> write orphanedAt
	clear []snapshots.Info // reachable but still tombstoned -> remove orphanedAt
	reap  []snapshots.Info // orphaned with a tombstone older than grace -> remove
}

// classifySnapshots partitions gc.root snapshots against the reachable set using
// the orphan-age tombstone (labelKeyOrphanedAt), NOT the gc.root commit time:
//   - reachable + tombstoned         -> clear (re-adopted; reset its clock)
//   - orphaned, no/empty/bad stamp   -> stamp (first sighting; never reaped this pass)
//   - orphaned, stamp older than grace -> reap
//   - orphaned, stamp within grace     -> keep (no-op)
//
// Routing every un-tombstoned orphan to stamp (never reap) is what makes the GC
// safe without a commit-age guard: reaping always requires a *later* pass to find
// the artifact still orphaned with an aged tombstone. A missing, empty, or
// unparseable tombstone is treated as "not yet stamped", so a corrupt value can
// only delay — never hasten — reclamation. The clear branch is the load-bearing
// part of the re-upload fix: a base layer reused after a gap gets its tombstone
// wiped here, so its grace clock restarts from the next time it falls out of use.
func classifySnapshots(gcRoots []snapshots.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) snapClass {
	var c snapClass
	for _, info := range gcRoots {
		stamp := info.Labels[labelKeyOrphanedAt]
		if _, ok := reachable[info.Name]; ok {
			if stamp != "" {
				c.clear = append(c.clear, info)
			}
			continue // referenced by a current image or container
		}
		ts, err := time.Parse(time.RFC3339, stamp)
		if stamp == "" || err != nil {
			c.stamp = append(c.stamp, info)
			continue // first sighting (or corrupt stamp) -> tombstone, never reap
		}
		if now.Sub(ts) <= grace {
			continue // orphaned recently -> still within grace, keep
		}
		c.reap = append(c.reap, info)
	}
	return c
}

// orderLeafFirst returns the orphan snapshot keys ordered so that every child is
// listed before its parent. Removing in this order means a parent layer is never
// deleted while one of its still-orphaned children references it. Ordering is by
// the number of ancestors within the orphan set, descending; ties break by name
// for determinism.
func orderLeafFirst(orphans []snapshots.Info) []string {
	parentOf := make(map[string]string, len(orphans)) // name -> parent (within set)
	for _, o := range orphans {
		parentOf[o.Name] = o.Parent
	}
	ancestorsInSet := func(name string) int {
		count := 0
		// Chain-ID parents form a strict tree, so this terminates well before the
		// bound; the len(parentOf) cap is defensive against a malformed cycle
		// hanging the background GC goroutine.
		for count <= len(parentOf) {
			parent, ok := parentOf[name]
			if !ok || parent == "" {
				return count
			}
			if _, ok := parentOf[parent]; !ok {
				return count
			}
			count++
			name = parent
		}
		return count
	}
	keys := make([]string, 0, len(orphans))
	for _, o := range orphans {
		keys = append(keys, o.Name)
	}
	sort.Slice(keys, func(i, j int) bool {
		di, dj := ancestorsInSet(keys[i]), ancestorsInSet(keys[j])
		if di != dj {
			return di > dj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// blobClass is the per-pass partition of gc.root content blobs produced by
// classifyBlobs. Each bucket holds the full content.Info so the sweep can read
// the digest (and Size, for reaped blobs) without a second lookup.
type blobClass struct {
	stamp []content.Info
	clear []content.Info
	reap  []content.Info
}

// classifyBlobs is the content-blob analogue of classifySnapshots, keyed on the
// blob digest. See classifySnapshots for the tombstone rules; the same orphan-age
// semantics apply. Reaping the reap bucket is gated to the boot sweep by the
// caller (per-deploy passes never touch content) — see runImageGC.
func classifyBlobs(infos []content.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) blobClass {
	var c blobClass
	for _, info := range infos {
		stamp := info.Labels[labelKeyOrphanedAt]
		if _, ok := reachable[info.Digest.String()]; ok {
			if stamp != "" {
				c.clear = append(c.clear, info)
			}
			continue // referenced by a current image
		}
		ts, err := time.Parse(time.RFC3339, stamp)
		if stamp == "" || err != nil {
			c.stamp = append(c.stamp, info)
			continue // first sighting (or corrupt stamp) -> tombstone, never reap
		}
		if now.Sub(ts) <= grace {
			continue // orphaned recently -> still within grace, keep
		}
		c.reap = append(c.reap, info)
	}
	return c
}
