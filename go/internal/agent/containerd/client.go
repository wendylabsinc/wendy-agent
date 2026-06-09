package containerd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cgroupv1 "github.com/containerd/cgroups/v3/cgroup1/stats"
	cgroupv2 "github.com/containerd/cgroups/v3/cgroup2/stats"
	tasks "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/api/types"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/cdi"
	"github.com/wendylabsinc/wendy/go/internal/agent/dbusproxy"
	localoci "github.com/wendylabsinc/wendy/go/internal/agent/oci"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	sharedenv "github.com/wendylabsinc/wendy/go/internal/shared/env"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Compile-time check that *Client satisfies services.ContainerdClient.
var _ services.ContainerdClient = (*Client)(nil)

// DefaultAddress is the default containerd socket path on Linux.
const DefaultAddress = "/run/containerd/containerd.sock"

type Client struct {
	client       *containerd.Client
	logger       *zap.Logger
	namespace    string
	mu           sync.Mutex
	proxyManager *dbusproxy.Manager // nil if xdg-dbus-proxy is not available

	// appServices caches the services map for multi-service apps, keyed by appID.
	// Populated on CreateContainerWithProgress; used by resolveStopOrder.
	appServices map[string]map[string]*appconfig.ServiceConfig

	// primaryPIDs tracks the PID of the primary (namespace-owner) container
	// for each shared-namespace app group. Protected by mu.
	primaryPIDs map[string]uint32

	// appIsolation caches the isolation mode for each appID.
	// Populated on CreateContainerWithProgress; read by StartContainer.
	appIsolation map[string]string

	// serviceIPs maps appID → serviceName → IP for isolated-mode apps.
	// Updated after each successful CNI ADD. Protected by mu.
	serviceIPs map[string]map[string]string

	// appStopping tracks appIDs that are currently being stopped.
	// Set before releasing c.mu in StopContainer; cleared in the cleanup phase.
	// Checked by CreateContainerWithProgress to reject concurrent create/stop races
	// (SOC2-CC6, NIST-AC-3, ISO27001-A.8).
	appStopping map[string]bool
}

func NewClient(logger *zap.Logger, address string, proxyMgr *dbusproxy.Manager) (*Client, error) {
	if address == "" {
		address = DefaultAddress
	}

	c, err := containerd.New(address)
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", address, err)
	}

	return &Client{
		client:       c,
		logger:       logger,
		namespace:    "default",
		proxyManager: proxyMgr,
		appServices:  make(map[string]map[string]*appconfig.ServiceConfig),
		primaryPIDs:  make(map[string]uint32),
		appIsolation: make(map[string]string),
		serviceIPs:   make(map[string]map[string]string),
		appStopping:  make(map[string]bool),
	}, nil
}

// Close releases the underlying containerd client connection and stops all
// D-Bus proxy processes.
func (c *Client) Close() error {
	if c.proxyManager != nil {
		c.proxyManager.StopAll()
	}
	return c.client.Close()
}

func (c *Client) withNamespace(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, c.namespace)
}

// setPrimaryPID records the PID of the primary container for appID.
// Caller must hold c.mu.
func (c *Client) setPrimaryPID(appID string, pid uint32) {
	if c.primaryPIDs == nil {
		c.primaryPIDs = make(map[string]uint32)
	}
	c.primaryPIDs[appID] = pid
}

// getPrimaryPID returns the PID of the primary container, if known.
// Caller must hold c.mu.
func (c *Client) getPrimaryPID(appID string) (uint32, bool) {
	pid, ok := c.primaryPIDs[appID]
	return pid, ok
}

// clearPrimaryPID removes the primary PID entry when the app group stops.
// Caller must hold c.mu.
func (c *Client) clearPrimaryPID(appID string) {
	delete(c.primaryPIDs, appID)
}

// getIsolation returns the cached isolation mode for appID. Caller must hold c.mu.
func (c *Client) getIsolation(appID string) string {
	return c.appIsolation[appID]
}

// recordServiceIP stores the CNI-assigned IP for a service. Caller must hold c.mu.
func (c *Client) recordServiceIP(appID, serviceName, ip string) {
	if c.serviceIPs == nil {
		c.serviceIPs = make(map[string]map[string]string)
	}
	if c.serviceIPs[appID] == nil {
		c.serviceIPs[appID] = make(map[string]string)
	}
	c.serviceIPs[appID][serviceName] = ip
}

// ListLayers walks the content store and returns metadata for all layer blobs.
func (c *Client) ListLayers(ctx context.Context) ([]*agentpb.LayerHeader, error) {
	ctx = c.withNamespace(ctx)
	cs := c.client.ContentStore()

	var layers []*agentpb.LayerHeader
	err := cs.Walk(ctx, func(info content.Info) error {
		// Include blobs that are tagged as wendy layers or have a layer media type.
		if info.Labels[labelKeyWendyLayer] == "true" || isLayerDigest(info) {
			layers = append(layers, &agentpb.LayerHeader{
				Digest: info.Digest.String(),
				Size:   info.Size,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking content store: %w", err)
	}

	return layers, nil
}

// isLayerDigest checks if a content info entry looks like a layer by inspecting
// its labels for known layer media type indicators.
func isLayerDigest(info content.Info) bool {
	for k, v := range info.Labels {
		if strings.HasPrefix(k, "containerd.io/distribution.source") {
			_ = v
			continue
		}
		// Labels set by image handlers for layer children include media type info.
		if strings.Contains(v, "diff.tar") || strings.Contains(v, "layer") {
			return true
		}
	}
	return false
}

func (c *Client) WriteLayer(ctx context.Context, dgst string, reader io.Reader, size int64) error {
	ctx = c.withNamespace(ctx)
	cs := c.client.ContentStore()

	expected, err := digest.Parse(dgst)
	if err != nil {
		return fmt.Errorf("parsing digest %q: %w", dgst, err)
	}

	labels := map[string]string{
		labelKeyGCRoot:     gcTimestamp(),
		labelKeyWendyLayer: "true",
	}

	err = content.WriteBlob(ctx, cs, dgst, reader, ocispec.Descriptor{
		Digest: expected,
		Size:   size,
	}, content.WithLabels(labels))
	if err != nil {
		// If the blob already exists, that is fine.
		if errdefs.IsAlreadyExists(err) {
			c.logger.Debug("Layer already exists in content store",
				zap.String("digest", dgst),
			)
			return nil
		}
		return fmt.Errorf("writing layer %s: %w", dgst, err)
	}

	c.logger.Info("Wrote layer to content store",
		zap.String("digest", dgst),
		zap.Int64("size", size),
	)
	return nil
}

func layerMediaType(compression agentpb.RunContainerLayerHeader_CompressionType, gzip bool) string {
	switch compression {
	case agentpb.RunContainerLayerHeader_COMPRESSION_ZSTD:
		return ocispec.MediaTypeImageLayerZstd
	case agentpb.RunContainerLayerHeader_COMPRESSION_NONE:
		return ocispec.MediaTypeImageLayer
	default: // COMPRESSION_GZIP (0) or unrecognised
		if gzip {
			return ocispec.MediaTypeImageLayerGzip
		}
		return ocispec.MediaTypeImageLayer
	}
}

func (c *Client) AssembleImage(ctx context.Context, imageName string, layers []*agentpb.RunContainerLayerHeader) error {
	ctx = c.withNamespace(ctx)
	cs := c.client.ContentStore()
	is := c.client.ImageService()

	// Build OCI layer descriptors and diff IDs.
	var layerDescs []ocispec.Descriptor
	var diffIDs []digest.Digest
	for _, l := range layers {
		mediaType := layerMediaType(l.GetCompression(), l.GetGzip())

		dgst, err := digest.Parse(l.GetDigest())
		if err != nil {
			return fmt.Errorf("parsing layer digest %q: %w", l.GetDigest(), err)
		}

		layerDescs = append(layerDescs, ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    dgst,
			Size:      l.GetSize(),
		})

		diffID := l.GetDiffId()
		if diffID == "" {
			diffID = l.GetDigest()
		}
		did, err := digest.Parse(diffID)
		if err != nil {
			return fmt.Errorf("parsing diff ID %q: %w", diffID, err)
		}
		diffIDs = append(diffIDs, did)
	}

	// Build OCI image config.
	imgConfig := ocispec.Image{
		Platform: ocispec.Platform{
			Architecture: "arm64",
			OS:           "linux",
		},
		RootFS: ocispec.RootFS{
			Type:    "layers",
			DiffIDs: diffIDs,
		},
	}
	configData, err := json.Marshal(imgConfig)
	if err != nil {
		return fmt.Errorf("marshaling image config: %w", err)
	}
	configDigest := digest.FromBytes(configData)

	// Write config to content store.
	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    configDigest,
		Size:      int64(len(configData)),
	}
	if err := content.WriteBlob(ctx, cs, configDigest.String(), bytes.NewReader(configData), configDesc); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("writing config blob: %w", err)
		}
	}

	// Build OCI manifest.
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    layerDescs,
	}
	manifest.SchemaVersion = 2
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	manifestDigest := digest.FromBytes(manifestData)

	// Write manifest to content store.
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestData)),
	}
	if err := content.WriteBlob(ctx, cs, manifestDigest.String(), bytes.NewReader(manifestData), manifestDesc); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("writing manifest blob: %w", err)
		}
	}

	// Create or update the image in the image store.
	_, err = is.Create(ctx, images.Image{
		Name:   imageName,
		Target: manifestDesc,
	})
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			_, err = is.Update(ctx, images.Image{
				Name:   imageName,
				Target: manifestDesc,
			})
			if err != nil {
				return fmt.Errorf("updating image %q: %w", imageName, err)
			}
		} else {
			return fmt.Errorf("creating image %q: %w", imageName, err)
		}
	}

	c.logger.Info("Assembled image",
		zap.String("name", imageName),
		zap.Int("layers", len(layers)),
		zap.String("manifest_digest", manifestDigest.String()),
	)
	return nil
}

