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

	"github.com/wendylabsinc/wendy/go/internal/shared/diskspace"
)

// imageGCTimeout bounds a single asynchronous (post-deploy) GC pass so a wedged
// containerd call can't pin the single-flight guard forever.
const imageGCTimeout = 5 * time.Minute

// gcRootGrace is the concurrency-safety window applied to the EXISTING
// containerd.io/gc.root commit timestamp: a gc.root snapshot or blob younger
// than this is spared, because it may belong to a deploy still in flight (its
// image record not yet written). It is NOT a retention policy — retention is
// governed entirely by disk pressure (GC only runs when free space is low) — so
// it only needs to cover the longest gap between an artifact being written and
// the image record that references it existing. That bound is the unpack lease
// window, so gcRootGrace mirrors unpackLeaseExpiration.
const gcRootGrace = unpackLeaseExpiration

// imageLister is the subset of images.Store the GC mark phase needs.
type imageLister interface {
	List(ctx context.Context, filters ...string) ([]images.Image, error)
}

// snapshotGC is the subset of snapshots.Snapshotter the GC needs. The GC never
// mutates labels, so no Update method is required.
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

// runImageGC performs one mark-and-sweep pass over gc.root snapshots — and, when
// includeContent is set, gc.root content blobs. It computes the reachable set
// from current images and containers (fail-closed), then reaps every gc.root
// artifact that is unreferenced and whose gc.root commit timestamp is older than
// the grace window.
//
// includeContent gates the ENTIRE content section and must be true ONLY when no
// deploy can be in flight (the boot sweep). A deploy that re-uses an existing
// layer hits the CLI's push dedup and never re-writes that blob, so the blob's
// gc.root timestamp stays old even though a new image record is about to
// reference it; only quiescence makes blob reclamation provably safe. Chain-ID
// snapshots have the favorable ordering — the image record exists before the
// snapshot is committed — so the snapshot section always runs, protected against
// a racing fresh commit by the gc.root grace window.
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

