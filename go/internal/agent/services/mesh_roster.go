package services

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wendylabsinc/wendy/go/internal/agent/mesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// RosterEntry is one record of the tenant's name directory: a friendly device
// name plus the two Cloud UUIDs. DeviceUUID is the mesh identity — the SPIFFE
// device component the mesh derives its address from — while AssetUUID names
// the Cloud asset record for asset-targeted RPCs. An empty DeviceUUID means
// the row carries no mesh identity (a path-shaped PKI name): it is never
// dialable, and no address is ever invented for it.
type RosterEntry struct {
	Name       string // normalized DNS label
	DeviceUUID string // canonical lowercase device UUID; "" = no mesh identity
	AssetUUID  string // Cloud asset record id

	device    meshid.Device
	hasDevice bool
}

// Device returns the entry's mesh identity, parsed through the shared meshid
// contract against the roster's authenticated tenant. ok=false when the row
// carries no usable device binding: callers must not invent an address.
func (e RosterEntry) Device() (meshid.Device, bool) { return e.device, e.hasDevice }

// Dialable reports whether this entry can produce a mesh identity at all.
func (e RosterEntry) Dialable() bool { return e.hasDevice }

// MeshRoster caches this device's tenant {normalized name -> roster entry}
// directory and the tenant's own slug, refreshed from the cloud GetMeshRoster
// RPC. It is the off-LAN half of the hybrid friendly-name resolver.
type MeshRoster struct {
	logger   *zap.Logger
	cloudURL string
	certPEM  string
	keyPEM   string
	chainPEM string

	mu        sync.RWMutex
	identity  string        // proof identity read from the leaf cert; "" until provisioned
	tenant    meshid.Tenant // tenant that identity authenticates; valid when hasTenant
	hasTenant bool
	slug      string
	byName    map[string]RosterEntry
	ambiguous map[string]struct{}
}

func NewMeshRoster(logger *zap.Logger, cloudURL, certPEM, keyPEM, chainPEM string) *MeshRoster {
	r := &MeshRoster{
		logger:    logger,
		cloudURL:  cloudURL,
		certPEM:   certPEM,
		keyPEM:    keyPEM,
		chainPEM:  chainPEM,
		byName:    make(map[string]RosterEntry),
		ambiguous: make(map[string]struct{}),
	}
	r.loadIdentity()
	return r
}

// loadIdentity derives the identity used to authenticate GetMeshRoster calls
// from the provisioned leaf certificate: the tenant SPIFFE device principal
// pki-core stamps on every leaf it issues, or the legacy URN on an old-chain
// certificate. An empty identity means the device has no provisioned identity
// yet and Sync will not dial. The caller must hold the write lock.
func (r *MeshRoster) loadIdentity() {
	r.identity, r.tenant, r.hasTenant = "", meshid.Tenant{}, false
	if r.certPEM == "" || r.keyPEM == "" {
		return
	}
	leaves, err := certs.ParseCertsFromPEM([]byte(r.certPEM))
	if err != nil || len(leaves) == 0 {
		r.logger.Warn("mesh roster: device certificate could not be read; sync stays disabled until provisioning completes", zap.Error(err))
		return
	}
	identity, found, err := certs.IdentityFromCert(leaves[0])
	if err != nil {
		r.logger.Warn("mesh roster: device certificate identity could not be parsed; sync stays disabled until provisioning completes", zap.Error(err))
		return
	}
	if !found {
		return
	}
	r.identity = identity.IdentityKey()
	if identity.TenantUUID == "" {
		return
	}
	tenant, err := meshid.ParseTenant(identity.TenantUUID)
	if err != nil {
		r.logger.Warn("mesh roster: certificate tenant UUID is not canonical; roster entries will not be dialable", zap.Error(err))
		return
	}
	r.tenant, r.hasTenant = tenant, true
}

// OrgSlug returns this device's tenant slug as the cloud normalized it.
func (r *MeshRoster) OrgSlug() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.slug
}

// OwnTenant returns the tenant of the provisioned identity the roster
// authenticates with; false until provisioning completes. The friendly-name
// resolver filters LAN sightings through it so tenant scope lives in exactly
// one place.
func (r *MeshRoster) OwnTenant() (meshid.Tenant, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tenant, r.hasTenant
}