// wrapWithDebugpy modifies the command args to run through debugpy for remote debugging.
// It injects "-m debugpy --listen 0.0.0.0:5678" after the Python binary.
func wrapWithDebugpy(args []string) []string {
	debugpyArgs := []string{"-m", "debugpy", "--listen", "0.0.0.0:5678"}

	if len(args) > 0 {
		base := args[0]
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base == "python" || base == "python3" || strings.HasPrefix(base, "python3.") {
			// python3 app.py -> python3 -m debugpy --listen 0.0.0.0:5678 app.py
			result := make([]string, 0, len(args)+len(debugpyArgs))
			result = append(result, args[0])
			result = append(result, debugpyArgs...)
			result = append(result, args[1:]...)
			return result
		}
	}

	// No python binary found; prepend python3 -m debugpy.
	result := make([]string, 0, len(args)+len(debugpyArgs)+1)
	result = append(result, "python3")
	result = append(result, debugpyArgs...)
	result = append(result, args...)
	return result
}

// CreateContainer creates (or replaces) a container in containerd for the given
// app. It builds an OCI runtime specification from the app config and request
// parameters, unpacks the image, and registers the container.
func (c *Client) CreateContainer(ctx context.Context, req *agentpb.CreateContainerRequest, appCfg *appconfig.AppConfig) error {
	return c.CreateContainerWithProgress(ctx, req, appCfg, nil)
}

func toCreateContainerProgress(progress UnpackProgress) *agentpb.CreateContainerProgress {
	switch progress.Phase {
	case "start":
		return &agentpb.CreateContainerProgress{
			Phase:       agentpb.CreateContainerProgress_UNPACKING,
			TotalLayers: int32(progress.TotalLayers),
		}
	case "layer-start":
		return &agentpb.CreateContainerProgress{
			Phase:       agentpb.CreateContainerProgress_UNPACKING,
			LayerIndex:  int32(progress.LayerIndex),
			TotalLayers: int32(progress.TotalLayers),
			LayerSize:   progress.LayerSize,
		}
	case "layer":
		return &agentpb.CreateContainerProgress{
			Phase:          agentpb.CreateContainerProgress_APPLYING_LAYER,
			LayerIndex:     int32(progress.LayerIndex),
			TotalLayers:    int32(progress.TotalLayers),
			LayerSize:      progress.LayerSize,
			ReusedSnapshot: progress.Reused,
		}
	default:
		return nil
	}
}

