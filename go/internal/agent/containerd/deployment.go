package containerd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/google/uuid"
	"github.com/opencontainers/image-spec/identity"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	localoci "github.com/wendylabsinc/wendy/go/internal/agent/oci"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	labelDeploymentRevision = "sh.wendy/deployment.revision"
	labelDeploymentPending  = "sh.wendy/deployment.pending"
	labelRevisionContainer  = "sh.wendy/revision.container"
	labelRevisionPhase      = "sh.wendy/revision.phase"
	revisionExtension       = "sh.wendy/revision.metadata"
	revisionMetadataType    = "wendy.internal/retained-container.v1"
)

type deploymentCreateKey struct{}
type deploymentCreateOptions struct{ revision, snapshotKey string }

func deploymentCreateFromContext(ctx context.Context) *deploymentCreateOptions {
	v, _ := ctx.Value(deploymentCreateKey{}).(*deploymentCreateOptions)
	return v
}

// A hidden containerd container is both the journal and the snapshot GC root.
// It deliberately carries no Wendy app/version labels, so normal listings and
// boot reconciliation never try to start it. The original metadata is encoded
// separately: changing the journal's labels cannot change the rollback spec.
type retainedMetadata struct {
	Original         *retainedContainer `json:"original,omitempty"`
	WasRunning       bool               `json:"wasRunning"`
	Revision         string             `json:"revision"`
	PreviousRevision string             `json:"previousRevision,omitempty"`
	CandidateImage   string             `json:"candidateImage"`
}
type retainedContainer struct {
	ID                                  string
	Labels                              map[string]string
	Image                               string
	RuntimeName                         string
	RuntimeOptions                      *anypb.Any
	Spec                                *anypb.Any
	SnapshotKey, Snapshotter, SandboxID string
	Extensions                          map[string]*anypb.Any
}

func copyAny(v typeurl.Any) *anypb.Any {
	if v == nil {
		return nil
	}
	return &anypb.Any{TypeUrl: v.GetTypeUrl(), Value: append([]byte(nil), v.GetValue()...)}
}
func retainContainer(info containers.Container) *retainedContainer {
	ext := make(map[string]*anypb.Any, len(info.Extensions))
	for k, v := range info.Extensions {
		ext[k] = copyAny(v)
	}
	return &retainedContainer{ID: info.ID, Labels: maps.Clone(info.Labels), Image: info.Image,
		RuntimeName: info.Runtime.Name, RuntimeOptions: copyAny(info.Runtime.Options), Spec: copyAny(info.Spec),
		SnapshotKey: info.SnapshotKey, Snapshotter: info.Snapshotter, SandboxID: info.SandboxID, Extensions: ext}
}
func (r *retainedContainer) container() containers.Container {
	ext := make(map[string]typeurl.Any, len(r.Extensions))
	for k, v := range r.Extensions {
		ext[k] = copyAny(v)
	}
	return containers.Container{ID: r.ID, Labels: maps.Clone(r.Labels), Image: r.Image,
		Runtime: containers.RuntimeInfo{Name: r.RuntimeName, Options: r.RuntimeOptions}, Spec: r.Spec,
		SnapshotKey: r.SnapshotKey, Snapshotter: r.Snapshotter, SandboxID: r.SandboxID, Extensions: ext}
}
func encodeRevisionMetadata(m retainedMetadata) (typeurl.Any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &anypb.Any{TypeUrl: revisionMetadataType, Value: b}, nil
}
func decodeRevisionMetadata(info containers.Container) (retainedMetadata, error) {
	var m retainedMetadata
	v := info.Extensions[revisionExtension]
	if v == nil || v.GetTypeUrl() != revisionMetadataType {
		return m, fmt.Errorf("invalid deployment journal %q", info.ID)
	}
	if err := json.Unmarshal(v.GetValue(), &m); err != nil {
		return m, err
	}
	if m.Revision == "" || info.Labels[labelRevisionContainer] == "" {
		return m, fmt.Errorf("incomplete deployment journal %q", info.ID)
	}
	if m.Original != nil && m.Original.ID != info.Labels[labelRevisionContainer] {
		return m, fmt.Errorf("deployment journal container identity mismatch")
	}
	return m, nil
}

