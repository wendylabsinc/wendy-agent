package services

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/mesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// MeshResolver implements mesh.Resolver with the hybrid strategy: an mDNS
// browse (filtered to our own org id) resolves LAN peers cloud-free; anything
// not found on the LAN falls back to the cloud-synced roster cache. OrgSlug is
// always the roster's (cloud-learned) value.
type MeshResolver struct {
	logger   *zap.Logger
	ownOrgID int32
	roster   *MeshRoster
	browse   func(context.Context) ([]models.LANDevice, error)
}

var _ mesh.Resolver = (*MeshResolver)(nil)

func NewMeshResolver(logger *zap.Logger, ownOrgID int32, roster *MeshRoster, browse func(context.Context) ([]models.LANDevice, error)) *MeshResolver {
	return &MeshResolver{logger: logger, ownOrgID: ownOrgID, roster: roster, browse: browse}
}

func (r *MeshResolver) OrgSlug() string { return r.roster.OrgSlug() }

// Resolve returns the asset id for a normalized device name, mDNS first.
func (r *MeshResolver) Resolve(name string) (int32, bool) {
	if id, ok := r.resolveLAN(name); ok {
		return id, true
	}
	return r.roster.Lookup(name)
}

func (r *MeshResolver) resolveLAN(name string) (int32, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	devices, err := r.browse(ctx)
	if err != nil {
		r.logger.Warn("mesh: LAN browse failed for friendly-name resolution", zap.Error(err))
		return 0, false
	}
	var found int32
	matches := 0
	for _, d := range devices {
		if d.OrgID != r.ownOrgID || d.AssetID == 0 {
			continue
		}
		if mesh.Normalize(d.MeshName) != name {
			continue
		}
		if matches == 0 {
			found = d.AssetID
		} else if d.AssetID != found {
			// Two different LAN devices share the name -> ambiguous.
			return 0, false
		}
		matches++
	}
	if matches == 0 {
		return 0, false
	}
	return found, true
}