func (c *Client) CreateContainerWithProgress(ctx context.Context, req *agentpb.CreateContainerRequest, appCfg *appconfig.AppConfig, onProgress services.ProgressFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx = c.withNamespace(ctx)

	// Derive the app identity. appCfg.AppID is the authoritative source; fall
	// back to req.GetAppName() for raw RPC calls that arrive without a parsed
	// AppConfig. We use a local variable (not a struct mutation) so the caller's
	// AppConfig is unchanged and concurrent/retry uses see a stable value.
	//
	// Validate before assigning to the named variables so that no unvalidated
	// RPC-controlled value ever reaches downstream helpers, even if future
	// refactors reorder code below this block (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
	rawAppID := appCfg.AppID
	if rawAppID == "" {
		rawAppID = req.GetAppName()
	}
	rawServiceName := appCfg.ServiceName

	if err := appconfig.ValidateAppID(rawAppID); err != nil {
		c.logger.Warn("CreateContainer rejected: invalid app ID",
			zap.String("app_id", sanitizeForLog(rawAppID, 253)), zap.Error(err))
		return fmt.Errorf("invalid app ID: %w", err)
	}
	if rawServiceName != "" {
		if err := appconfig.ValidateServiceName(rawServiceName); err != nil {
			c.logger.Warn("CreateContainer rejected: invalid service name",
				zap.String("app_id", sanitizeForLog(rawAppID, 253)),
				zap.String("service_name", sanitizeForLog(rawServiceName, 57)),
				zap.Error(err))
			return fmt.Errorf("invalid service name: %w", err)
		}
	}

	// Both values are now validated; promote to short names for readability.
	appID, serviceName := rawAppID, rawServiceName

	// Reject creation while a concurrent StopContainer is tearing down this app.
	// Without this check a new container could be created after resolveStopOrder
	// snapshots the container list, leaving it running after StopContainer returns
	// (TOCTOU; SOC2-CC6, NIST-AC-3, ISO27001-A.8).
	if c.appStopping[appID] {
		return fmt.Errorf("app %q is currently being stopped; retry after stop completes", appID)
	}

	containerName := ContainerName(appID, serviceName)

	// Canonicalise the image reference so older CLIs sending Docker short
	// names like "python:3.11-slim" still resolve correctly under containerd's
	// strict parser, which would otherwise read "3.11-slim" as a port.
	imageName := normalizeImageName(req.GetImageName())

	report := func(p *agentpb.CreateContainerProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	logFields := []zap.Field{
		zap.String("container_name", containerName),
		zap.String("app_id", appID),
		zap.String("image", imageName),
	}
	if serviceName != "" {
		logFields = append(logFields, zap.String("service_name", serviceName))
	}
	c.logger.Info("Creating container", logFields...)

	// Determine version from the app config or default.
	version := appCfg.Version
	if version == "" {
		version = "latest"
	}

	// Delete any pre-existing container with the same name.
	if existing, err := c.client.LoadContainer(ctx, containerName); err == nil {
		c.logger.Info("Removing existing container", zap.String("container_name", containerName))
		// Try to stop/kill the task first.
		if task, taskErr := existing.Task(ctx, nil); taskErr == nil {
			_ = task.Kill(ctx, syscall.SIGKILL)
			_, _ = task.Delete(ctx, containerd.WithProcessKill)
		} else {
			// Task may be orphaned (shim crashed). Force-delete via the task
			// service directly so the runtime clears the old task ID.
			c.forceDeleteTask(ctx, containerName)
		}
		_ = existing.Delete(ctx, containerd.WithSnapshotCleanup)
		// Stop old D-Bus proxy if any.
		if c.proxyManager != nil {
			_ = c.proxyManager.Stop(containerName)
		}
	}

	// Try the local image store first. The device-local registry shares
	// containerd's content store, so anything just pushed to it is already
	// available via GetImage — pulling would just round-trip bytes over
	// loopback. Fall back to a pull only on miss; use PlainHTTP for the
	// local-registry case.
	var image containerd.Image
	var err error
	report(&agentpb.CreateContainerProgress{Phase: agentpb.CreateContainerProgress_UNPACKING})
	image, err = c.client.GetImage(ctx, imageName)
	if err != nil {
		c.logger.Info("Image not in local store, attempting pull from registry",
			zap.String("image", imageName),
		)
		pullOpts := []containerd.RemoteOpt{containerd.WithPullUnpack}
		if isLocalRegistryImage(imageName) {
			pullOpts = append(pullOpts,
				containerd.WithResolver(docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})),
			)
		}
		image, err = c.client.Pull(ctx, imageName, pullOpts...)
		if err != nil {
			return fmt.Errorf("getting/pulling image %q: %w", imageName, err)
		}
	}

	// Start D-Bus proxy if bluetooth entitlement is present.
	var dbusProxyStarted bool
	if c.proxyManager != nil && hasBluetooth(appCfg) {
		if _, err := c.proxyManager.Start(ctx, containerName); err != nil {
			return fmt.Errorf("starting D-Bus proxy for %q: %w", containerName, err)
		}
		dbusProxyStarted = true
		defer func() {
			if dbusProxyStarted {
				_ = c.proxyManager.Stop(containerName)
			}
		}()
	}

	// Unpack the image into the snapshotter if not already done.
	unpacked, err := image.IsUnpacked(ctx, "")
	if err != nil {
		c.logger.Warn("Failed to check if image is unpacked", zap.Error(err))
	}
	if !unpacked {
		c.logger.Info("Unpacking image", zap.String("image", imageName))
		if err := c.UnpackImage(ctx, image, func(progress UnpackProgress) {
			if mapped := toCreateContainerProgress(progress); mapped != nil {
				report(mapped)
			}
		}); err != nil {
			return fmt.Errorf("unpacking image %q: %w", imageName, err)
		}
	}

	// Read the image's OCI config (CMD, ENTRYPOINT, ENV, WorkingDir).
	imageSpec, specErr := image.Spec(ctx)
	if specErr != nil {
		c.logger.Warn("Failed to read image spec, using defaults", zap.Error(specErr))
	}

	// Build the container command: explicit request > image config > /bin/sh.
	var args []string
	cmd := req.GetCmd()
	if cmd != "" {
		args = strings.Fields(cmd)
	}
	if len(req.GetUserArgs()) > 0 {
		args = append(args, req.GetUserArgs()...)
	}
	if len(args) == 0 && specErr == nil {
		args = append(imageSpec.Config.Entrypoint, imageSpec.Config.Cmd...)
	}
	if len(args) == 0 {
		args = []string{"/bin/sh"}
	}

	// Wrap Python commands with debugpy for remote debugging (only in debug mode).
	if appCfg.Debug && appCfg.Language == "python" {
		args = wrapWithDebugpy(args)
	}

	// Build the working directory: explicit request > image config > /.
	workingDir := req.GetWorkingDir()
	if workingDir == "" && specErr == nil && imageSpec.Config.WorkingDir != "" {
		workingDir = imageSpec.Config.WorkingDir
	}
	if workingDir == "" {
		workingDir = "/"
	}

	// Build environment variables.
	// Order: image built-in env → user-provided env (from request) → Wendy system env → OTEL injection.
	// Wendy vars appear last so they always win in OCI semantics (last KEY wins).
	wendyEnv, err := buildContainerBaseEnv(appID, serviceName)
	if err != nil {
		return fmt.Errorf("building container env: %w", err)
	}
	if err := validateUserEnv(req.GetEnv()); err != nil {
		return fmt.Errorf("invalid env var in request (SOC2-CC6, NIST-SI-10): %w", err)
	}
	var env []string
	if specErr == nil {
		env = append(env, imageSpec.Config.Env...)
	}
	env = append(env, req.GetEnv()...)
	env = append(env, wendyEnv...)
	env = append(env, buildROS2Env(appCfg)...)
	env = injectOTELEnvIfNeeded(env, appCfg, appID)

	// Build OCI spec using local oci package, then apply entitlements.
	spec := localoci.DefaultSpec("rootfs", args)
	spec.Process.Cwd = workingDir
	spec.Process.Env = env
	if spec.Linux == nil {
		spec.Linux = &localoci.Linux{}
	}

	// Apply the NVIDIA CDI spec before entitlements so that entitlements can
	// override CDI-injected env vars (e.g. NVIDIA_VISIBLE_DEVICES=void → =all).
	if appCfg.HasEntitlement(appconfig.EntitlementGPU) {
		c.applyCDIGPU(spec)
	}

	opts := localoci.ApplyOptions{
		DBusProxyAvailable: c.proxyManager != nil,
	}
	// Pass a shallow copy of appCfg with AppID and ServiceName set to the
	// derived (validated) values. This ensures ApplyEntitlements always receives
	// a non-empty AppID even when the caller used the raw-RPC fallback path
	// where appCfg.AppID was empty and appID was derived from req.GetAppName().
	entCfg := *appCfg
	entCfg.AppID = appID
	entCfg.ServiceName = serviceName
	if err := localoci.ApplyEntitlements(spec, &entCfg, opts); err != nil {
		return fmt.Errorf("applying entitlements: %w", err)
	}

	// Set the cgroup path here — client.go is the sole authority so there is
	// no risk of divergence with entitlements.go. SetDeviceCapabilities only
	// adds the cgroup namespace and mount; it no longer sets CgroupsPath.
	// "@" is used as separator because it cannot appear in a valid appID
	// ([a-zA-Z0-9._-]) or serviceName ([a-z][a-z0-9-]*), eliminating the
	// collision risk that a hyphen separator would have introduced.
	//   - Single-container: "system.slice:{systemdSvc}:{appID}"
	//   - Multi-service:    "system.slice:{systemdSvc}:{appID}@{serviceName}"
	//
	// INVARIANT: ApplyEntitlements and CDI helpers must not set CgroupsPath.
	// The assertion below detects any future violation at runtime (SOC2-CC6).
	if spec.Linux.CgroupsPath != "" {
		return fmt.Errorf("security: CgroupsPath was unexpectedly set before assignment (%q); ApplyEntitlements or CDI must not set it", spec.Linux.CgroupsPath)
	}
	cgroupSuffix := appID
	if serviceName != "" {
		cgroupSuffix = appID + "@" + serviceName
	}
	spec.Linux.CgroupsPath = fmt.Sprintf("system.slice:%s:%s", sharedenv.SystemdServiceName(), cgroupSuffix)

	report(&agentpb.CreateContainerProgress{Phase: agentpb.CreateContainerProgress_CREATING_CONTAINER})

	labels := wendyLabels(appID, serviceName, version, req.GetRestartPolicy(), appCfg.Entitlements)

	// Inject /etc/hosts bind-mount for isolated multi-service apps so service
	// names resolve via CNI-assigned IPs.
	if appCfg.Isolation == "isolated" && len(appCfg.Services) > 1 {
		// safeJoin rejects separators and dot-only segments, then verifies the
		// result is directly under the base dir (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
		hostsPath, err := safeJoin("/run/wendy/hosts", appID)
		if err != nil {
			return fmt.Errorf("security: appID %q produces unsafe hosts path: %w", appID, err)
		}
		// Always create the directory (os.MkdirAll is idempotent) and seed the
		// hosts file with IPs already known from previously-started sibling services.
		// c.mu is held here (defer Unlock above), so reading c.serviceIPs is safe.
		// Seeding with existing IPs means containers that start late see a useful
		// /etc/hosts from the first moment rather than an empty file (SOC2-CC6).
		// The atomic rename in writeHostsFile prevents truncated reads (NIST-SI-10).
		if err := os.MkdirAll("/run/wendy/hosts", 0o700); err != nil {
			return fmt.Errorf("creating hosts dir: %w", err)
		}
		if err := writeHostsFile(hostsPath, c.serviceIPs[appID]); err != nil {
			return fmt.Errorf("initialising hosts file for %s: %w", appID, err)
		}
		localoci.InjectHostsMount(spec, hostsPath)
	}

	// Apply isolation-specific namespace and shm settings for shared-namespace groups.
	if appconfig.IsSharedNamespaceIsolation(appCfg.Isolation) {
		primaryPID, hasPrimary := c.getPrimaryPID(appID)
		if hasPrimary {
			// Secondary service: join the primary's namespaces.
			// nsAnchors holds open fds for each namespace so the paths embedded
			// in the spec (/proc/self/fd/{n}) remain valid until runc opens them.
			nsAnchors, err := localoci.JoinGroupNamespaces(spec, primaryPID, appCfg.Isolation)
			if err != nil {
				return fmt.Errorf("joining group namespaces: %w", err)
			}
			defer func() {
				for _, f := range nsAnchors {
					f.Close()
				}
			}()
			if appCfg.Isolation == "shared-ipc" {
				shmPath, shmErr := ensureSharedSHM(appID)
				if shmErr != nil {
					return shmErr
				}
				localoci.RemoveDefaultSHM(spec)
				spec.Mounts = append(spec.Mounts, localoci.SharedSHMMount(shmPath))
			}
		} else {
			// Primary service: create shared shm dir so secondaries can find it.
			if appCfg.Isolation == "shared-ipc" {
				if _, shmErr := ensureSharedSHM(appID); shmErr != nil {
					return shmErr
				}
			}
		}
	}

	// Serialize our custom OCI spec to JSON for WithSpecFromBytes.
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshaling OCI spec: %w", err)
	}

	// Create the container with a new snapshot from the image.
	snapshotKey := SnapshotKey(appID, serviceName)
	_, err = c.client.NewContainer(ctx, containerName,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithContainerLabels(labels),
		containerd.WithNewSpec(
			oci.WithSpecFromBytes(specJSON),
		),
	)
	if err != nil {
		return fmt.Errorf("creating container %q: %w", containerName, err)
	}

	// Container created successfully; keep the D-Bus proxy running.
	dbusProxyStarted = false

	report(&agentpb.CreateContainerProgress{Phase: agentpb.CreateContainerProgress_COMPLETE})

	createdFields := []zap.Field{
		zap.String("container_name", containerName),
		zap.String("app_id", appID),
		zap.String("image", imageName),
		zap.String("version", version),
	}
	if serviceName != "" {
		createdFields = append(createdFields, zap.String("service_name", serviceName))
	}
	c.logger.Info("Container created", createdFields...)

	// Cache services map for stop-order resolution and isolation mode for
	// StartContainer PID tracking. c.mu is already held for the full function
	// via defer c.mu.Unlock() above — no inner lock needed.
	if len(appCfg.Services) > 0 {
		if c.appServices == nil {
			c.appServices = make(map[string]map[string]*appconfig.ServiceConfig)
		}
		c.appServices[appID] = appCfg.Services
	}
	if appCfg.Isolation != "" {
		if c.appIsolation == nil {
			c.appIsolation = make(map[string]string)
		}
		c.appIsolation[appID] = appCfg.Isolation
	}

	return nil
}

// applyCDIGPU loads the NVIDIA CDI spec (generated by nvidia-ctk at boot)
// and applies GPU devices, library mounts, and environment variables to the
// OCI spec. This handles platform-specific paths (Orin Nano vs Thor, etc.).
func (c *Client) applyCDIGPU(spec *localoci.Spec) {
	mgr := cdi.NewManager()
	cdiSpec, err := mgr.LoadNVIDIACDISpec()
	if err != nil {
		c.logger.Warn("No NVIDIA CDI spec found, GPU library mounts may be incomplete", zap.Error(err))
		return
	}

	// nvidia-ctk in CSV mode generates a device named "all".
	// Try that first, then fall back to the first device in the spec.
	if err := cdi.ApplyCDIDevice(spec, cdiSpec, "all"); err == nil {
		c.logger.Info("Applied NVIDIA CDI spec for GPU access")
		return
	}
	if len(cdiSpec.Devices) > 0 {
		if err := cdi.ApplyCDIDevice(spec, cdiSpec, cdiSpec.Devices[0].Name); err == nil {
			c.logger.Info("Applied NVIDIA CDI device", zap.String("device", cdiSpec.Devices[0].Name))
			return
		}
	}
	c.logger.Warn("CDI spec found but no devices could be applied")
}