type deploymentTransaction struct {
	c                                        *Client
	req                                      *agentpb.CreateContainerRequest
	cfg                                      *appconfig.AppConfig
	journal                                  containers.Container
	metadata                                 retainedMetadata
	resume                                   func()
	activated, committed, rolledBack, closed bool
}

var _ services.DeploymentRuntime = (*Client)(nil)
var _ services.DeploymentRevisionRemover = (*Client)(nil)
var _ services.DeploymentTransaction = (*deploymentTransaction)(nil)

// resolveDeploymentImage does all fallible image work before cutover. It does
// not allocate app-keyed sockets, stop proxies, or alter the existing task.
func (c *Client) resolveDeploymentImage(ctx context.Context, imageName string) (containerd.Image, error) {
	image, err := c.client.GetImage(ctx, imageName)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("resolving image %q: %w", imageName, err)
		}
		pullOpts := []containerd.RemoteOpt{containerd.WithPullUnpack}
		if isLocalRegistryImage(imageName) {
			pullOpts = append(pullOpts, containerd.WithResolver(docker.NewResolver(docker.ResolverOptions{PlainHTTP: true})))
		}
		image, err = c.client.Pull(ctx, imageName, pullOpts...)
		if err != nil {
			return nil, fmt.Errorf("getting/pulling image %q: %w", imageName, err)
		}
	}
	if _, err = image.Spec(ctx); err != nil {
		return nil, fmt.Errorf("reading image config for %q: %w", imageName, err)
	}
	unpacked, err := image.IsUnpacked(ctx, c.snapshotter)
	if err != nil {
		return nil, fmt.Errorf("checking image unpack: %w", err)
	}
	if !unpacked {
		if err = c.UnpackImage(ctx, image, nil); err != nil {
			return nil, fmt.Errorf("unpacking image %q: %w", imageName, err)
		}
	}
	return image, nil
}

