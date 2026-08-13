package containerd

import (
	"context"
	"fmt"
	"sync"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"
)

// unpackLeaseExpiration bounds how long the unpack lease keeps freshly created
// snapshots and content alive. It must be long enough to apply the largest
// expected image, but short enough that a lease orphaned by a crashed agent
// doesn't pin disk for too long. The expiration is a backstop only — the
// happy path releases the lease on return.
const unpackLeaseExpiration = 30 * time.Minute

// UnpackProgress reports progress during the image unpack operation.
type UnpackProgress struct {
	// Phase is one of "start", "layer-start", "layer", "complete".
	Phase string
	// LayerIndex is the zero-based index of the current layer being unpacked.
	LayerIndex int
	// TotalLayers is the total number of layers in the image.
	TotalLayers int
	// LayerSize is the compressed size of the current layer in bytes.
	LayerSize int64
	// Reused indicates whether the layer snapshot already existed and was reused.
	Reused bool
}

// snapshotStatter is the subset of snapshots.Snapshotter used for existence checks.
type snapshotStatter interface {
	Stat(ctx context.Context, key string) (snapshots.Info, error)
}

// statLayers checks which chain-ID snapshots already exist by fanning out
// sn.Stat calls concurrently. Returns a bool slice indexed by layer position.
// Any non-NotFound error from any goroutine is returned (first one wins).
func statLayers(ctx context.Context, sn snapshotStatter, chainIDs []string) ([]bool, error) {
	exists := make([]bool, len(chainIDs))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for i, id := range chainIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sn.Stat(ctx, id)
			if err == nil {
				exists[i] = true
			} else if !errdefs.IsNotFound(err) {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return exists, firstErr
}

// UnpackImage unpacks an image's layers into the snapshotter so that the
// resulting chain-ID snapshots are present for a subsequent
// `WithNewSnapshot` call to build a container rootfs from. It computes chain
// IDs for each layer and creates snapshots incrementally, reusing existing
// snapshots when possible.
//
// The progress callback, if non-nil, is invoked for each phase of the unpack
// operation to allow callers to report progress upstream.
//
// The unpack runs inside a containerd lease and tags each committed snapshot
// with a `containerd.io/gc.root` label. These work as a pair: the lease pins
// content and active snapshots while the unpack is in progress, and the
// gc.root label on each committed chain-ID snapshot keeps it alive after the
// lease releases. Without both, containerd's metadata GC could reap a
// freshly committed chain-ID snapshot before the next layer's `Prepare`
// references it as a parent — surfacing as a random-layer "parent snapshot
// does not exist" failure.
func (c *Client) UnpackImage(ctx context.Context, img containerd.Image, progress func(UnpackProgress)) error {
	ctx = c.withNamespace(ctx)

	// cleanupCtx is used for best-effort `Remove` calls so a cancelled caller
	// doesn't also cancel cleanup. The namespace is required by containerd
	// services even off the happy path.
	cleanupCtx := c.withNamespace(context.Background())

	ctx, doneLease, err := c.client.WithLease(ctx, leases.WithExpiration(unpackLeaseExpiration))
	if err != nil {
		return fmt.Errorf("creating unpack lease: %w", err)
	}
	defer func() {
		if err := doneLease(cleanupCtx); err != nil {
			c.logger.Warn("Failed to release unpack lease; relying on expiration backstop",
				zap.Duration("expiration", unpackLeaseExpiration),
				zap.Error(err),
			)
		}
	}()

	cs := c.client.ContentStore()
	sn := c.client.SnapshotService(c.snapshotter)

	// Resolve through index if needed (platform selection).
	manifest, err := images.Manifest(ctx, cs, img.Target(), img.Platform())
	if err != nil {
		return fmt.Errorf("reading manifest for %q: %w", img.Name(), err)
	}

	// Read all diff IDs from the image config in a single cheap blob read.
	// images.GetDiffID resolves each diff ID by decompressing the layer blob
	// when the containerd.io/uncompressed label is absent — the same bytes
	// DiffService.Apply will decompress again. Reading from the config avoids
	// that double-decompression on every first-time unpack.
	diffIDs, err := images.RootFS(ctx, cs, manifest.Config)
	if err != nil {
		return fmt.Errorf("reading diff IDs for %q: %w", img.Name(), err)
	}
	if len(diffIDs) != len(manifest.Layers) {
		return fmt.Errorf("image %q has %d layers but %d diff IDs", img.Name(), len(manifest.Layers), len(diffIDs))
	}

	totalLayers := len(manifest.Layers)
	if progress != nil {
		progress(UnpackProgress{Phase: "start", TotalLayers: totalLayers})
	}

	// Pre-compute all chain IDs in a single pass, then check existence in parallel.
	chainIDs := make([]string, len(diffIDs))
	parent := ""
	for i, diffID := range diffIDs {
		chainIDs[i] = computeChainID(parent, diffID.String())
		parent = chainIDs[i]
	}

	exists, err := statLayers(ctx, sn, chainIDs)
	if err != nil {
		return fmt.Errorf("pre-checking layer snapshots: %w", err)
	}

	var parentChainID string
	for i, layerDesc := range manifest.Layers {
		chainID := chainIDs[i]

		if exists[i] {
			c.refreshSnapshotLabels(ctx, sn, chainID)
			if progress != nil {
				progress(UnpackProgress{
					Phase:       "layer",
					LayerIndex:  i,
					TotalLayers: totalLayers,
					LayerSize:   layerDesc.Size,
					Reused:      true,
				})
			}
			c.logger.Debug("Reusing existing snapshot",
				zap.Int("layer", i),
				zap.String("chain_id", chainID),
			)
			parentChainID = chainID
			continue
		}

		if progress != nil {
			progress(UnpackProgress{
				Phase:       "layer-start",
				LayerIndex:  i,
				TotalLayers: totalLayers,
				LayerSize:   layerDesc.Size,
			})
		}

		reused, err := c.applyLayerSnapshot(ctx, cleanupCtx, sn, img.Name(), i, layerDesc, diffIDs[i].String(), parentChainID, chainID)
		if err != nil {
			return err
		}
		if progress != nil {
			progress(UnpackProgress{
				Phase:       "layer",
				LayerIndex:  i,
				TotalLayers: totalLayers,
				LayerSize:   layerDesc.Size,
				Reused:      reused,
			})
		}

		parentChainID = chainID
	}

	if progress != nil {
		progress(UnpackProgress{Phase: "complete", TotalLayers: totalLayers})
	}

	return nil
}

// applyLayerSnapshot materializes one layer descriptor and commits it above
// parentChainID under the ordered chainID. The caller must hold a containerd
// lease. Returning reused=true means either the immutable layer data or the
// complete ordered chain was already available.
func (c *Client) applyLayerSnapshot(
	ctx, cleanupCtx context.Context,
	sn snapshots.Snapshotter,
	imageName string,
	layer int,
	layerDesc ocispec.Descriptor,
	diffID string,
	parentChainID, chainID string,
) (reused bool, err error) {
	if c.tryReuseLayerSnapshot(ctx, cleanupCtx, sn, imageName, layer, diffID, parentChainID, chainID) {
		return true, nil
	}
	if c.snapshotter == "overlayfs" {
		reused, err := c.applyLayerSnapshotAttempt(ctx, cleanupCtx, sn, imageName, layer, layerDesc, diffID, parentChainID, chainID, true)
		if err == nil {
			return reused, nil
		}
		if ctx.Err() != nil {
			return false, err
		}
		// Overlay snapshotters running without the advertised rebase capability
		// (notably some user namespaces) reject WithParent at commit. Preserve
		// compatibility by replaying the canonical sequential apply path.
		c.logger.Debug("Parent-independent layer apply unavailable; applying layer sequentially",
			zap.Int("layer", layer), zap.String("diff_id", diffID), zap.Error(err))
	}
	return c.applyLayerSnapshotAttempt(ctx, cleanupCtx, sn, imageName, layer, layerDesc, diffID, parentChainID, chainID, false)
}

func (c *Client) applyLayerSnapshotAttempt(
	ctx, cleanupCtx context.Context,
	sn snapshots.Snapshotter,
	imageName string,
	layer int,
	layerDesc ocispec.Descriptor,
	diffID string,
	parentChainID, chainID string,
	standalone bool,
) (reused bool, err error) {
	snapshotParent := parentChainID
	if standalone {
		snapshotParent = ""
	}

	// Unique per-attempt active key so concurrent unpacks of the same image (or
	// a stale key from a crashed prior attempt) cannot collide or clobber each
	// other's in-progress snapshot.
	activeKey := fmt.Sprintf("extract-%s-%d-%s", imageName, layer, uuid.NewString())
	mounts, err := sn.Prepare(ctx, activeKey, snapshotParent)
	if err != nil {
		return false, fmt.Errorf("preparing snapshot for layer %d: %w", layer, err)
	}

	if standalone && parentChainID != "" {
		mounts = standaloneLayerApplyMounts(mounts)
	}
	if _, err := c.client.DiffService().Apply(ctx, layerDesc, mounts); err != nil {
		c.removeActiveSnapshot(cleanupCtx, sn, activeKey, "active snapshot after apply failure", layer)
		return false, fmt.Errorf("applying layer %d: %w", layer, err)
	}

	labels := map[string]string{
		labelKeyGCRoot:        gcTimestamp(),
		labelKeyWendySnapshot: "true",
	}
	if standalone {
		// Only parent-independent uppers receive this label. Snapshots produced
		// by the sequential fallback may contain metadata copied from their
		// parent and must never be reused above another chain.
		labels[labelKeyWendyDiffID] = diffID
	}
	commitOpts := []snapshots.Opt{snapshots.WithLabels(labels)}
	if standalone && parentChainID != "" {
		commitOpts = append(commitOpts, snapshots.WithParent(parentChainID))
	}
	commitErr := sn.Commit(ctx, chainID, activeKey, commitOpts...)
	switch {
	case commitErr == nil:
		c.logger.Debug("Unpacked layer",
			zap.Int("layer", layer),
			zap.String("chain_id", chainID),
			zap.Int64("size", layerDesc.Size),
		)
		return false, nil
	case errdefs.IsAlreadyExists(commitErr):
		// A concurrent unpack committed the same chain ID first. Our active
		// key still exists and must be removed.
		c.removeActiveSnapshot(cleanupCtx, sn, activeKey, "active snapshot after concurrent commit", layer)
		return true, nil
	default:
		c.removeActiveSnapshot(cleanupCtx, sn, activeKey, "active snapshot after commit failure", layer)
		return false, fmt.Errorf("committing snapshot for layer %d: %w", layer, commitErr)
	}
}

// removeActiveSnapshot deletes an active snapshot key as part of error recovery,
// logging at Warn for any failure other than NotFound (which is benign — the
// key was never created or has already been swept).
func (c *Client) removeActiveSnapshot(ctx context.Context, sn snapshots.Snapshotter, key, reason string, layer int) {
	if err := sn.Remove(ctx, key); err != nil && !errdefs.IsNotFound(err) {
		c.logger.Warn("Failed to remove active snapshot",
			zap.String("active_key", key),
			zap.String("reason", reason),
			zap.Int("layer", layer),
			zap.Error(err),
		)
	}
}