func (c *Client) StartContainer(ctx context.Context, appName, postStartAgentCommand string, restartPolicy *agentpb.RestartPolicy) (<-chan services.ContainerOutput, error) {
	// Accept both "appID" and "appID_serviceName" forms. ParseContainerName
	// validates both components so a crafted value cannot reach the label filter
	// in the containersForApp fallback path (SOC2-CC6, ISO27001-A.8).
	appID, serviceName, err := ParseContainerName(appName)
	if err != nil {
		return nil, fmt.Errorf("StartContainer: invalid app name: %w", err)
	}
	// Hold c.mu for container lookup and task creation to prevent a concurrent
	// DeleteContainer from removing the container between the label-based lookup
	// and NewTask (TOCTOU, SOC2-CC6). Released before the streaming goroutine
	// launch via the muHeld flag pattern.
	c.mu.Lock()
	muHeld := true
	defer func() {
		if muHeld {
			c.mu.Unlock()
		}
	}()
	ctx = c.withNamespace(ctx)

	container, err := c.client.LoadContainer(ctx, appName)
	if err != nil {
		// Fall back to a label-based lookup so that callers can pass the bare
		// appID (e.g. "myapp") even when the container was created under a
		// multi-service name (e.g. "myapp/api" for serviceName="api").
		// If the label query returns exactly one container we use it; if it
		// returns multiple the caller must be more specific.
		ctrs, labelErr := c.containersForApp(ctx, appName)
		if labelErr != nil || len(ctrs) == 0 {
			return nil, fmt.Errorf("loading container %q: %w", appName, err)
		}
		if len(ctrs) > 1 {
			return nil, fmt.Errorf("app %q has multiple service containers; use the full container name (appID_serviceName) to start a specific service", appName)
		}
		container = ctrs[0]
	}

	if restartPolicy != nil {
		if err := c.applyRestartPolicyLabel(ctx, container, restartPolicy); err != nil {
			return nil, fmt.Errorf("updating restart policy for %q: %w", appName, err)
		}
	}

	// Clean up any stale task from a previous run.
	c.deleteStaleTask(ctx, container, appName)

	// Create pipes for stdout/stderr capture.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Create a new task with pipe-based stdio for programmatic capture.
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdoutW, stderrW)))
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			// Orphaned task: exists in the containerd runtime but container.Task()
			// can't load it. Force-delete via the task service, then recreate the
			// container and retry.
			c.logger.Warn("Orphaned task detected, force-deleting and recreating container", zap.String("app_name", appName))
			c.forceDeleteTask(ctx, appName)
			if rerr := c.recreateContainer(ctx, container, appName); rerr != nil {
				c.logger.Error("Failed to recreate container", zap.Error(rerr))
			} else {
				container, err = c.client.LoadContainer(ctx, appName)
				if err == nil {
					task, err = container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdoutW, stderrW)))
				}
			}
		}
		if err != nil {
			stdoutR.Close()
			stdoutW.Close()
			stderrR.Close()
			stderrW.Close()
			return nil, fmt.Errorf("creating task for %q: %w", appName, err)
		}
	}

	// Set up the wait channel before starting.
	exitStatusCh, err := task.Wait(ctx)
	if err != nil {
		_, _ = task.Delete(ctx)
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("waiting on task for %q: %w", appName, err)
	}

	// Start the task.
	if err := task.Start(ctx); err != nil {
		_, _ = task.Delete(ctx)
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("starting task for %q: %w", appName, err)
	}

	c.logger.Info("Container started", zap.String("app_name", appName))
	c.startPostStartAgentHook(postStartAgentCommand, appName)

	// Track the primary PID for shared-namespace app groups.
	// getIsolation requires c.mu (held here via muHeld).
	isolation := c.getIsolation(appID)
	if appconfig.IsSharedNamespaceIsolation(isolation) {
		if _, alreadyHasPrimary := c.getPrimaryPID(appID); !alreadyHasPrimary {
			c.setPrimaryPID(appID, task.Pid())
		}
	}

	// Anchor the network namespace with an open fd BEFORE releasing the mutex.
	// This eliminates the TOCTOU race where a concurrent StopContainer could
	// recycle the PID between mutex release and CNI ADD, causing the plugin to
	// operate on the wrong process's netns (SOC2-CC6, NIST-SC-7, ISO27001-A.8).
	var netnsRef *os.File
	if isolation == "isolated" && serviceName != "" {
		nsPath := fmt.Sprintf("/proc/%d/ns/net", task.Pid())
		var nsErr error
		netnsRef, nsErr = os.Open(nsPath)
		if nsErr != nil {
			c.logger.Warn("could not anchor netns fd before mutex release; CNI ADD skipped",
				zap.String("app_id", appID), zap.Error(nsErr))
		}
	}

	// Release the mutex before launching the streaming goroutine, which does
	// not need it (it only reads from pipes).
	muHeld = false
	c.mu.Unlock()

	// CNI ADD for isolated multi-service apps: assign IP and update /etc/hosts.
	// bindNetnsForCNI creates a stable bind-mount under /run/wendy/netns/ so
	// CNI_NETNS is a real filesystem path as required by the CNI spec — not a
	// /proc/self/fd/<n> reference that third-party CNI plugins may not honour.
	// It also closes the fd (the bind-mount anchors the namespace independently).
	// On Linux the bind-mount is used; on other platforms the fd path is the fallback.
	if isolation == "isolated" && serviceName != "" && netnsRef != nil {
		netnsPath, cleanupNetns := bindNetnsForCNI(appName, netnsRef)
		ip, cniErr := c.CNIAdd(ctx, appID, appName, netnsPath)
		cleanupNetns()
		if cniErr != nil {
			c.logger.Error("CNI ADD failed", zap.String("app_id", appID), zap.Error(cniErr))
		} else {
			c.mu.Lock()
			// Guard against a concurrent StopContainer that may have deleted
			// c.appIsolation[appID] during the window between CNI ADD and this
			// re-lock. If the app is already gone, discard the IP silently rather
			// than writing stale state (SOC2-CC6, NIST-SI-16, ISO27001-A.8).
			if c.appIsolation[appID] == "" {
				c.mu.Unlock()
				c.logger.Warn("CNI ADD: app already stopped before IP could be recorded, discarding IP",
					zap.String("app_id", appID), zap.String("ip", ip))
				_, _ = task.Delete(ctx, containerd.WithProcessKill)
				return nil, fmt.Errorf("app %q stopped during CNI ADD; container not started", appID)
			}
			c.recordServiceIP(appID, serviceName, ip)
			hostsPath, pathErr := safeJoin("/run/wendy/hosts", appID)
			if pathErr != nil {
				// Hard error: a validated appID must never produce an unsafe path.
				// Remove the just-recorded IP so it cannot pollute future writeHostsFile
				// calls for the same appID (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
				if c.serviceIPs != nil {
					delete(c.serviceIPs[appID], serviceName)
				}
				c.logger.Error("security: appID produces unsafe hosts path",
					zap.String("app_id", appID), zap.Error(pathErr))
				c.mu.Unlock()
				_, _ = task.Delete(ctx, containerd.WithProcessKill)
				return nil, fmt.Errorf("security: appID %q produces unsafe hosts path: %w", appID, pathErr)
			}
			_ = writeHostsFile(hostsPath, c.serviceIPs[appID])
			c.mu.Unlock()
		}
	}

	// Stream output from the pipes.
	outputCh := make(chan services.ContainerOutput, 64)
	go c.streamOutput(ctx, task, exitStatusCh, outputCh, appName, stdoutR, stderrR, stdoutW, stderrW)

	return outputCh, nil
}

// StartContainerWithStdin is like StartContainer but attaches the provided
// stdin reader to the container's standard input.
func (c *Client) StartContainerWithStdin(ctx context.Context, appName string, stdin io.Reader, postStartAgentCommand string, restartPolicy *agentpb.RestartPolicy) (<-chan services.ContainerOutput, error) {
	if _, _, err := ParseContainerName(appName); err != nil {
		return nil, fmt.Errorf("StartContainerWithStdin: invalid app name: %w", err)
	}
	c.mu.Lock()
	muHeld := true
	defer func() {
		if muHeld {
			c.mu.Unlock()
		}
	}()
	ctx = c.withNamespace(ctx)

	container, err := c.client.LoadContainer(ctx, appName)
	if err != nil {
		ctrs, labelErr := c.containersForApp(ctx, appName)
		if labelErr != nil || len(ctrs) == 0 {
			return nil, fmt.Errorf("loading container %q: %w", appName, err)
		}
		if len(ctrs) > 1 {
			return nil, fmt.Errorf("app %q has multiple service containers; use the full container name (appID_serviceName) to start a specific service", appName)
		}
		container = ctrs[0]
	}

	if restartPolicy != nil {
		if err := c.applyRestartPolicyLabel(ctx, container, restartPolicy); err != nil {
			return nil, fmt.Errorf("updating restart policy for %q: %w", appName, err)
		}
	}

	c.deleteStaleTask(ctx, container, appName)

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(stdin, stdoutW, stderrW)))
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			c.logger.Warn("Orphaned task detected, force-deleting and recreating container", zap.String("app_name", appName))
			c.forceDeleteTask(ctx, appName)
			if rerr := c.recreateContainer(ctx, container, appName); rerr != nil {
				c.logger.Error("Failed to recreate container", zap.Error(rerr))
			} else {
				container, err = c.client.LoadContainer(ctx, appName)
				if err == nil {
					task, err = container.NewTask(ctx, cio.NewCreator(cio.WithStreams(stdin, stdoutW, stderrW)))
				}
			}
		}
		if err != nil {
			stdoutR.Close()
			stdoutW.Close()
			stderrR.Close()
			stderrW.Close()
			return nil, fmt.Errorf("creating task for %q: %w", appName, err)
		}
	}

	exitStatusCh, err := task.Wait(ctx)
	if err != nil {
		_, _ = task.Delete(ctx)
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("waiting on task for %q: %w", appName, err)
	}

	if err := task.Start(ctx); err != nil {
		_, _ = task.Delete(ctx)
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("starting task for %q: %w", appName, err)
	}

	c.logger.Info("Container started with stdin", zap.String("app_name", appName))
	c.startPostStartAgentHook(postStartAgentCommand, appName)

	muHeld = false
	c.mu.Unlock()

	outputCh := make(chan services.ContainerOutput, 64)
	go c.streamOutput(ctx, task, exitStatusCh, outputCh, appName, stdoutR, stderrR, stdoutW, stderrW)

	return outputCh, nil
}

func shellCommand() (string, string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", "/C"
	}
	return "sh", "-c"
}

var deviceHostnameWithSuffix = func() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return ""
	}
	return h + ".local"
}