func (c *Client) PrepareDeployment(ctx context.Context, req *agentpb.CreateContainerRequest, cfg *appconfig.AppConfig) (_ services.DeploymentTransaction, retErr error) {
	if req == nil || cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "deployment requires an app configuration")
	}
	if err := cfg.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid app configuration: %v", err)
	}
	if appconfig.IsSharedNamespaceIsolation(cfg.Isolation) {
		return nil, status.Error(codes.FailedPrecondition, "verified deployment of shared-namespace service groups is not supported; deploy an isolated service")
	}
	name := cfg.ContainerName()
	if name != req.GetAppName() {
		return nil, status.Error(codes.InvalidArgument, "deployment container name does not match app configuration")
	}
	if err := validateUserEnv(req.GetEnv()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid environment: %v", err)
	}
	if err := c.requireDBusProxy(cfg, name); err != nil {
		return nil, err
	}
	if entitlementsContain(cfg.Entitlements, appconfig.EntitlementNotifications) && c.systemAPISocketProvider == nil {
		return nil, status.Error(codes.FailedPrecondition, "notifications entitlement requires the app System API socket manager")
	}
	if err := localoci.ApplyResourceLimits(localoci.DefaultSpec("rootfs", nil), cfg.ResolveResourcesForService(cfg.ServiceName)); err != nil {
		return nil, err
	}
	ctx = c.withNamespace(ctx)
	image, err := c.resolveDeploymentImage(ctx, normalizeImageName(req.GetImageName()))
	if err != nil {
		return nil, err
	}

	t := &deploymentTransaction{c: c, req: proto.Clone(req).(*agentpb.CreateContainerRequest), cfg: cfg}
	t.metadata.Revision = uuid.NewString()
	// The per-app service lock spans this transaction; suppression also excludes
	// autonomous monitor restarts through health verification and rollback.
	t.resume = c.suppressRestarts(name)
	defer func() {
		if retErr != nil {
			t.resume()
		}
	}()
	unlock := c.lockNetworkOperation(name)
	defer unlock()
	old, err := c.client.LoadContainer(ctx, name)
	if err == nil {
		info, err := old.Info(ctx)
		if err != nil {
			return nil, err
		}
		if appconfig.IsSharedNamespaceIsolation(info.Labels[labelKeyIsolation]) {
			return nil, status.Error(codes.FailedPrecondition, "the previous revision belongs to a shared-namespace group and cannot be rolled back independently")
		}
		if deploymentIsPending(info.Labels) {
			return nil, status.Error(codes.FailedPrecondition, "the previous deployment is unresolved; recover it before starting another deployment")
		}
		t.metadata.Original = retainContainer(info)
		t.metadata.PreviousRevision = info.Labels[labelDeploymentRevision]
		if t.metadata.PreviousRevision == "" {
			t.metadata.PreviousRevision = "legacy-" + t.metadata.Revision
		}
		if task, taskErr := old.Task(ctx, nil); taskErr == nil {
			s, err := task.Status(ctx)
			if err != nil {
				return nil, err
			}
			t.metadata.WasRunning = s.Status == containerd.Running
		} else if !errdefs.IsNotFound(taskErr) {
			return nil, taskErr
		}
	} else if !errdefs.IsNotFound(err) {
		return nil, err
	}

	// Pin the candidate under an immutable per-revision name. Another upload to
	// :latest cannot change the image used between prepare and activation.
	imageName := normalizeImageName("wendy-revision/" + t.metadata.Revision + ":candidate")
	if _, err = c.client.ImageService().Create(ctx, images.Image{Name: imageName, Target: image.Target()}); err != nil {
		return nil, fmt.Errorf("pinning candidate image: %w", err)
	}
	t.metadata.CandidateImage = imageName
	t.req.ImageName = imageName
	defer func() {
		if retErr != nil {
			_ = c.cleanupRuntimeOperation(func(ctx context.Context) error { return c.client.ImageService().Delete(ctx, imageName) })
		}
	}()
	rootfs, err := image.RootFS(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading candidate rootfs: %w", err)
	}
	parent := identity.ChainID(rootfs).String()
	prepared := c.takePreparedSnapshot(normalizeImageName(req.GetImageName()), parent)
	if prepared == nil {
		prepareCtx, release, err := c.client.WithLease(ctx, leases.WithExpiration(unpackLeaseExpiration))
		if err != nil {
			return nil, fmt.Errorf("leasing candidate snapshot: %w", err)
		}
		key := "wendy-deploy-" + t.Revision()
		sn := c.client.SnapshotService(c.snapshotter)
		if _, err := sn.Prepare(prepareCtx, key, parent); err != nil {
			_ = c.cleanupRuntimeOperation(release)
			return nil, fmt.Errorf("preparing candidate writable snapshot: %w", err)
		}
		prepared = &preparedSnapshot{key: key, parent: parent,
			release: func() { _ = c.cleanupRuntimeOperation(release) },
			remove: func() {
				_ = c.cleanupRuntimeOperation(func(ctx context.Context) error { return sn.Remove(ctx, key) })
			},
		}
	}
	c.storePreparedSnapshot(imageName, prepared)
	defer func() {
		if retErr != nil {
			c.takePreparedSnapshot(imageName, "").discard()
		}
	}()
	encoded, err := encodeRevisionMetadata(t.metadata)
	if err != nil {
		return nil, err
	}
	journal := containers.Container{ID: "wendy-revision-" + t.metadata.Revision,
		Labels:     map[string]string{labelRevisionContainer: name, labelRevisionPhase: "prepared"},
		Runtime:    containers.RuntimeInfo{Name: "io.containerd.runc.v2"},
		Extensions: map[string]typeurl.Any{revisionExtension: encoded}}
	if original := t.metadata.Original; original != nil {
		journal.Runtime = containers.RuntimeInfo{Name: original.RuntimeName, Options: original.RuntimeOptions}
		journal.Spec = original.Spec
		journal.SnapshotKey, journal.Snapshotter = original.SnapshotKey, original.Snapshotter
		if original.Labels[labelDeploymentRevision] != "" {
			// Verified images use immutable revision-specific aliases. Retain
			// their content too; legacy :latest remains snapshot/spec-only.
			journal.Image = original.Image
		}
	} else {
		journal.Spec, err = typeurl.MarshalAny(&runtimespec.Spec{})
		if err != nil {
			return nil, err
		}
	}
	t.journal, err = c.client.ContainerService().Create(ctx, journal)
	if err != nil {
		return nil, fmt.Errorf("retaining previous container revision: %w", err)
	}
	return t, nil
}

