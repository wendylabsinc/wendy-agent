//go:build linux

package containerd

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Run only against an explicitly supplied disposable Linux containerd daemon:
//
// WENDY_DEPLOYMENT_TEST_SOCKET=/tmp/containerd.sock \
// WENDY_DEPLOYMENT_TEST_BUSYBOX=/bin/busybox \
// go test ./go/internal/agent/containerd -run TestDeploymentIntegration -v
//
// The BusyBox binary must be static and match the host architecture. The test
// makes its own small OCI images, uses a unique namespace, and needs no registry
// access. Its container has host networking to avoid requiring a CNI setup.
func TestDeploymentIntegration(t *testing.T) {
	socket := os.Getenv("WENDY_DEPLOYMENT_TEST_SOCKET")
	if socket == "" {
		t.Skip("set WENDY_DEPLOYMENT_TEST_SOCKET to a disposable containerd daemon")
	}
	busyboxPath := os.Getenv("WENDY_DEPLOYMENT_TEST_BUSYBOX")
	if busyboxPath == "" {
		busyboxPath = "/bin/busybox"
	}
	busybox, err := os.ReadFile(busyboxPath)
	if err != nil {
		t.Fatalf("read static BusyBox (WENDY_DEPLOYMENT_TEST_BUSYBOX): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	namespace := fmt.Sprintf("wendy-deployment-it-%d", time.Now().UnixNano())
	c := deploymentIntegrationClient(t, socket, namespace)
	const appName = "com.wendy.deployment.integration"
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		deploymentIntegrationCleanup(t, c, cleanupCtx, appName)
		_ = c.Close()
	})

	goodImage := deploymentIntegrationImage(t, c, ctx, busybox, "first")
	badImage := deploymentIntegrationImage(t, c, ctx, busybox, "candidate")
	nextImage := deploymentIntegrationImage(t, c, ctx, busybox, "second")
	probe := &appconfig.ReadinessConfig{Exec: []string{"/bin/busybox", "test", "-f", "/image-version"}}

	first := deploymentIntegrationPrepare(t, c, ctx, appName, goodImage, probe)
	deploymentIntegrationStart(t, c, ctx, appName, first)
	if err := c.ProbeReadiness(ctx, appName, probe); err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit initial revision: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	original := deploymentIntegrationInfo(t, c, ctx, appName)
	originalPID := deploymentIntegrationPID(t, c, ctx, appName)
	deploymentIntegrationExec(t, c, ctx, appName, "sh", "-c", "printf preserved > /state/marker")
	t.Logf("initial revision %s running; snapshot %s, PID %d", first.Revision(), original.SnapshotKey, originalPID)

	// A pull failure must not stop the current task or replace its snapshot.
	missingCfg, missingReq := deploymentIntegrationConfig(t, appName, "localhost:1/wendy-integration/missing:latest", probe)
	pullCtx, stopPull := context.WithTimeout(ctx, 3*time.Second)
	_, prepareErr := c.PrepareDeployment(pullCtx, missingReq, missingCfg)
	stopPull()
	if prepareErr == nil {
		t.Fatal("missing image unexpectedly prepared")
	}
	deploymentIntegrationAssertRevision(t, c, ctx, appName, original)
	if got := deploymentIntegrationPID(t, c, ctx, appName); got != originalPID {
		t.Fatalf("failed preparation replaced running process: got PID %d, want %d", got, originalPID)
	}
	t.Log("image preparation failure preserved the running process and snapshot")

	// The candidate really starts, but its in-container exec probe fails. Its
	// writable snapshot must be independent of the retained live revision.
	badProbe := &appconfig.ReadinessConfig{Exec: []string{"/bin/busybox", "false"}}
	failed := deploymentIntegrationPrepare(t, c, ctx, appName, badImage, badProbe)
	deploymentIntegrationStart(t, c, ctx, appName, failed)
	failedInfo := deploymentIntegrationInfo(t, c, ctx, appName)
	if failedInfo.SnapshotKey == original.SnapshotKey {
		t.Fatal("candidate reused the previous writable snapshot")
	}
	if got := deploymentIntegrationExec(t, c, ctx, appName, "cat", "/image-version"); got != "candidate" {
		t.Fatalf("candidate did not run new image contents: %q", got)
	}
	if err := c.ProbeReadiness(ctx, appName, badProbe); err == nil {
		t.Fatal("failing readiness command unexpectedly passed")
	}
	out, err := failed.Rollback(ctx)
	deploymentIntegrationDrain(out)
	if err != nil {
		t.Fatalf("rollback failed candidate: %v", err)
	}
	if err := failed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	deploymentIntegrationAssertRevision(t, c, ctx, appName, original)
	if got := deploymentIntegrationExec(t, c, ctx, appName, "cat", "/state/marker"); got != "preserved" {
		t.Fatalf("rollback lost previous writable contents: %q", got)
	}
	if got := deploymentIntegrationExec(t, c, ctx, appName, "cat", "/image-version"); got != "first" {
		t.Fatalf("rollback restored wrong image contents: %q", got)
	}
	if _, err := c.client.SnapshotService(c.snapshotter).Stat(c.withNamespace(ctx), failedInfo.SnapshotKey); !errdefs.IsNotFound(err) {
		t.Fatalf("failed candidate snapshot remains after rollback: %v", err)
	}
	t.Log("failed readiness restored previous spec, writable contents, snapshot, and running process")

	// A successful replacement keeps exactly the preceding revision available.
	second := deploymentIntegrationPrepare(t, c, ctx, appName, nextImage, probe)
	deploymentIntegrationStart(t, c, ctx, appName, second)
	if err := c.ProbeReadiness(ctx, appName, probe); err != nil {
		t.Fatalf("second revision readiness: %v", err)
	}
	deploymentIntegrationExec(t, c, ctx, appName, "test", "!", "-f", "/state/marker")
	if err := second.Commit(ctx); err != nil {
		t.Fatalf("commit second revision: %v", err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	secondInfo := deploymentIntegrationInfo(t, c, ctx, appName)
	deploymentIntegrationExec(t, c, ctx, appName, "sh", "-c", "printf second-state > /state/marker")
	entries, err := c.client.ContainerService().List(c.withNamespace(ctx), fmt.Sprintf("labels.%q==%q", labelRevisionContainer, appName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("retained %d revisions, want exactly one", len(entries))
	}
	retained, err := decodeRevisionMetadata(entries[0])
	if err != nil || retained.Original == nil || retained.Original.SnapshotKey != original.SnapshotKey {
		t.Fatalf("incorrect previous revision retained: %+v, error %v", retained, err)
	}
	if _, err := c.client.SnapshotService(c.snapshotter).Stat(c.withNamespace(ctx), original.SnapshotKey); err != nil {
		t.Fatalf("retained snapshot unavailable: %v", err)
	}
	t.Log("successful replacement retained exactly the previous revision's snapshot")

	// Simulate losing the agent after cutover but before Commit/Rollback. Close
	// only the client, deliberately leaving the transaction journal pending.
	interrupted := deploymentIntegrationPrepare(t, c, ctx, appName, badImage, badProbe)
	deploymentIntegrationStart(t, c, ctx, appName, interrupted)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = deploymentIntegrationClient(t, socket, namespace)
	if err := c.RecoverDeployments(ctx); err != nil {
		t.Fatalf("recover interrupted activation: %v", err)
	}
	deploymentIntegrationAssertRevision(t, c, ctx, appName, secondInfo)
	if got := deploymentIntegrationExec(t, c, ctx, appName, "cat", "/state/marker"); got != "second-state" {
		t.Fatalf("recovery lost second revision's writable contents: %q", got)
	}
	if got := deploymentIntegrationExec(t, c, ctx, appName, "cat", "/image-version"); got != "second" {
		t.Fatalf("recovery started wrong image contents: %q", got)
	}
	recoveredPID := deploymentIntegrationPID(t, c, ctx, appName)
	if err := c.RecoverDeployments(ctx); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	if got := deploymentIntegrationPID(t, c, ctx, appName); got != recoveredPID {
		t.Fatalf("repeat recovery restarted the recovered task: %d -> %d", recoveredPID, got)
	}
	t.Log("fresh client recovered interrupted activation; repeat recovery preserved the restored task")
}

func deploymentIntegrationClient(t *testing.T, socket, namespace string) *Client {
	t.Helper()
	c, err := NewClient(zap.NewNop(), socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.namespace = namespace
	return c
}

func deploymentIntegrationConfig(t *testing.T, name, image string, probe *appconfig.ReadinessConfig) (*appconfig.AppConfig, *agentpb.CreateContainerRequest) {
	t.Helper()
	cfg := &appconfig.AppConfig{AppID: name, Readiness: probe,
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "host"}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, &agentpb.CreateContainerRequest{AppName: name, ImageName: image, AppConfig: b}
}

func deploymentIntegrationPrepare(t *testing.T, c *Client, ctx context.Context, name, image string, probe *appconfig.ReadinessConfig) services.DeploymentTransaction {
	t.Helper()
	cfg, req := deploymentIntegrationConfig(t, name, image, probe)
	tx, err := c.PrepareDeployment(ctx, req, cfg)
	if err != nil {
		t.Fatalf("prepare %s: %v", image, err)
	}
	return tx
}

func deploymentIntegrationStart(t *testing.T, c *Client, ctx context.Context, name string, tx services.DeploymentTransaction) {
	t.Helper()
	if err := tx.Activate(ctx); err != nil {
		t.Fatalf("activate %s: %v", tx.Revision(), err)
	}
	out, err := c.StartContainer(ctx, name, "", nil)
	deploymentIntegrationDrain(out)
	if err != nil {
		t.Fatalf("start %s: %v", tx.Revision(), err)
	}
}

func deploymentIntegrationDrain(output <-chan services.ContainerOutput) {
	if output != nil {
		go func() {
			for range output {
			}
		}()
	}
}

func deploymentIntegrationInfo(t *testing.T, c *Client, ctx context.Context, name string) containers.Container {
	t.Helper()
	info, err := c.client.ContainerService().Get(c.withNamespace(ctx), name)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func deploymentIntegrationPID(t *testing.T, c *Client, ctx context.Context, name string) uint32 {
	t.Helper()
	ctr, err := c.client.LoadContainer(c.withNamespace(ctx), name)
	if err != nil {
		t.Fatal(err)
	}
	task, err := ctr.Task(c.withNamespace(ctx), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := readinessRunning(c.withNamespace(ctx), task); err != nil {
		t.Fatal(err)
	}
	return task.Pid()
}

func deploymentIntegrationAssertRevision(t *testing.T, c *Client, ctx context.Context, name string, want containers.Container) {
	t.Helper()
	got := deploymentIntegrationInfo(t, c, ctx, name)
	if got.SnapshotKey != want.SnapshotKey || got.Snapshotter != want.Snapshotter || got.Image != want.Image ||
		got.Labels[labelDeploymentRevision] != want.Labels[labelDeploymentRevision] || !reflect.DeepEqual(got.Spec, want.Spec) {
		t.Fatalf("restored revision identity differs: snapshot=%q image=%q revision=%q", got.SnapshotKey, got.Image, got.Labels[labelDeploymentRevision])
	}
	if err := c.ProbeReadiness(ctx, name, nil); err != nil {
		t.Fatalf("restored process is not running: %v", err)
	}
}

func deploymentIntegrationExec(t *testing.T, c *Client, ctx context.Context, name string, args ...string) string {
	t.Helper()
	command := append([]string{"/bin/busybox"}, args...)
	var stdout, stderr bytes.Buffer
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, err := c.ExecInContainer(execCtx, name, command, false, nil, &stdout, &stderr, nil)
	if err != nil || code != 0 {
		t.Fatalf("exec %v: exit=%d error=%v stderr=%s", command, code, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func deploymentIntegrationImage(t *testing.T, c *Client, ctx context.Context, busybox []byte, version string) string {
	t.Helper()
	ctx, release, err := c.client.WithLease(c.withNamespace(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release(c.withNamespace(context.Background())) }()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, dir := range []string{"bin", "dev", "etc", "proc", "root", "run", "state", "sys", "tmp"} {
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct {
		name string
		data []byte
		mode int64
	}{{"bin/busybox", busybox, 0o755}, {"image-version", []byte(version), 0o644}, {"etc/resolv.conf", nil, 0o644}, {"etc/hosts", nil, 0o644}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Size: int64(len(file.data)), Mode: file.mode}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	write := func(data []byte, mediaType string, labels map[string]string) ocispec.Descriptor {
		t.Helper()
		desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
		if err := content.WriteBlob(ctx, c.client.ContentStore(), "integration-"+desc.Digest.Encoded(), bytes.NewReader(data), desc, content.WithLabels(labels)); err != nil {
			t.Fatal(err)
		}
		return desc
	}
	layerDesc := write(layer.Bytes(), ocispec.MediaTypeImageLayer, nil)
	config, err := json.Marshal(ocispec.Image{Platform: ocispec.Platform{Architecture: runtime.GOARCH, OS: "linux"},
		Config: ocispec.ImageConfig{Cmd: []string{"/bin/busybox", "sleep", "3600"}, Env: []string{"PATH=/bin"}, WorkingDir: "/"},
		RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{layerDesc.Digest}}})
	if err != nil {
		t.Fatal(err)
	}
	configDesc := write(config, ocispec.MediaTypeImageConfig, nil)
	manifest, err := json.Marshal(ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		Config: configDesc, Layers: []ocispec.Descriptor{layerDesc}})
	if err != nil {
		t.Fatal(err)
	}
	manifestDesc := write(manifest, ocispec.MediaTypeImageManifest, map[string]string{
		"containerd.io/gc.ref.content.config": configDesc.Digest.String(),
		"containerd.io/gc.ref.content.l.0":    layerDesc.Digest.String(),
	})
	name := "docker.io/wendy-deployment-integration/" + version + ":test"
	if _, err := c.client.ImageService().Create(ctx, images.Image{Name: name, Target: manifestDesc}); err != nil {
		t.Fatal(err)
	}
	return name
}

func deploymentIntegrationCleanup(t *testing.T, c *Client, ctx context.Context, name string) {
	t.Helper()
	_ = c.StopContainer(ctx, name)
	_ = c.DeleteContainer(ctx, name, false)
	ctx = c.withNamespace(ctx)
	ctrs, err := c.client.Containers(ctx)
	if err != nil {
		t.Logf("cleanup list containers: %v", err)
		return
	}
	for _, ctr := range ctrs {
		if task, err := ctr.Task(ctx, nil); err == nil {
			_, _ = task.Delete(ctx, containerd.WithProcessKill)
		}
		if err := ctr.Delete(ctx, containerd.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
			t.Logf("cleanup retained container %s: %v", ctr.ID(), err)
		}
	}
	if imgs, err := c.client.ImageService().List(ctx); err == nil {
		for _, img := range imgs {
			_ = c.client.ImageService().Delete(ctx, img.Name)
		}
	}
}