// sweep removes the gc.root snapshots — and, when includeContent is set, gc.root
// content blobs — that classifySnapshots/classifyBlobs mark reapable. Removal
// failures are logged and counted in stats.Errors but never abort the pass; a
// "has children" precondition error on snapshot removal is a benign skip (the
// snapshot is still referenced — the structural safety backstop).
func sweep(ctx context.Context, env gcEnv, rSnap, rBlob map[string]struct{}, includeContent bool) (GCStats, error) {
	var stats GCStats
	now := env.now()

	var gcRootSnaps []snapshots.Info
	err := env.snapshots.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		gcRootSnaps = append(gcRootSnaps, info)
		return nil
	}, gcRootFilter)
	if err != nil {
		return stats, fmt.Errorf("walking snapshots: %w", err)
	}

	for _, key := range orderLeafFirst(classifySnapshots(gcRootSnaps, rSnap, now, env.grace)) {
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

	for _, info := range classifyBlobs(gcRootBlobs, rBlob, now, env.grace) {
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

// GCStats summarises what one garbage-collection pass did. It exists purely for
// observability — callers log it; no control flow branches on it.
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

// underDiskPressure reports whether the filesystem backing containerd's data
// root is low enough on free space to justify reclaiming stale image data. It
// uses the same free-% threshold `wendy device doctor` warns at, so both agree
// on when a device is "too full". A failed measurement (statfs error, or any
// non-Linux build) reports false, keeping the GC a no-op.
func (c *Client) underDiskPressure() bool {
	pct, ok := c.diskFreePct(c.containerdRoot)
	if !ok {
		return false
	}
	return pct < diskspace.WarnFreePct
}

// GarbageCollectImages runs one synchronous full mark-and-sweep pass (snapshots
// AND content blobs) when the containerd filesystem is under disk pressure. It
// is a no-op when GC is disabled, when free space is above the threshold, or
// when a pass is already running (none of which is an error). Used for the boot
// sweep, which is safe to reclaim content because it runs before the agent
// serves any deploy RPC — no deploy can be in flight. The per-deploy hook
// (triggerImageGCAsync) reclaims snapshots only, since a deploy that re-uses a
// dedup-hit layer never refreshes that blob's gc.root timestamp, so only boot
// quiescence makes blob reclamation provably safe.
func (c *Client) GarbageCollectImages(ctx context.Context) (GCStats, error) {
	if !c.imageGCEnabled {
		return GCStats{}, nil
	}
	if !c.underDiskPressure() {
		return GCStats{}, nil
	}
	if !c.tryStartGC() {
		return GCStats{}, nil
	}
	defer c.finishGC()

	stats, err := c.gcPass(ctx, true)
	if err == nil {
		logGCStats(c.logger, stats)
	}
	return stats, err
}

// triggerImageGCAsync kicks off a snapshot-only mark-and-sweep pass in the
// background after a successful deploy, but only when the containerd filesystem
// is under disk pressure — so normal, roomy deploys never reclaim (and so never
// force a layer to be re-uploaded on the next deploy). It returns immediately so
// it never blocks (or fails) the deploy. The pass runs on a detached,
// time-bounded context because the deploy's request context is gone once the RPC
// returns. Single-flight via the gcRunning guard means concurrent/back-to-back
// deploys never overlap passes. Content blobs are intentionally left to the boot
// sweep (see GarbageCollectImages / runImageGC).
func (c *Client) triggerImageGCAsync() {
	if !c.imageGCEnabled {
		return
	}
	if !c.underDiskPressure() {
		return
	}
	if !c.tryStartGC() {
		return // a pass is already running; it (or the next deploy/boot) covers this
	}
	go func() {
		defer c.finishGC()
		ctx, cancel := context.WithTimeout(context.Background(), imageGCTimeout)
		defer cancel()
		stats, err := c.gcPass(ctx, false)
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
		grace:                 gcRootGrace,
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

// logGCStats emits a summary of a pass that actually reclaimed data or hit
// errors. A fully quiet pass (nothing reclaimed, no errors) stays silent.
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

// withinGrace reports whether a containerd.io/gc.root timestamp label is recent
// enough that its artifact must be spared as potentially belonging to an
// in-flight deploy. An empty or unparseable stamp is treated as NOT within grace
// (i.e. aged): only a live deploy writes a fresh, parseable RFC3339 stamp via
// gcTimestamp, so a bad value cannot belong to one, and reachability has already
// established the artifact is otherwise unreferenced.
func withinGrace(stamp string, now time.Time, grace time.Duration) bool {
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false
	}
	return now.Sub(ts) <= grace
}

// classifySnapshots returns the gc.root snapshots that are safe to reap: those
// not in the reachable set whose EXISTING containerd.io/gc.root commit timestamp
// is older than grace. Reading the gc.root value means no extra bookkeeping
// label is needed. The grace window spares an artifact a racing deploy just
// wrote (its gc.root stamp is fresh) — the only concurrency hazard once the GC
// runs solely under disk pressure. Everything else that is unreferenced is an
// old superseded layer and, under disk pressure, is exactly what we want to
// reclaim.
func classifySnapshots(gcRoots []snapshots.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) []snapshots.Info {
	var reap []snapshots.Info
	for _, info := range gcRoots {
		if _, ok := reachable[info.Name]; ok {
			continue // referenced by a current image or container
		}
		if withinGrace(info.Labels[labelKeyGCRoot], now, grace) {
			continue // recently written -> spare a potentially in-flight deploy's artifact
		}
		reap = append(reap, info)
	}
	return reap
}

// classifyBlobs is the content-blob analogue of classifySnapshots, keyed on the
// blob digest. Reaping is gated to the boot sweep by the caller (per-deploy
// passes never touch content) — see runImageGC.
func classifyBlobs(infos []content.Info, reachable map[string]struct{}, now time.Time, grace time.Duration) []content.Info {
	var reap []content.Info
	for _, info := range infos {
		if _, ok := reachable[info.Digest.String()]; ok {
			continue // referenced by a current image
		}
		if withinGrace(info.Labels[labelKeyGCRoot], now, grace) {
			continue // recently written -> spare a potentially in-flight deploy's blob
		}
		reap = append(reap, info)
	}
	return reap
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