func (t *deploymentTransaction) Revision() string         { return t.metadata.Revision }
func (t *deploymentTransaction) PreviousRevision() string { return t.metadata.PreviousRevision }
func (t *deploymentTransaction) PreviousWasRunning() bool { return t.metadata.WasRunning }
func (t *deploymentTransaction) setPhase(ctx context.Context, phase string) error {
	t.journal.Labels[labelRevisionPhase] = phase
	_, err := t.c.client.ContainerService().Update(t.c.withNamespace(ctx), t.journal, "labels")
	return err
}
func (t *deploymentTransaction) Activate(ctx context.Context) error {
	if t.closed || t.activated {
		return fmt.Errorf("deployment has already been activated or closed")
	}
	if err := t.setPhase(ctx, "activating"); err != nil {
		return err
	}
	t.activated = true // Rollback must run even if the graceful stop fails.
	name := t.journal.Labels[labelRevisionContainer]
	if t.metadata.Original != nil {
		if err := t.c.StopContainer(ctx, name); err != nil {
			return fmt.Errorf("stopping previous revision: %w", err)
		}
	}
	ctx = context.WithValue(ctx, deploymentCreateKey{}, &deploymentCreateOptions{revision: t.Revision(), snapshotKey: "wendy-deploy-" + t.Revision()})
	return t.c.CreateContainer(ctx, t.req, t.cfg)
}
func (t *deploymentTransaction) Commit(ctx context.Context) error {
	if !t.activated || t.closed || t.rolledBack {
		return fmt.Errorf("deployment is not active")
	}
	ctx = t.c.withNamespace(ctx)
	info, err := t.c.client.ContainerService().Get(ctx, t.journal.Labels[labelRevisionContainer])
	if err != nil {
		return err
	}
	if info.Labels[labelDeploymentRevision] != t.Revision() {
		return fmt.Errorf("active container revision changed during deployment")
	}
	delete(info.Labels, labelDeploymentPending)
	if _, err = t.c.client.ContainerService().Update(ctx, info, "labels"); err != nil {
		return err
	}
	// Removing pending is the durable commit point. A crash before updating the
	// journal is recognized by RecoverDeployments and never rolls back success.
	t.committed = true
	if err := t.setPhase(ctx, "retained"); err != nil {
		t.c.logger.Warn("Deployment committed; journal finalization will resume on restart", zap.Error(err))
		return nil
	}
	if err := t.pruneOlderRevisions(ctx); err != nil {
		t.c.logger.Warn("Deployment committed; older revision cleanup failed", zap.Error(err))
	}
	return nil
}