// buildContainerBaseEnv builds the base environment variables for a container.
//
// Precondition: appID must pass ValidateAppID and serviceName (when non-empty)
// must pass ValidateServiceName. CreateContainerWithProgress enforces this at
// its entry point; callers that bypass it are responsible for their own check.
//
// For single-container apps (serviceName == ""):
//   - WENDY_HOSTNAME is set to the device hostname (e.g. "device.local").
//
// For multi-service apps (serviceName != ""):
//   - WENDY_HOSTNAME is set to "{serviceName}.local" so each service has a
//     distinct hostname identity.
//   - WENDY_APP_GROUP is set to appID so the service can discover its siblings.
func buildContainerBaseEnv(appID, serviceName string) ([]string, error) {
	// Defence-in-depth: reject non-empty inputs that fail validation at the
	// injection site so callers can't accidentally inject control characters
	// into OCI env vars (SOC2-CC6, ISO27001-A.8, NIST-SI-10). Empty values are
	// allowed; they simply skip the corresponding env var (see guards below).
	if appID != "" {
		if err := appconfig.ValidateAppID(appID); err != nil {
			return nil, fmt.Errorf("buildContainerBaseEnv: invalid appID: %w", err)
		}
		// Explicit fast-fail: ValidateAppID's regex rejects these, but guard
		// explicitly at the concatenation site as well.
		if strings.ContainsAny(appID, "\x00\n\r=\t") {
			return nil, fmt.Errorf("buildContainerBaseEnv: appID contains forbidden characters")
		}
	}
	if serviceName != "" {
		if err := appconfig.ValidateServiceName(serviceName); err != nil {
			return nil, fmt.Errorf("buildContainerBaseEnv: invalid serviceName: %w", err)
		}
		if strings.ContainsAny(serviceName, "\x00\n\r=\t") {
			return nil, fmt.Errorf("buildContainerBaseEnv: serviceName contains forbidden characters")
		}
	}

	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm",
	}
	deviceHost := deviceHostnameWithSuffix()
	if serviceName != "" {
		// Multi-service: hostname is the service name, not the device hostname.
		env = append(env, "WENDY_HOSTNAME="+serviceName+".local")
		env = append(env, "WENDY_APP_GROUP="+appID)
	} else {
		if deviceHost != "" {
			env = append(env, "WENDY_HOSTNAME="+deviceHost)
		}
	}
	// WENDY_DEVICE_HOSTNAME is the mDNS hostname of the host device, available
	// in both single- and multi-service containers so workloads can always reach
	// the device regardless of what WENDY_HOSTNAME is set to.
	if deviceHost != "" {
		env = append(env, "WENDY_DEVICE_HOSTNAME="+deviceHost)
	}
	// WENDY_APP_ID is injected unconditionally (all network modes) so app code
	// can always read its own identity. The OTel identity vars are injected only
	// under host networking (in injectOTELEnvIfNeeded) because the OTLP receiver
	// is only reachable in that mode.
	if appID != "" {
		env = append(env, "WENDY_APP_ID="+appID)
	}
	return env, nil
}

// validateUserEnv rejects caller-supplied env entries that contain characters
// which could break the OCI env format or enable injection attacks.
// Mirrors the defence-in-depth checks in buildContainerBaseEnv (SOC2-CC6, NIST-SI-10).

// posixEnvKeyPattern is an allowlist for POSIX-compliant environment variable
// names. It accepts only ASCII letters, digits, and underscores, with an
// underscore or letter as the first character. This allowlist prevents leading-
// whitespace bypass (e.g. " LD_PRELOAD") and eliminates Unicode case-folding
// ambiguity before the denylist check below (SOC2-CC6, NIST-SI-10).
var posixEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// blockedEnvPrefixes is the set of key prefixes that user-supplied env vars
// must not use. These keys affect dynamic linker behavior (LD_*) or are
// reserved by Wendy (WENDY_*); a compromised or malicious caller could use
// them to preload arbitrary code or override Wendy internals
// (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
var blockedEnvPrefixes = []string{
	"LD_",    // LD_PRELOAD, LD_LIBRARY_PATH, LD_AUDIT, LD_DEBUG, etc.
	"DYLD_",  // macOS dynamic linker (defense-in-depth for cross-platform images)
	"WENDY_", // Wendy-internal variables must not be overrideable by callers
}

// maxUserEnvEntries is the maximum number of caller-supplied env entries accepted.
// Prevents OCI spec bloat / DoS via unbounded env injection (SOC2-CC6, NIST-SI-10).
const maxUserEnvEntries = 512

// maxUserEnvEntryLen is the maximum byte length of a single KEY=VALUE entry.
// 32 KB covers all practical use cases while bounding spec-JSON size (SOC2-CC6, NIST-SI-10).
const maxUserEnvEntryLen = 32 * 1024

func validateUserEnv(entries []string) error {
	if len(entries) > maxUserEnvEntries {
		return fmt.Errorf("too many env entries: %d exceeds limit of %d (SOC2-CC6, NIST-SI-10)", len(entries), maxUserEnvEntries)
	}
	for _, kv := range entries {
		if len(kv) > maxUserEnvEntryLen {
			return fmt.Errorf("env entry exceeds maximum length of %d bytes (SOC2-CC6, NIST-SI-10)", maxUserEnvEntryLen)
		}
		if strings.ContainsAny(kv, "\x00\n\r") {
			return fmt.Errorf("env entry contains forbidden control character: %q", sanitizeForLog(kv, 80))
		}
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("env entry missing '=' separator: %q", sanitizeForLog(kv, 80))
		}
		// Reject keys that do not conform to the POSIX env key format. This also
		// closes the leading-whitespace bypass (" LD_PRELOAD") and the Unicode
		// case-folding bypass that strings.ToUpper alone cannot prevent.
		if !posixEnvKeyPattern.MatchString(key) {
			return fmt.Errorf("env key %q is not a valid POSIX environment variable name (SOC2-CC6, NIST-SI-10)", sanitizeForLog(key, 80))
		}
		upper := strings.ToUpper(key) // safe: key is ASCII-only after pattern check
		for _, prefix := range blockedEnvPrefixes {
			if strings.HasPrefix(upper, prefix) {
				return fmt.Errorf("env key %q is reserved and cannot be set by callers (SOC2-CC6, NIST-SI-10)", key)
			}
		}
	}
	return nil
}

// ros2DomainIDMax is the conservative upper bound for ROS 2 domain IDs.
// The ROS 2 spec defines valid IDs as 0–101 (conservative); some platforms
// allow up to 232 but 101 covers all standard deployments (SOC2-CC6, NIST-SI-10).
const (
	ros2DomainIDMin = 0
	ros2DomainIDMax = 101
)

// validRMWImplementations is the allowlist of known RMW (ROS Middleware)
// implementation identifiers. Validating against a fixed set prevents
// injection of arbitrary strings into the container environment via the
// RMW field in wendy.json (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
var validRMWImplementations = map[string]bool{
	"rmw_cyclonedds_cpp": true,
	"rmw_fastrtps_cpp":   true,
	"rmw_connextdds":     true,
	"rmw_gurumdds_cpp":   true,
}

// buildROS2Env returns ROS2 environment variables from the app's frameworks.ros2 config.
func buildROS2Env(appCfg *appconfig.AppConfig) []string {
	ros2 := appCfg.GetROS2Config()
	if ros2 == nil {
		return nil
	}
	if ros2.DomainID < ros2DomainIDMin || ros2.DomainID > ros2DomainIDMax {
		return nil // invalid domain ID; caller should have validated at config parse time
	}
	env := []string{fmt.Sprintf("ROS_DOMAIN_ID=%d", ros2.DomainID)}
	if ros2.RMW != "" {
		// Allowlist validation: only known RMW identifiers are injected.
		// A control-char check alone is insufficient — an unknown value with
		// only printable chars could still corrupt the env or trigger
		// plugin-specific parsing bugs (SOC2-CC6, NIST-SI-10).
		if !validRMWImplementations[ros2.RMW] {
			return env // unknown RMW: drop rather than inject
		}
		env = append(env, "RMW_IMPLEMENTATION="+ros2.RMW)
	}
	return env
}

// injectOTELEnvIfNeeded appends OTEL exporter env vars to env when host
// networking is in effect and the endpoint is not already configured. Besides
// the endpoint and protocol, it sets OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES (wendy.app.name) to the appId so that telemetry
// exported by the app matches `wendy device logs --app <id>`, which filters on
// those resource attributes. It must be called after the image env has been
// merged so that image-set values take precedence.
//
// appID is passed explicitly (rather than read from appCfg.AppID) so the
// caller's AppConfig struct is never mutated, which would affect concurrent or
// retry uses of the same pointer.
func injectOTELEnvIfNeeded(env []string, appCfg *appconfig.AppConfig, appID string) []string {
	if !hasHostNetworkEntitlement(appCfg) {
		return env
	}
	hasEndpoint, hasProtocol := false, false
	hasServiceName, hasResourceAttrs := false, false
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "OTEL_EXPORTER_OTLP_ENDPOINT="):
			hasEndpoint = true
		case strings.HasPrefix(e, "OTEL_EXPORTER_OTLP_PROTOCOL="):
			hasProtocol = true
		case strings.HasPrefix(e, "OTEL_SERVICE_NAME="):
			hasServiceName = true
		case strings.HasPrefix(e, "OTEL_RESOURCE_ATTRIBUTES="):
			hasResourceAttrs = true
		}
	}
	// Endpoint/protocol: only point the exporter at our receiver when the image
	// hasn't already configured one.
	if !hasEndpoint {
		otelPort := os.Getenv("WENDY_OTEL_PORT")
		if otelPort == "" {
			otelPort = "4317"
		}
		if p, err := strconv.Atoi(otelPort); err != nil || p < 1 || p > 65535 {
			otelPort = "4317"
		}
		env = append(env, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:"+otelPort)
		if !hasProtocol {
			env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL=grpc")
		}
	}
	// Identity: set regardless of where the exporter points, so `wendy device
	// logs --app <id>` can match even when the image preset its own endpoint.
	// Image-set values still take precedence.
	if appID != "" {
		if !hasServiceName {
			env = append(env, "OTEL_SERVICE_NAME="+appID)
		}
		if !hasResourceAttrs {
			env = append(env, "OTEL_RESOURCE_ATTRIBUTES=wendy.app.name="+appID)
		}
	}
	return env
}

