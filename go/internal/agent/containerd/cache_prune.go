package containerd

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/snapshots"
	digest "github.com/opencontainers/go-digest"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
)

// cachePruneGracePeriod keeps cache roots that may belong to an in-flight
// deployment. A layer push spans multiple RPCs, so there is no single lease
// covering the entire transfer until AssembleImage creates the final image
// reference. A day safely covers even unusually large/slow edge deployments.
const cachePruneGracePeriod = 24 * time.Hour

type cacheContentStore interface {
	Walk(context.Context, content.WalkFunc, ...string) error
	Update(context.Context, content.Info, ...string) (content.Info, error)
}

type cacheSnapshotter interface {
	Walk(context.Context, snapshots.WalkFunc, ...string) error
	Update(context.Context, snapshots.Info, ...string) (snapshots.Info, error)
	Usage(context.Context, string) (snapshots.Usage, error)
}

type contentCacheCandidate struct {
	info content.Info
}

type snapshotCacheCandidate struct {
	info  snapshots.Info
	usage snapshots.Usage
}

// PruneCache releases Wendy's explicit GC roots from cache entries old enough
// not to belong to an in-flight deployment. Containerd's own reference graph
// remains authoritative, preserving current images and active containers while
// its normal GC reclaims entries that are now unreachable.
func (c *Client) PruneCache(ctx context.Context, dryRun bool) (services.CachePruneResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx = c.withNamespace(ctx)
	result, err := pruneCacheRoots(
		ctx,
		c.client.ContentStore(),
		c.client.SnapshotService(c.snapshotter),
		time.Now().Add(-cachePruneGracePeriod),
		dryRun,
	)
	result.MinimumAgeSeconds = uint64(cachePruneGracePeriod / time.Second)
	return result, err
}

func pruneCacheRoots(ctx context.Context, cs cacheContentStore, sn cacheSnapshotter, cutoff time.Time, dryRun bool) (services.CachePruneResult, error) {
	var (
		result             services.CachePruneResult
		contentCandidates  []contentCacheCandidate
		snapshotCandidates []snapshotCacheCandidate
	)

	if err := cs.Walk(ctx, func(info content.Info) error {
		if info.Labels[labelKeyWendyLayer] != "true" || !cacheRootOlderThan(info.Labels[labelKeyGCRoot], cutoff) {
			return nil
		}
		contentCandidates = append(contentCandidates, contentCacheCandidate{info: info})
		result.ContentBlobs++
		if info.Size > 0 {
			result.ContentBytes += uint64(info.Size)
		}
		return nil
	}); err != nil {
		return services.CachePruneResult{}, fmt.Errorf("listing cached content: %w", err)
	}

	if err := sn.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		if info.Kind != snapshots.KindCommitted || !isWendyCacheSnapshot(info) || !cacheRootOlderThan(info.Labels[labelKeyGCRoot], cutoff) {
			return nil
		}
		usage, _ := sn.Usage(ctx, info.Name) // best-effort accounting must not block cleanup
		snapshotCandidates = append(snapshotCandidates, snapshotCacheCandidate{info: info, usage: usage})
		result.Snapshots++
		if usage.Size > 0 {
			result.SnapshotBytes += uint64(usage.Size)
		}
		return nil
	}); err != nil {
		return services.CachePruneResult{}, fmt.Errorf("listing cached snapshots: %w", err)
	}

	if dryRun {
		return result, nil
	}

	for _, candidate := range contentCandidates {
		candidate.info.Labels = labelsWithout(candidate.info.Labels, labelKeyGCRoot)
		if _, err := cs.Update(ctx, candidate.info, "labels"); err != nil {
			return result, fmt.Errorf("releasing cache root from content %s: %w", candidate.info.Digest, err)
		}
	}
	for _, candidate := range snapshotCandidates {
		candidate.info.Labels = labelsWithout(candidate.info.Labels, labelKeyGCRoot)
		if _, err := sn.Update(ctx, candidate.info, "labels"); err != nil {
			return result, fmt.Errorf("releasing cache root from snapshot %s: %w", candidate.info.Name, err)
		}
	}

	return result, nil
}

func cacheRootOlderThan(value string, cutoff time.Time) bool {
	if value == "" {
		return false
	}
	created, err := time.Parse(time.RFC3339, value)
	return err == nil && !created.After(cutoff)
}

func isWendyCacheSnapshot(info snapshots.Info) bool {
	if info.Labels[labelKeyWendySnapshot] == "true" {
		return true
	}
	// Agents predating labelKeyWendySnapshot used the OCI chain ID as the
	// snapshot name and set the same GC root. Recognize that exact legacy shape
	// so upgrades can clean the cache already accumulated on devices.
	d, err := digest.Parse(info.Name)
	return err == nil && d.Algorithm() == digest.SHA256
}

func labelsWithout(labels map[string]string, key string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k != key {
			out[k] = v
		}
	}
	return out
}
