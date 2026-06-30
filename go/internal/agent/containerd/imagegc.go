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

// snapshotGC is the subset of snapshots.Snapshotter the GC needs.
type snapshotGC interface {
	Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error
	Stat(ctx context.Context, key string) (snapshots.Info, error)
	Usage(ctx context.Context, key string) (snapshots.Usage, error)
	Remove(ctx context.Context, key string) error
}

// contentGC is the subset of content.Store the GC needs.
type contentGC interface {
	Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error
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

// runImageGC performs one mark-and-sweep pass: it computes the reachable set
// from current images and containers (fail-closed), then reclaims gc.root
// snapshots — and, when includeContent is set, gc.root content blobs — outside
// that set.
//
// includeContent must be true ONLY when no deploy can be in flight (the boot
// sweep). A gc.root layer blob is written by WriteLayer *before* AssembleImage
// creates the image record that references it, so during a concurrent deploy a
// just-pushed blob is legitimately unreferenced; only quiescence (not the
// wall-clock grace alone) makes blob reclamation provably safe. Chain-ID
// snapshots have no such hazard — UnpackImage commits them *after* the image
// record exists — so the snapshot sweep always runs.
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

// sweep (SWEEP) removes gc.root snapshots not in the reachable set, and (only
// when includeContent is set) gc.root content blobs not in the reachable set.
// Individual removal failures are logged and counted but never abort the pass; a
// "has children" precondition error is a benign skip (the snapshot is still
// referenced — the structural safety backstop).
func sweep(ctx context.Context, env gcEnv, rSnap, rBlob map[string]struct{}, includeContent bool) (GCStats, error) {
	var stats GCStats

	var gcRootSnaps []snapshots.Info
	err := env.snapshots.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		gcRootSnaps = append(gcRootSnaps, info)
		return nil
	}, gcRootFilter)
	if err != nil {
		return stats, fmt.Errorf("walking snapshots: %w", err)
	}

	orphans := selectOrphanSnapshots(gcRootSnaps, rSnap, env.now(), env.grace)
	for _, key := range orderLeafFirst(orphans) {
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

	sizeByDigest := make(map[digest.Digest]int64, len(gcRootBlobs))
	for _, info := range gcRootBlobs {
		sizeByDigest[info.Digest] = info.Size
	}
	for _, d := range selectOrphanBlobs(gcRootBlobs, rBlob, env.now(), env.grace) {
		if derr := env.content.Delete(ctx, d); derr != nil {
			if errdefs.IsNotFound(derr) {
				continue
			}
			env.logger.Warn("failed to delete orphan blob", zap.String("digest", d.String()), zap.Error(derr))
			stats.Errors++
			continue
		}
		stats.BlobsReclaimed++
		stats.BlobBytes += sizeByDigest[d]
	}
	return stats, nil
}

// imageGCGracePeriod is the minimum age a gc.root-labelled snapshot or content
// blob must reach (by its gc.root RFC3339 timestamp) before the sweep may reclaim
// it. It protects artifacts a concurrent in-flight deploy created that this pass's
// reachable set may not cover:
//   - content blobs: WriteLayer labels a layer blob gc.root *before* AssembleImage
//     creates the referencing image record, so a just-pushed blob is briefly
//     unreferenced;
//   - snapshots: the async pass marks reachability once and sweeps later, so a
//     back-to-back deploy can commit new gc.root chain-ID snapshots after the mark
//     that are absent from the (older) reachable set.
//
// It is pinned to the unpack lease expiration (30m), which far exceeds a single
// pass's imageGCTimeout (5m); any artifact created during a pass is therefore
// comfortably within grace and kept, while genuine orphans age past it and are
// reclaimed by a later pass or the boot sweep.
const imageGCGracePeriod = unpackLeaseExpiration

// GCStats summarises what one garbage-collection pass reclaimed. It exists purely
// for observability — callers log it; no control flow branches on it.
type GCStats struct {
	SnapshotsRemoved int
	SnapshotBytes    int64
	BlobsReclaimed   int
	BlobBytes        int64
	Errors           int
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
		grace:                 imageGCGracePeriod,
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

// logGCStats emits a single summary line when a pass reclaimed something (or hit
// errors); quiet passes stay silent to avoid log noise on every deploy.
func logGCStats(logger *zap.Logger, stats GCStats) {
	if stats.SnapshotsRemoved+stats.BlobsReclaimed == 0 && stats.Errors == 0 {
		return
	}
	logger.Info("image GC reclaimed stale image data",
		zap.Int("snapshots_removed", stats.SnapshotsRemoved),
		zap.Int64("snapshot_bytes", stats.SnapshotBytes),
		zap.Int("blobs_reclaimed", stats.BlobsReclaimed),
		zap.Int64("blob_bytes", stats.BlobBytes),
		zap.Int("errors", stats.Errors),
	)
}

// selectOrphanSnapshots returns the gc.root snapshots eligible for removal:
// those whose key is not in the reachable set AND whose gc.root timestamp is
// older than grace. The age guard is essential because the async per-deploy pass
// builds its reachable set once and sweeps later: a back-to-back deploy can
// commit new gc.root chain-ID snapshots after the mark, and those would be absent
// from the (older) reachable set. Keeping anything younger than grace — which far
// exceeds a single pass's imageGCTimeout — prevents reaping a concurrent deploy's
// freshly committed layers. A snapshot is kept (conservatively) when its gc.root
// value is missing/unparseable or still within the grace window; genuine orphans
// age past grace and are reclaimed by a later pass or the boot sweep.
func selectOrphanSnapshots(gcRoots []snapshots.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) []snapshots.Info {
	var orphans []snapshots.Info
	for _, info := range gcRoots {
		if _, ok := reachable[info.Name]; ok {
			continue // referenced by a current image or container
		}
		ts, err := time.Parse(time.RFC3339, info.Labels[labelKeyGCRoot])
		if err != nil {
			continue // unparseable gc.root timestamp -> keep
		}
		if now.Sub(ts) <= grace {
			continue // committed too recently -> may belong to an in-flight deploy
		}
		orphans = append(orphans, info)
	}
	return orphans
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

// selectOrphanBlobs returns the gc.root content blobs eligible for reclamation:
// those whose digest is not in the reachable set AND whose gc.root timestamp is
// older than grace. A blob is kept (conservatively) when its gc.root label value
// is missing/unparseable or still within the grace window.
func selectOrphanBlobs(infos []content.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) []digest.Digest {
	var orphans []digest.Digest
	for _, info := range infos {
		if _, ok := reachable[info.Digest.String()]; ok {
			continue // referenced by a current image
		}
		ts, err := time.Parse(time.RFC3339, info.Labels[labelKeyGCRoot])
		if err != nil {
			continue // unparseable gc.root timestamp -> keep
		}
		if now.Sub(ts) <= grace {
			continue // within grace -> may belong to an in-flight deploy
		}
		orphans = append(orphans, info.Digest)
	}
	return orphans
}