func hasHostNetworkEntitlement(appCfg *appconfig.AppConfig) bool {
	for _, e := range appCfg.Entitlements {
		if e.Type == appconfig.EntitlementNetwork && (e.Mode == "host" || e.Mode == "") {
			return true
		}
	}
	return false
}

func expandAgentHook(command, appName string) string {
	return os.Expand(command, func(key string) string {
		switch key {
		case "WENDY_HOSTNAME":
			return "localhost"
		case "WENDY_APP_ID":
			return appName
		default:
			return os.Getenv(key)
		}
	})
}

var startPostStartHookCommand = func(shell, flag, command string) (func() error, error) {
	cmd := exec.Command(shell, flag, command)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}

func (c *Client) startPostStartAgentHook(command, appName string) bool {
	if command == "" {
		return false
	}

	expanded := expandAgentHook(command, appName)
	shell, flag := shellCommand()
	wait, err := startPostStartHookCommand(shell, flag, expanded)
	if err != nil {
		c.logger.Warn("Failed to start postStart agent hook",
			zap.String("app_name", appName),
			zap.Error(err),
		)
		return false
	}
	go func() {
		if err := wait(); err != nil {
			c.logger.Warn("postStart agent hook exited with error",
				zap.String("app_name", appName),
				zap.Error(err),
			)
		}
	}()
	c.logger.Info("Started postStart agent hook",
		zap.String("app_name", appName),
	)
	return true
}

// deleteStaleTask attempts to load and force-delete any existing task for the
// container. It handles both the normal case (task loadable) and the edge case
// where the task exists in containerd but container.Task() can't load it.
func (c *Client) deleteStaleTask(ctx context.Context, container containerd.Container, appName string) {
	existingTask, taskErr := container.Task(ctx, nil)
	if taskErr != nil {
		return // No task to clean up.
	}
	_ = existingTask.Kill(ctx, syscall.SIGKILL)
	if waitCh, waitErr := existingTask.Wait(ctx); waitErr == nil {
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
			c.logger.Warn("Timed out waiting for stale task to exit", zap.String("app_name", appName))
		}
	}
	_, _ = existingTask.Delete(ctx, containerd.WithProcessKill)
}

// forceDeleteTask uses the low-level containerd task service to delete a task
// by container ID. This handles orphaned tasks where container.Task() fails
// because the shim process is gone but task metadata remains in the runtime.
func (c *Client) forceDeleteTask(ctx context.Context, containerID string) {
	_, err := c.client.TaskService().Delete(ctx, &tasks.DeleteTaskRequest{
		ContainerID: containerID,
	})
	if err != nil {
		c.logger.Debug("Force task delete attempt",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
	} else {
		c.logger.Info("Force-deleted orphaned task",
			zap.String("container_id", containerID),
		)
	}
}

// recreateContainer deletes a container (which cascades to any orphaned task)
// and recreates it with the same image, spec, and labels. This clears orphaned
// task metadata that blocks NewTask.
func (c *Client) recreateContainer(ctx context.Context, ctr containerd.Container, appName string) error {
	info, err := ctr.Info(ctx)
	if err != nil {
		return fmt.Errorf("getting container info: %w", err)
	}

	image, err := ctr.Image(ctx)
	if err != nil {
		return fmt.Errorf("getting container image: %w", err)
	}

	spec, err := ctr.Spec(ctx)
	if err != nil {
		return fmt.Errorf("getting container spec: %w", err)
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}

	// Derive appID and serviceName from labels — they are the authoritative
	// source (set at creation time by wendyLabels). Parsing the container name
	// is intentionally avoided: the name format is an encoded composite of
	// appID+serviceName and labels are unambiguous (SOC2-CC8).
	labelAppID := info.Labels[labelKeyAppID]
	labelSvcName := info.Labels[labelKeyServiceName]
	if labelAppID == "" {
		// Fallback for containers created before label-based identity was
		// introduced; parse the name as a best-effort recovery.
		var parseErr error
		labelAppID, labelSvcName, parseErr = ParseContainerName(appName)
		if parseErr != nil {
			return fmt.Errorf("refusing to recreate container with malformed name: %w", parseErr)
		}
	}
	if err := appconfig.ValidateAppID(labelAppID); err != nil {
		return fmt.Errorf("refusing to recreate container with invalid appID in labels: %w", err)
	}
	if labelSvcName != "" {
		if err := appconfig.ValidateServiceName(labelSvcName); err != nil {
			return fmt.Errorf("refusing to recreate container with invalid serviceName in labels: %w", err)
		}
	}

	// Delete the container (cascades to orphaned task).
	if err := ctr.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("deleting container: %w", err)
	}
	snapshotKey := SnapshotKey(labelAppID, labelSvcName)
	_, err = c.client.NewContainer(ctx, ContainerName(labelAppID, labelSvcName),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithContainerLabels(info.Labels),
		containerd.WithNewSpec(
			oci.WithSpecFromBytes(specJSON),
		),
	)
	if err != nil {
		return fmt.Errorf("recreating container: %w", err)
	}

	c.logger.Info("Recreated container to clear orphaned task", zap.String("app_name", appName))
	return nil
}

// applyRestartPolicyLabel updates the restart policy label on an existing container.
func (c *Client) applyRestartPolicyLabel(ctx context.Context, container containerd.Container, restartPolicy *agentpb.RestartPolicy) error {
	return container.Update(ctx, func(ctx context.Context, client *containerd.Client, ctr *containers.Container) error {
		if ctr.Labels == nil {
			ctr.Labels = make(map[string]string)
		}
		policyStr := restartPolicyToLabel(restartPolicy)
		if policyStr != "" {
			ctr.Labels[labelKeyRestartPolicy] = policyStr
		} else {
			delete(ctr.Labels, labelKeyRestartPolicy)
		}
		return nil
	})
}

// streamOutput reads stdout/stderr from pipes and sends it to the output
// channel. It closes the channel when the task exits.
func (c *Client) streamOutput(
	ctx context.Context,
	task containerd.Task,
	exitStatusCh <-chan containerd.ExitStatus,
	outputCh chan<- services.ContainerOutput,
	appName string,
	stdoutR, stderrR *io.PipeReader,
	stdoutW, stderrW *io.PipeWriter,
) {
	defer close(outputCh)

	// Read stdout and stderr concurrently.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		streamReader(stdoutR, outputCh, func(data []byte) services.ContainerOutput {
			return services.ContainerOutput{Stdout: data}
		})
	}()

	go func() {
		defer wg.Done()
		streamReader(stderrR, outputCh, func(data []byte) services.ContainerOutput {
			return services.ContainerOutput{Stderr: data}
		})
	}()

	// Wait for the task to exit.
	exitStatus := <-exitStatusCh
	code, _, err := exitStatus.Result()
	if err != nil {
		c.logger.Error("Task exited with error",
			zap.String("app_name", appName),
			zap.Error(err),
		)
	} else {
		c.logger.Info("Task exited",
			zap.String("app_name", appName),
			zap.Uint32("exit_code", code),
		)
	}

	// Close the write ends to unblock readers.
	stdoutW.Close()
	stderrW.Close()

	// Wait for readers to finish.
	wg.Wait()

	outputCh <- services.ContainerOutput{Done: true}
}

// containersForApp returns all Wendy-managed containers whose labelKeyAppID
// label equals appID. Both single-container apps (one container) and
// multi-service apps (one container per service) are found this way, with no
// dependency on container-name conventions.
// ctx must already have the containerd namespace set.
func (c *Client) containersForApp(ctx context.Context, appID string) ([]containerd.Container, error) {
	// Defence-in-depth: re-validate appID at the injection site so that a future
	// caller that bypasses the RPC entry-point validation cannot inject into the
	// containerd filter expression (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
	// ValidateAppID allows only [a-zA-Z0-9._-], none of which are special in
	// the containerd filter grammar, so %q quoting is safe for this character set.
	if err := appconfig.ValidateAppID(appID); err != nil {
		return nil, fmt.Errorf("containersForApp: invalid appID: %w", err)
	}
	all, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q==%q", labelKeyAppID, appID))
	if err != nil {
		return nil, fmt.Errorf("listing containers for app %q: %w", appID, err)
	}
	// Post-filter in Go to confirm the label value matches exactly, providing
	// defence-in-depth against any future filter grammar edge case.
	// Use a fresh slice — reusing all[:0] would alias the backing array and
	// risk reading overwritten elements during the range loop (SOC2-CC6).
	var ctrs []containerd.Container
	for _, ctr := range all {
		labels, lerr := ctr.Labels(ctx)
		if lerr != nil || labels[labelKeyAppID] != appID {
			continue
		}
		ctrs = append(ctrs, ctr)
	}
	return ctrs, nil
}

// ContainerIDsForApp returns the containerd container IDs for all services
// belonging to appID. Single-container apps return one ID; multi-service apps
// return one ID per service. The service layer uses this to mark each
// container in the monitor before issuing a stop or delete.
func (c *Client) ContainerIDsForApp(ctx context.Context, appID string) ([]string, error) {
	ctx = c.withNamespace(ctx)
	ctrs, err := c.containersForApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(ctrs))
	for i, ctr := range ctrs {
		ids[i] = ctr.ID()
	}
	return ids, nil
}