func (t *deploymentTransaction) Rollback(ctx context.Context) (<-chan services.ContainerOutput, error) {
	if t.committed {
		return nil, fmt.Errorf("cannot roll back a committed transaction")
	}
	if t.rolledBack {
		return nil, nil
	}
	if !t.activated {
		return nil, nil
	}
	ctx = t.c.withNamespace(ctx)
	name := t.journal.Labels[labelRevisionContainer]
	current, err := t.c.client.ContainerService().Get(ctx, name)
	if err == nil && t.metadata.Original != nil && current.SnapshotKey == t.metadata.Original.SnapshotKey {
		// Activation stopped the old task but failed before replacing metadata.
		// Keep its snapshot and rebuild its transient resources before restarting.
		if current.Labels[labelDeploymentRevision] == "" {
			current.Labels[labelDeploymentRevision] = t.PreviousRevision()
			if _, err := t.c.client.ContainerService().Update(ctx, current, "labels"); err != nil {
				return nil, err
			}
		}
	} else {
		if err == nil {
			if current.Labels[labelDeploymentRevision] != t.Revision() {
				return nil, fmt.Errorf("refusing rollback: another revision now owns %q", name)
			}
			if err := t.c.StopContainer(ctx, name); err != nil {
				return nil, err
			}
			if err := t.c.DeleteContainer(ctx, name, false); err != nil {
				return nil, err
			}
		} else if !errdefs.IsNotFound(err) {
			return nil, err
		}
		if t.metadata.Original != nil {
			// Recreate metadata directly. Do not resolve Image, run CreateContainer,
			// or allocate a new snapshot: :latest may already refer to bad code.
			original := t.metadata.Original.container()
			if original.Labels == nil {
				original.Labels = make(map[string]string)
			}
			original.Labels[labelDeploymentRevision] = t.PreviousRevision()
			if _, err := t.c.client.ContainerService().Create(ctx, original); err != nil {
				return nil, fmt.Errorf("restoring retained container metadata: %w", err)
			}
		}
	}
	var output <-chan services.ContainerOutput
	if original := t.metadata.Original; original != nil {
		if err := t.c.restoreDeploymentResources(ctx, original); err != nil {
			return nil, err
		}
		if t.metadata.WasRunning {
			output, err = t.c.StartContainer(ctx, name, "", nil)
			if err != nil {
				return output, fmt.Errorf("restarting previous revision: %w", err)
			}
		}
	}
	t.rolledBack = true
	if err := t.setPhase(ctx, "restored"); err != nil {
		return output, err
	}
	return output, nil
}

func (c *Client) restoreDeploymentResources(ctx context.Context, r *retainedContainer) error {
	appID, serviceName := r.Labels[labelKeyAppID], r.Labels[labelKeyServiceName]
	if appID == "" {
		appID = r.ID
	}
	if err := appconfig.ValidateAppID(appID); err != nil {
		return err
	}
	if serviceName != "" {
		if err := appconfig.ValidateServiceName(serviceName); err != nil {
			return err
		}
	}
	entitlements := parseEntitlementsFromAnnotations(r.Labels)
	c.mu.Lock()
	if c.appIsolation == nil {
		c.appIsolation = make(map[string]string)
	}
	c.appIsolation[appID] = r.Labels[labelKeyIsolation]
	c.mu.Unlock()
	if entitlementsContain(entitlements, appconfig.EntitlementBluetooth) {
		if c.proxyManager == nil {
			return fmt.Errorf("restoring Bluetooth proxy: manager unavailable")
		}
		if _, err := c.proxyManager.Start(ctx, r.ID); err != nil {
			return err
		}
	}
	if entitlementsContain(entitlements, appconfig.EntitlementNotifications) {
		if c.systemAPISocketProvider == nil {
			return fmt.Errorf("restoring app System API socket: manager unavailable")
		}
		if _, err := c.systemAPISocketProvider.Ensure(appID, serviceName, []string{services.SystemAPICapabilityNotifications}); err != nil {
			return err
		}
	}
	// A stopped or replaced revision's saved network namespace path may no
	// longer exist. StartContainer checks and recreates CNI/DNS from these labels.
	return nil
}

func (t *deploymentTransaction) Close(ctx context.Context) error {
	if t.closed {
		return nil
	}
	t.closed = true
	defer t.resume()
	if t.committed || (t.activated && !t.rolledBack) {
		return nil
	}
	ctx = t.c.withNamespace(ctx)
	// The snapshot may still be waiting under the candidate alias if prepare
	// was canceled, or activation failed before container creation consumed it.
	t.c.takePreparedSnapshot(t.metadata.CandidateImage, "").discard()
	// Metadata-only deletion: the restored/original live container owns this
	// same writable snapshot. WithSnapshotCleanup here would destroy it.
	err := t.c.client.ContainerService().Delete(ctx, t.journal.ID)
	if errdefs.IsNotFound(err) {
		err = nil
	}
	imageErr := t.c.client.ImageService().Delete(ctx, t.metadata.CandidateImage)
	if errdefs.IsNotFound(imageErr) {
		imageErr = nil
	}
	return errors.Join(err, imageErr)
}