// UpdateIdentity swaps the identity used to authenticate GetMeshRoster calls.
// Called when BLE first-boot enrollment provisions the device while the agent
// is already running, so the boot-time snapshot (no certificate, no principal)
// needs to be replaced with the freshly issued identity. The cached entries are
// dropped with it: they were scoped to whatever tenant the previous identity
// authenticated — possibly none — and a tenant change must never serve stale
// names from the old scope.
func (r *MeshRoster) UpdateIdentity(cloudURL, certPEM, keyPEM, chainPEM string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cloudURL, r.certPEM, r.keyPEM, r.chainPEM = cloudURL, certPEM, keyPEM, chainPEM
	r.loadIdentity()
	r.slug = ""
	r.byName = make(map[string]RosterEntry)
	r.ambiguous = make(map[string]struct{})
}

// Lookup returns the roster entry for a normalized device name. Unknown and
// ambiguous (duplicate-normalized) names return ok=false.
func (r *MeshRoster) Lookup(name string) (RosterEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, dup := r.ambiguous[name]; dup {
		return RosterEntry{}, false
	}
	entry, ok := r.byName[name]
	return entry, ok
}

// applyResponse rebuilds the cache from a roster response. Names that collide
// after normalization are recorded as ambiguous and never resolve. Device
// UUIDs become mesh identities through meshid against the authenticated
// tenant; rows with no device UUID — or a device component that is not a
// canonical UUID — stay in the directory as non-dialable rather than acquiring
// an invented address.
func (r *MeshRoster) applyResponse(resp *cloudpbv2.GetMeshRosterResponse) {
	r.mu.RLock()
	tenant, hasTenant := r.tenant, r.hasTenant
	r.mu.RUnlock()

	byName := make(map[string]RosterEntry, len(resp.GetEntries()))
	ambiguous := make(map[string]struct{})
	for _, e := range resp.GetEntries() {
		n := mesh.Normalize(e.GetName())
		if n == "" {
			continue
		}
		entry := RosterEntry{Name: n, DeviceUUID: e.GetDeviceUuid(), AssetUUID: e.GetAssetId()}
		if hasTenant && entry.DeviceUUID != "" {
			if device, err := meshid.ParseDevice(tenant, entry.DeviceUUID); err == nil {
				entry.device, entry.hasDevice = device, true
			}
		}
		if existing, ok := byName[n]; ok && existing.AssetUUID != entry.AssetUUID {
			ambiguous[n] = struct{}{}
			continue
		}
		byName[n] = entry
	}
	r.mu.Lock()
	r.slug = mesh.Normalize(resp.GetOrgSlug())
	r.byName = byName
	r.ambiguous = ambiguous
	r.mu.Unlock()
}

// Sync performs one GetMeshRoster refresh over the cloud gRPC endpoint using
// the same certificate-bound identity the tunnel broker client uses; the cloud
// scopes the roster it returns to the tenant that identity authenticates.
func (r *MeshRoster) Sync(ctx context.Context) error {
	r.mu.RLock()
	cloudURL, identity := r.cloudURL, r.identity
	certPEM, keyPEM, chainPEM := r.certPEM, r.keyPEM, r.chainPEM
	r.mu.RUnlock()

	if identity == "" {
		// No provisioned device identity yet: there is no certificate to
		// authenticate a roster call with, so don't dial at all — this is
		// expected before enrollment (and briefly during BLE first-boot
		// provisioning) and would otherwise just fail the RPC every tick.
		r.logger.Debug("mesh roster: skipping sync, device identity not provisioned")
		return nil
	}

	dialOpts, requestMetadata, err := brokerIdentityDialOpts(r.logger, identity, certPEM, keyPEM, chainPEM)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cloudURL, dialOpts...)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := cloudpbv2.NewMeshRosterServiceClient(conn)
	md, err := requestMetadata(cloudpbv2.MeshRosterService_GetMeshRoster_FullMethodName)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), 10*time.Second)
	defer cancel()
	resp, err := client.GetMeshRoster(callCtx, &cloudpbv2.GetMeshRosterRequest{})
	if err != nil {
		return err
	}
	r.applyResponse(resp)
	return nil
}

// RunSync refreshes immediately, then every interval, until ctx is done.
// Failures are logged and retried on the next tick; the cache keeps its last
// good contents so a transient cloud outage never breaks LAN-cached names.
func (r *MeshRoster) RunSync(ctx context.Context, interval time.Duration) {
	if err := r.Sync(ctx); err != nil {
		r.logger.Warn("mesh roster initial sync failed", zap.Error(err))
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Sync(ctx); err != nil {
				r.logger.Warn("mesh roster sync failed", zap.Error(err))
			}
		}
	}
}