// stopOne stops the task for a single container.
// ctx must already have the containerd namespace set.
func (c *Client) stopOne(ctx context.Context, containerID string) error {
	container, err := c.client.LoadContainer(ctx, containerID)
	if err != nil {
		return fmt.Errorf("loading container %q: %w", containerID, err)
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil // No task running.
		}
		return fmt.Errorf("getting task for %q: %w", containerID, err)
	}

	// For isolated multi-service containers, call CNI DEL while the task's
	// network namespace still exists (the PID is live, so /proc/PID/ns/net is
	// valid). After SIGTERM/SIGKILL the netns reference disappears. CNI DEL is
	// best-effort — failure is logged but does not block the stop path.
	if appID, svcName, parseErr := ParseContainerName(containerID); parseErr == nil && svcName != "" {
		netnsPath := fmt.Sprintf("/proc/%d/ns/net", task.Pid())
		if cniErr := c.CNIDel(ctx, appID, containerID, netnsPath); cniErr != nil {
			c.logger.Warn("CNI DEL failed during stop (non-fatal)",
				zap.String("container_id", containerID), zap.Error(cniErr))
		}
	}

	// Send SIGTERM first for graceful shutdown.
	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		if !errdefs.IsNotFound(err) {
			c.logger.Warn("Failed to send SIGTERM",
				zap.String("container_id", containerID),
				zap.Error(err),
			)
		}
	}

	// Wait up to 10 seconds for graceful exit.
	waitCh, err := task.Wait(ctx)
	if err != nil {
		c.logger.Warn("Failed to wait on task, sending SIGKILL",
			zap.String("container_id", containerID),
			zap.Error(err),
		)
	} else {
		select {
		case <-waitCh:
			c.logger.Info("Container stopped gracefully", zap.String("container_id", containerID))
		case <-time.After(10 * time.Second):
			c.logger.Warn("Container did not stop within 10s, sending SIGKILL",
				zap.String("container_id", containerID),
			)
			if err := task.Kill(ctx, syscall.SIGKILL); err != nil && !errdefs.IsNotFound(err) {
				c.logger.Error("Failed to send SIGKILL",
					zap.String("container_id", containerID),
					zap.Error(err),
				)
			}
			<-waitCh
		}
	}

	// Delete the task.
	_, err = task.Delete(ctx, containerd.WithProcessKill)
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("deleting task for %q: %w", containerID, err)
	}

	if c.proxyManager != nil {
		_ = c.proxyManager.Stop(containerID)
	}

	c.logger.Info("Container stopped", zap.String("container_id", containerID))
	return nil
}

// StopContainer stops all containers belonging to appID. For single-container
// apps this is one container; for multi-service apps it stops every service.
// c.mu is held for the full duration to prevent a concurrent
// CreateContainerWithProgress from inserting a new service container between
// the list query and the stop loop (TOCTOU, SOC2-CC6, NIST-AC-4).
func (c *Client) StopContainer(ctx context.Context, appID string) error {
	ctx = c.withNamespace(ctx)

	// Hold mutex only long enough to enumerate containers and resolve stop order.
	// Releasing before stopOne prevents holding c.mu across potentially long
	// blocking I/O (SIGTERM wait, 10 s timeout), which would starve concurrent
	// StartContainer / CreateContainerWithProgress calls (SOC2-CC6, NIST-AC-3).
	c.mu.Lock()
	ctrs, err := c.containersForApp(ctx, appID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if len(ctrs) == 0 {
		// Idempotent: already stopped / never created.
		c.logger.Info("StopContainer: no containers found, already stopped",
			zap.String("app_id", sanitizeForLog(appID, 253)))
		c.mu.Unlock()
		return nil
	}
	stopOrder := c.resolveStopOrder(ctx, appID, ctrs)
	// Mark app as stopping before releasing the mutex so any concurrent
	// CreateContainerWithProgress call will see it and abort (SOC2-CC6, NIST-AC-3).
	if c.appStopping == nil {
		c.appStopping = make(map[string]bool)
	}
	c.appStopping[appID] = true
	c.mu.Unlock()

	var errs []error
	for _, ctrID := range stopOrder {
		if err := c.stopOne(ctx, ctrID); err != nil {
			c.logger.Error("Failed to stop service container",
				zap.String("container_id", ctrID),
				zap.Error(err))
			errs = append(errs, err)
		}
	}

	// Re-acquire mutex for map cleanup. Both reads and writes of these maps
	// are protected by c.mu to prevent data races with concurrent callers
	// (SOC2-CC6, NIST-AC-3, ISO27001-A.8).
	// clearPrimaryPID under the lock; other per-app metadata is kept alive until
	// after the late sweep so that appIsolation is still readable by any
	// concurrent code that observes appStopping (SOC2-CC6, NIST-AC-3).
	c.mu.Lock()
	c.clearPrimaryPID(appID)
	c.mu.Unlock()

	// Re-enumerate unconditionally to catch any containers that appeared after
	// resolveStopOrder snapshotted the list (e.g. a concurrent StartContainer
	// mid-CNI-ADD). stopOne is idempotent for already-stopped containers.
	// appStopping is still set during this sweep to block new concurrent creates.
	if lateCtrs, lateErr := c.containersForApp(ctx, appID); lateErr == nil && len(lateCtrs) > 0 {
		for _, ctr := range lateCtrs {
			if stopErr := c.stopOne(ctx, ctr.ID()); stopErr != nil {
				c.logger.Error("StopContainer: failed to stop late-appearing container",
					zap.String("container_id", ctr.ID()), zap.Error(stopErr))
				errs = append(errs, stopErr)
			}
		}
	}

	// Release per-app metadata in one atomic section AFTER the late sweep, so
	// no partial-state window exists between metadata deletion and appStopping
	// clearance. Concurrent CreateContainerWithProgress remains blocked (via
	// appStopping) until this section completes (SOC2-CC6, NIST-AC-3, NIST-SI-16,
	// ISO27001-A.8, SOC2-CC8/ISO27001-A.12 unbounded-growth prevention).
	c.mu.Lock()
	delete(c.appServices, appID)
	delete(c.appIsolation, appID)
	delete(c.serviceIPs, appID)
	delete(c.appStopping, appID)
	c.mu.Unlock()

	return errors.Join(errs...)
}

// resolveStopOrder returns container IDs in reverse dependency order (dependents first).
// Falls back to arbitrary order for single-container apps or unknown graphs.
// Caller must hold c.mu.
func (c *Client) resolveStopOrder(ctx context.Context, appID string, ctrs []containerd.Container) []string {
	if len(ctrs) <= 1 {
		ids := make([]string, len(ctrs))
		for i, ctr := range ctrs {
			ids[i] = ctr.ID()
		}
		return ids
	}

	services := c.appServices[appID]
	if len(services) == 0 {
		ids := make([]string, len(ctrs))
		for i, ctr := range ctrs {
			ids[i] = ctr.ID()
		}
		return ids
	}

	// Build serviceName→containerID map from containerd labels.
	svcToID := make(map[string]string, len(ctrs))
	for _, ctr := range ctrs {
		labels, err := ctr.Labels(ctx)
		if err != nil {
			continue
		}
		if svcName := labels[labelKeyServiceName]; svcName != "" {
			svcToID[svcName] = ctr.ID()
		}
	}

	ordered, err := appconfig.ServiceTopoOrder(services)
	if err != nil {
		c.logger.Warn("resolveStopOrder: topo sort failed, using arbitrary order",
			zap.String("app_id", appID), zap.Error(err))
		ids := make([]string, len(ctrs))
		for i, ctr := range ctrs {
			ids[i] = ctr.ID()
		}
		return ids
	}

	// Reverse for stop order: dependents first, then dependencies.
	result := make([]string, 0, len(ctrs))
	for i := len(ordered) - 1; i >= 0; i-- {
		if id, ok := svcToID[ordered[i]]; ok {
			result = append(result, id)
		}
	}
	return result
}

// ensureSharedSHM creates the host-side shared memory directory for a
// shared-ipc app group. Returns the path so it can be bind-mounted.
func ensureSharedSHM(appID string) (string, error) {
	if err := appconfig.ValidateAppID(appID); err != nil {
		return "", fmt.Errorf("ensureSharedSHM: %w", err)
	}
	path := "/run/wendy/shm/" + appID
	// Lock the OS thread so that the umask change below is thread-local and
	// does not race with other goroutines on the same process (SOC2-CC6,
	// NIST-SC-7, ISO27001-A.8). Without this, a permissive umask could widen
	// 0o1770 → 0o1750 or looser during the MkdirAll call, creating a window
	// before the subsequent Chmod during which the directory is accessible to
	// unintended users.
	// 0o1700: owner-only sticky directory. The agent runs as root (uid 0) and
	// is the sole writer; group/other bits are cleared so no GID-0 sibling
	// daemon can traverse or modify the shm tree (SOC2-CC6, NIST-AC-3,
	// ISO27001-A.9). The sticky bit prevents any in-container process from
	// unlinking entries owned by a different container even if it somehow
	// gains access to the host mount.
	runtime.LockOSThread()
	oldUmask := syscall.Umask(0)
	mkdirErr := os.MkdirAll(path, 0o1700)
	syscall.Umask(oldUmask)
	runtime.UnlockOSThread()
	if mkdirErr != nil {
		return "", fmt.Errorf("creating shared shm dir %q: %w", path, mkdirErr)
	}
	// Explicit Chmod to handle the case where the directory already existed
	// with looser permissions (MkdirAll is a no-op for existing dirs).
	if err := os.Chmod(path, 0o1700); err != nil {
		return "", fmt.Errorf("setting permissions on shared shm dir %q: %w", path, err)
	}
	return path, nil
}

// deleteOne kills any running task, deletes a single container and its
// snapshot, and stops the D-Bus proxy. It returns the image name so the caller
// can batch image deletions across services. ctx must have the namespace set
// and the caller must hold c.mu.
func (c *Client) deleteOne(ctx context.Context, ctr containerd.Container, wantImg bool) (imgName string, err error) {
	if task, taskErr := ctr.Task(ctx, nil); taskErr == nil {
		_ = task.Kill(ctx, syscall.SIGKILL)
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
	}
	if wantImg {
		if img, imgErr := ctr.Image(ctx); imgErr == nil {
			imgName = img.Name()
		}
	}
	if err := ctr.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return "", fmt.Errorf("deleting container %q: %w", ctr.ID(), err)
	}
	if c.proxyManager != nil {
		if proxyErr := c.proxyManager.Stop(ctr.ID()); proxyErr != nil {
			c.logger.Warn("Failed to stop D-Bus proxy",
				zap.String("container_id", ctr.ID()),
				zap.Error(proxyErr))
		}
	}
	c.logger.Info("Container deleted", zap.String("container_id", ctr.ID()))
	return imgName, nil
}