func (t *deploymentTransaction) pruneOlderRevisions(ctx context.Context) error {
	// Keep exactly one preceding revision per concrete container. Explicit
	// snapshot deletion is safe only after excluding every current container's
	// snapshot key; an interrupted prepared transaction may share that snapshot.
	all, err := t.c.client.ContainerService().List(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	aliases := map[string]bool{}
	for _, info := range all {
		if info.Labels[labelRevisionContainer] == "" {
			live[info.Snapshotter+"\x00"+info.SnapshotKey] = true
		}
	}
	for _, info := range all {
		if info.ID == t.journal.ID || info.Labels[labelRevisionContainer] != t.journal.Labels[labelRevisionContainer] {
			continue
		}
		if info.Labels[labelRevisionPhase] != "retained" {
			continue
		}
		if err := t.c.client.ContainerService().Delete(ctx, info.ID); err != nil {
			return err
		}
		aliases[info.Image] = true
		if info.SnapshotKey != "" && !live[info.Snapshotter+"\x00"+info.SnapshotKey] && info.SnapshotKey != t.journal.SnapshotKey {
			if err := t.c.client.SnapshotService(info.Snapshotter).Remove(ctx, info.SnapshotKey); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
		}
		if m, err := decodeRevisionMetadata(info); err == nil {
			aliases[m.CandidateImage] = true
		}
	}
	remaining, err := t.c.client.ContainerService().List(ctx)
	if err != nil {
		return err
	}
	for _, info := range remaining {
		delete(aliases, info.Image)
		if m, err := decodeRevisionMetadata(info); err == nil {
			delete(aliases, m.CandidateImage)
			if m.Original != nil {
				delete(aliases, m.Original.Image)
			}
		}
	}
	for alias := range aliases {
		// Never garbage collect arbitrary user image names or legacy :latest.
		if !strings.HasPrefix(alias, "docker.io/wendy-revision/") {
			continue
		}
		if err := t.c.client.ImageService().Delete(ctx, alias); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// DeleteDeploymentRevisions is called only after an explicit user deletion,
// under the service's app lock. Internal replacement and rollback deletion must
// retain these journals. A tombstone prevents interrupted cleanup from turning
// a pending deployment into a request to restore an intentionally removed app.
func (c *Client) DeleteDeploymentRevisions(ctx context.Context, containerNames []string) error {
	return c.deleteDeploymentRevisions(ctx, containerNames, false)
}

func (c *Client) deleteDeploymentRevisions(ctx context.Context, containerNames []string, onlyDeleted bool) error {
	ctx = c.withNamespace(ctx)
	names := make(map[string]bool, len(containerNames))
	for _, name := range containerNames {
		if name != "" {
			names[name] = true
		}
	}
	all, err := c.client.ContainerService().List(ctx)
	if err != nil {
		return err
	}
	var deleted []containers.Container
	var errs []error
	for _, info := range all {
		if !names[info.Labels[labelRevisionContainer]] || (onlyDeleted && info.Labels[labelRevisionPhase] != "deleted") {
			continue
		}
		info.Labels[labelRevisionPhase] = "deleted"
		if _, err := c.client.ContainerService().Update(ctx, info, "labels"); err != nil {
			errs = append(errs, fmt.Errorf("tombstoning deployment journal %s: %w", info.ID, err))
			continue
		}
		deleted = append(deleted, info)
	}
	if len(errs) > 0 {
		// Keep successfully written tombstones if durable intent could not be
		// recorded for the whole selection; report the incomplete deletion.
		return errors.Join(errs...)
	}
	// Tombstone all selected records before removing any, so recovery can finish
	// cleanup even if the process stops between two journal deletions.
	type snapshotReference struct{ snapshotter, key string }
	snapshotRefs := map[snapshotReference]bool{}
	aliases := map[string]bool{}
	for _, info := range deleted {
		if err := c.client.ContainerService().Delete(ctx, info.ID); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting deployment journal %s: %w", info.ID, err))
			continue
		}
		if info.SnapshotKey != "" {
			snapshotRefs[snapshotReference{info.Snapshotter, info.SnapshotKey}] = true
		}
		aliases[info.Image] = true
		if m, err := decodeRevisionMetadata(info); err == nil {
			c.takePreparedSnapshot(m.CandidateImage, "").discard()
			aliases[m.CandidateImage] = true
			if m.Original != nil {
				aliases[m.Original.Image] = true
			}
		}
	}
	remaining, err := c.client.ContainerService().List(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, info := range remaining {
		// Protect both real containers and every remaining journal. A snapshot
		// may be shared with a restored live revision or another GC root.
		delete(snapshotRefs, snapshotReference{info.Snapshotter, info.SnapshotKey})
		delete(aliases, info.Image)
		if m, err := decodeRevisionMetadata(info); err == nil {
			delete(aliases, m.CandidateImage)
			if m.Original != nil {
				delete(aliases, m.Original.Image)
			}
		}
	}
	for ref := range snapshotRefs {
		if err := c.client.SnapshotService(ref.snapshotter).Remove(ctx, ref.key); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting retained snapshot %s: %w", ref.key, err))
		}
	}
	for alias := range aliases {
		if !strings.HasPrefix(alias, "docker.io/wendy-revision/") {
			continue // --delete-image remains responsible for user image names.
		}
		if err := c.client.ImageService().Delete(ctx, alias); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting retained image %s: %w", alias, err))
		}
	}
	return errors.Join(errs...)
}

// RecoverDeployments must run before the container restart monitor on startup.
// Uncommitted candidates are rolled back using only durable local metadata.
func (c *Client) RecoverDeployments(ctx context.Context) error {
	ctx = c.withNamespace(ctx)
	entries, err := c.client.ContainerService().List(ctx, fmt.Sprintf("labels.%q", labelRevisionContainer))
	if err != nil {
		return err
	}
	var deletedNames []string
	for _, entry := range entries {
		if entry.Labels[labelRevisionPhase] == "deleted" {
			deletedNames = append(deletedNames, entry.Labels[labelRevisionContainer])
		}
	}
	if len(deletedNames) > 0 {
		// A newer deployment may exist for a name whose earlier deletion only
		// partially cleaned up. Its journal remains an independent recovery root.
		if err := c.deleteDeploymentRevisions(ctx, deletedNames, true); err != nil {
			return fmt.Errorf("finishing deleted deployment cleanup: %w", err)
		}
		// Cleanup can remove other journals for the same deleted app. Never use
		// a stale listing to resurrect one of those just-deleted records.
		entries, err = c.client.ContainerService().List(ctx, fmt.Sprintf("labels.%q", labelRevisionContainer))
		if err != nil {
			return err
		}
	}
	var errs []error
	for _, entry := range entries {
		phase := entry.Labels[labelRevisionPhase]
		if phase == "retained" {
			continue
		}
		m, err := decodeRevisionMetadata(entry)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		t := &deploymentTransaction{c: c, journal: entry, metadata: m, resume: c.suppressRestarts(entry.Labels[labelRevisionContainer]), activated: phase == "activating", rolledBack: phase == "restored"}
		if t.activated {
			current, currentErr := c.client.ContainerService().Get(ctx, entry.Labels[labelRevisionContainer])
			if currentErr == nil && current.Labels[labelDeploymentRevision] == m.Revision && current.Labels[labelDeploymentPending] == "" {
				t.committed = true
				err = t.setPhase(ctx, "retained")
			} else {
				var output <-chan services.ContainerOutput
				output, err = t.Rollback(ctx)
				if output != nil {
					go func() {
						for range output {
						}
					}()
				}
			}
		}
		if closeErr := t.Close(ctx); closeErr != nil {
			errs = append(errs, closeErr)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("recovering deployment %s: %w", m.Revision, err))
		} else {
			c.logger.Info("Recovered deployment journal", zap.String("revision", m.Revision), zap.String("phase", phase))
		}
	}
	return errors.Join(errs...)
}

// prevent premature deletion of a pending candidate by boot reconciliation.
func deploymentIsPending(labels map[string]string) bool { return labels[labelDeploymentPending] != "" }
