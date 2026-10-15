package services

import (
	"context"
	"sync/atomic"
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
	ownOrgID atomic.Int32
	roster   *MeshRoster
	browse   func(context.Context) ([]models.LANDevice, error)
}

var _ mesh.Resolver = (*MeshResolver)(nil)

func NewMeshResolver(logger *zap.Logger, ownOrgID int32, roster *MeshRoster, browse func(context.Context) ([]models.LANDevice, error)) *MeshResolver {
	r := &MeshResolver{logger: logger, roster: roster, browse: browse}
	r.ownOrgID.Store(ownOrgID)
	return r
}

// UpdateOwnOrgID updates the org id used to filter LAN peers. Called when
// BLE first-boot enrollment provisions the device while the agent is
// already running, so the boot-time snapshot (org=0) needs to be replaced
// with the freshly issued org id — otherwise resolveLAN rejects every peer.
func (r *MeshResolver) UpdateOwnOrgID(orgID int32) {
	r.ownOrgID.Store(orgID)
}

func (r *MeshResolver) OrgSlug() string { return r.roster.OrgSlug() }

// Resolve returns the device a normalized name maps to, mDNS first. A LAN
// sighting carries the legacy asset id from its mDNS TXT record — the only
// source the legacy VIP answers are built from. A roster entry carries the
// Cloud v2 UUIDs and no legacy id, so it resolves without a VIP to answer.
func (r *MeshResolver) Resolve(name string) (mesh.ResolvedDevice, bool) {
	if d, ok := r.resolveLAN(name); ok {
		return mesh.ResolvedDevice{AssetID: d.AssetID}, true
	}
	entry, ok := r.roster.Lookup(name)
	if !ok {
		return mesh.ResolvedDevice{}, false
	}
	return mesh.ResolvedDevice{DeviceUUID: entry.DeviceUUID, AssetUUID: entry.AssetUUID}, true
}

// resolveLAN returns the LAN sighting for a normalized name, or ok=false when
// none matches or the name is ambiguous across LAN devices.
func (r *MeshResolver) resolveLAN(name string) (models.LANDevice, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	devices, err := r.browse(ctx)
	if err != nil {
		r.logger.Warn("mesh: LAN browse failed for friendly-name resolution", zap.Error(err))
		return models.LANDevice{}, false
	}
	var found models.LANDevice
	matches := 0
	for _, d := range devices {
		if d.OrgID != r.ownOrgID.Load() || d.AssetID == 0 {
			continue
		}
		if mesh.Normalize(d.MeshName) != name {
			continue
		}
		if matches == 0 {
			found = d
		} else if d.AssetID != found.AssetID {
			// Two different LAN devices share the name -> ambiguous.
			return models.LANDevice{}, false
		}
		matches++
	}
	if matches == 0 {
		return models.LANDevice{}, false
	}
	return found, true
}