// DeleteContainer deletes all containers belonging to appID. For multi-service
// apps all service containers are removed. When deleteImage is true, each
// distinct image is deleted once (services sharing an image are handled safely).
func (c *Client) DeleteContainer(ctx context.Context, appID string, deleteImage bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx = c.withNamespace(ctx)
	ctrs, err := c.containersForApp(ctx, appID)
	if err != nil {
		return err
	}
	if len(ctrs) == 0 {
		return nil // Already gone.
	}

	seen := make(map[string]bool)
	var errs []error
	for _, ctr := range ctrs {
		imgName, delErr := c.deleteOne(ctx, ctr, deleteImage)
		if delErr != nil {
			c.logger.Error("Failed to delete service container",
				zap.String("container_id", ctr.ID()),
				zap.Error(delErr))
			errs = append(errs, delErr)
			continue
		}
		if imgName != "" && !seen[imgName] {
			seen[imgName] = true
			imgSvc := c.client.ImageService()
			if err := imgSvc.Delete(ctx, imgName); err != nil && !errdefs.IsNotFound(err) {
				c.logger.Warn("Failed to delete image", zap.String("image", imgName), zap.Error(err))
			} else {
				c.logger.Info("Image deleted", zap.String("image", imgName))
			}
		}
	}
	return errors.Join(errs...)
}

// ListContainers lists all Wendy-managed apps. Multi-service apps (whose
// container IDs follow the {appID}_{serviceName} convention) are grouped under
// their bare appID: the aggregate entry is RUNNING if any service is running,
// and AppContainer.Services is populated with one ServiceEntry per service so
// callers can display individual service state. This ensures that
// stop/start/remove — which address by appID — operate on the same granularity
// shown in the list and picker.
func (c *Client) ListContainers(ctx context.Context) ([]*agentpb.AppContainer, error) {
	ctx = c.withNamespace(ctx)

	ctrs, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q", labelKeyAppVersion))
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	type serviceEntry struct {
		name         string
		runningState agentpb.AppRunningState
	}
	type entry struct {
		version      string
		runningState agentpb.AppRunningState
		mcpPort      uint32
		services     []serviceEntry
	}
	grouped := make(map[string]*entry)
	var order []string

	for _, ctr := range ctrs {
		info, err := ctr.Info(ctx)
		if err != nil {
			c.logger.Warn("Failed to get container info", zap.String("id", ctr.ID()), zap.Error(err))
			continue
		}

		appVersion := info.Labels[labelKeyAppVersion]
		runningState := agentpb.AppRunningState_STOPPED
		if task, err := ctr.Task(ctx, nil); err == nil {
			if st, err := task.Status(ctx); err == nil && st.Status == containerd.Running {
				runningState = agentpb.AppRunningState_RUNNING
			}
		}

		var mcpPort uint32
		if portStr, ok := info.Labels[labelKeyMCPPort]; ok && portStr != "" {
			if p, err := strconv.ParseUint(portStr, 10, 32); err == nil {
				mcpPort = uint32(p)
			}
		}

		// labelKeyAppID is always set by wendyLabels; fall back to container ID
		// for containers created before this label was introduced.
		appID := info.Labels[labelKeyAppID]
		if appID == "" {
			appID = ctr.ID()
		}
		serviceName := info.Labels[labelKeyServiceName]

		svc := serviceEntry{name: serviceName, runningState: runningState}

		if e, ok := grouped[appID]; !ok {
			order = append(order, appID)
			grouped[appID] = &entry{
				version:      appVersion,
				runningState: runningState,
				mcpPort:      mcpPort,
				services:     []serviceEntry{svc},
			}
		} else {
			if runningState == agentpb.AppRunningState_RUNNING {
				e.runningState = agentpb.AppRunningState_RUNNING
			}
			if mcpPort != 0 && e.mcpPort == 0 {
				e.mcpPort = mcpPort
			}
			e.services = append(e.services, svc)
		}
	}

	result := make([]*agentpb.AppContainer, 0, len(grouped))
	for _, appID := range order {
		e := grouped[appID]

		// Populate per-service entries only for multi-service apps; single-service
		// apps leave Services empty so callers can distinguish them cheaply.
		var services []*agentpb.ServiceEntry
		if len(e.services) > 1 {
			services = make([]*agentpb.ServiceEntry, len(e.services))
			for i, s := range e.services {
				services[i] = &agentpb.ServiceEntry{
					Name:         s.name,
					RunningState: s.runningState,
				}
			}
		}

		result = append(result, &agentpb.AppContainer{
			AppName:      appID,
			AppVersion:   e.version,
			RunningState: e.runningState,
			McpPort:      e.mcpPort,
			Services:     services,
		})
	}
	return result, nil
}

func (c *Client) GetContainerMCPPort(ctx context.Context, appName string) (uint32, error) {
	ctx = c.withNamespace(ctx)
	ctr, err := c.client.LoadContainer(ctx, appName)
	if err != nil {
		return 0, fmt.Errorf("loading container %q: %w", appName, err)
	}
	info, err := ctr.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting container info for %q: %w", appName, err)
	}
	portStr, ok := info.Labels[labelKeyMCPPort]
	if !ok || portStr == "" {
		return 0, nil
	}
	p, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing mcp port label for %q: %w", appName, err)
	}
	return uint32(p), nil
}

func (c *Client) GetContainerRestartPolicyLabel(ctx context.Context, appName string) (string, error) {
	ctx = c.withNamespace(ctx)
	ctr, err := c.client.LoadContainer(ctx, appName)
	if err != nil {
		return "", fmt.Errorf("loading container %q: %w", appName, err)
	}
	info, err := ctr.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("getting container info for %q: %w", appName, err)
	}
	return info.Labels[labelKeyRestartPolicy], nil
}

// GetContainerStats collects memory and image-size stats for all Wendy-managed containers.
// Memory is read from cgroup metrics (only available for running tasks). Storage is the
// image size from the content store. Both values are 0 if unavailable.
func (c *Client) GetContainerStats(ctx context.Context) ([]*agentpb.ContainerStats, error) {
	ctx = c.withNamespace(ctx)

	containers, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q", labelKeyAppVersion))
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var result []*agentpb.ContainerStats
	for _, ctr := range containers {
		appName := ctr.ID()
		stat := &agentpb.ContainerStats{AppName: appName}

		// Storage: image size from content store.
		if img, imgErr := ctr.Image(ctx); imgErr == nil {
			if sz, szErr := img.Size(ctx); szErr == nil {
				stat.StorageBytes = sz
			}
		}

		// Memory: cgroup metrics from running task.
		if task, taskErr := ctr.Task(ctx, nil); taskErr == nil {
			if metric, metErr := task.Metrics(ctx); metErr == nil {
				stat.MemoryBytes = extractMemoryBytes(metric)
			}
		}

		result = append(result, stat)
	}
	return result, nil
}

func (c *Client) GetContainerMetrics(ctx context.Context, appName string) (services.ContainerMetrics, error) {
	ctx = c.withNamespace(ctx)
	container, err := c.client.LoadContainer(ctx, appName)
	if err != nil {
		return services.ContainerMetrics{}, err
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return services.ContainerMetrics{}, err
	}
	metric, err := task.Metrics(ctx)
	if err != nil {
		return services.ContainerMetrics{}, err
	}
	return extractContainerMetrics(metric), nil
}

// extractContainerMetrics decodes cgroup v1 or v2 task metrics into a ContainerMetrics snapshot.
func extractContainerMetrics(metric *types.Metric) services.ContainerMetrics {
	switch {
	case typeurl.Is(metric.Data, (*cgroupv1.Metrics)(nil)):
		m := &cgroupv1.Metrics{}
		if err := typeurl.UnmarshalTo(metric.Data, m); err != nil {
			return services.ContainerMetrics{}
		}
		var result services.ContainerMetrics
		if m.CPU != nil && m.CPU.Usage != nil {
			result.UserCPUNanos = int64(m.CPU.Usage.User)
			result.SysCPUNanos = int64(m.CPU.Usage.Kernel)
		}
		if m.Memory != nil && m.Memory.Usage != nil {
			result.MemBytes = int64(m.Memory.Usage.Usage)
		}
		return result
	case typeurl.Is(metric.Data, (*cgroupv2.Metrics)(nil)):
		m := &cgroupv2.Metrics{}
		if err := typeurl.UnmarshalTo(metric.Data, m); err != nil {
			return services.ContainerMetrics{}
		}
		var result services.ContainerMetrics
		if m.CPU != nil {
			result.UserCPUNanos = int64(m.CPU.UserUsec) * 1000
			result.SysCPUNanos = int64(m.CPU.SystemUsec) * 1000
		}
		if m.Memory != nil {
			result.MemBytes = int64(m.Memory.Usage)
		}
		return result
	}
	return services.ContainerMetrics{}
}

// extractMemoryBytes decodes cgroup v1 or v2 task metrics and returns memory usage in bytes.
func extractMemoryBytes(metric *types.Metric) int64 {
	return extractContainerMetrics(metric).MemBytes
}

func streamReader(r io.Reader, ch chan<- services.ContainerOutput, buildOutput func([]byte) services.ContainerOutput) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			ch <- buildOutput(data)
		}
		if err != nil {
			return
		}
	}
}

// hasBluetooth returns true if the app config includes a bluetooth entitlement.
func hasBluetooth(cfg *appconfig.AppConfig) bool {
	for _, ent := range cfg.Entitlements {
		if ent.Type == appconfig.EntitlementBluetooth {
			return true
		}
	}
	return false
}
