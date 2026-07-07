package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
)

func TestServiceFingerprintKey(t *testing.T) {
	a := serviceFingerprintKey("sh.wendy.app", "gpu")
	b := serviceFingerprintKey("sh.wendy.app", "vui")
	if a == b {
		t.Fatalf("distinct services share a fingerprint key: %q", a)
	}
	if want := "sh.wendy.app/svc/gpu"; a != want {
		t.Fatalf("serviceFingerprintKey = %q, want %q", a, want)
	}
}

// With WENDY_PUSH_SKIP=0 the planner must short-circuit before touching the
// device (so a nil conn is safe) and skip nothing.
func TestPlanServicePushSkipsDisabled(t *testing.T) {
	t.Setenv("WENDY_PUSH_SKIP", "0")
	services := map[string]*appconfig.ServiceConfig{
		"a": {Context: "./a"},
		"b": {Context: "./b"},
	}
	skip, hashes := planServicePushSkips(context.Background(), nil, t.TempDir(), "app", "devkey", "linux/arm64", services, nil)
	if len(skip) != 0 {
		t.Fatalf("expected no skips when disabled, got %v", skip)
	}
	if len(hashes) != 0 {
		t.Fatalf("expected no hashes computed when disabled, got %v", hashes)
	}
}

// multiSvcContainerClient reports one app group with a set of service names
// present, and answers QueryLayers from a configurable present-layer set. It
// drives planServicePushSkips end to end.
type multiSvcContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil

	appName       string
	services      []string
	presentLayers map[string]bool
}

func (f *multiSvcContainerClient) ListContainers(_ context.Context, _ *agentpb.ListContainersRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	svcs := make([]*agentpb.ServiceEntry, 0, len(f.services))
	for _, s := range f.services {
		svcs = append(svcs, &agentpb.ServiceEntry{Name: s})
	}
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{
		Container: &agentpb.AppContainer{AppName: f.appName, Services: svcs},
	}}, nil
}

func (f *multiSvcContainerClient) QueryLayers(_ context.Context, in *agentpb.QueryLayersRequest, _ ...grpc.CallOption) (*agentpb.QueryLayersResponse, error) {
	resp := &agentpb.QueryLayersResponse{}
	for _, id := range in.GetDiffIds() {
		if f.presentLayers[id] {
			resp.Present = append(resp.Present, &agentpb.PresentLayer{DiffId: id, Size: 1})
		}
	}
	return resp, nil
}

// writeServiceContext lays down a minimal buildable context (Dockerfile) under
// cwd/<rel> so computeBuildInputHash succeeds, and returns the hash the planner
// will compute for it.
func writeServiceContext(t *testing.T, cwd, rel, platform string) string {
	t.Helper()
	writeFile(t, filepath.Join(cwd, rel), "Dockerfile", "FROM scratch\n")
	h, err := computeBuildInputHash(filepath.Join(cwd, rel), "", platform, nil)
	if err != nil {
		t.Fatalf("computeBuildInputHash: %v", err)
	}
	return h
}

// TestPlanServicePushSkips_MissingLayersForcesRebuild is the multi-service side
// of WDY-1824: a service whose inputs are unchanged AND whose container is
// present must still NOT be skipped when its recorded image content is not
// confirmed on the device. Here the fingerprint carries diff IDs (the state a
// future content-verifiable multi-service push would produce) but the device no
// longer holds them.
func TestPlanServicePushSkips_MissingLayersForcesRebuild(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "grp"
		deviceKey = "devkey"
		platform  = "linux/arm64"
	)
	cwd := t.TempDir()
	hash := writeServiceContext(t, cwd, "llm", platform)

	// Fingerprint recorded a layer; matching inputs; container is present.
	saveDeployFingerprint(serviceFingerprintKey(appID, "llm"), deviceKey, deployFingerprint{InputHash: hash, LayerDiffIDs: []string{"sha256:layer0"}})

	services := map[string]*appconfig.ServiceConfig{"llm": {Context: "./llm"}}

	// Device: container present, but layer NOT present (stale/partial image).
	fake := &multiSvcContainerClient{appName: appID, services: []string{"llm"}, presentLayers: map[string]bool{}}
	conn := &grpcclient.AgentConnection{ContainerService: fake}

	skip, hashes := planServicePushSkips(context.Background(), conn, cwd, appID, deviceKey, platform, services, nil)
	if skip["llm"] {
		t.Fatal("service skipped despite the device missing its image layers (WDY-1824)")
	}
	if hashes["llm"] != hash {
		t.Fatalf("hash for llm = %q, want %q", hashes["llm"], hash)
	}
}

// TestPlanServicePushSkips_RegistryPushNeverSkips pins the current, deliberate
// behavior of the multi-service (registry-push) path: even when inputs are
// unchanged and the container is present, the planner declines the skip because
// it cannot confirm the device still holds the pushed content (WDY-1824).
//
// This is the reachable production state: runMultiServiceWithAgent saves
// fingerprints via saveDeployFingerprint(...deployFingerprint{InputHash, AppVersion})
// with NO layer diff IDs (the registry-push builder never surfaces device-verifiable
// content identity), so contentPresentForService fails closed. Restoring the
// WDY-1692 skip here needs the deferred registry-digest pre-check; when that
// lands this test should flip to assert a skip.
func TestPlanServicePushSkips_RegistryPushNeverSkips(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "grp"
		deviceKey = "devkey"
		platform  = "linux/arm64"
	)
	cwd := t.TempDir()
	hash := writeServiceContext(t, cwd, "llm", platform)
	// Save the fingerprint exactly as the multi-service deploy path does: input
	// hash + app version, no layer diff IDs.
	saveDeployFingerprint(serviceFingerprintKey(appID, "llm"), deviceKey, deployFingerprint{InputHash: hash, AppVersion: "1.0.0"})

	services := map[string]*appconfig.ServiceConfig{"llm": {Context: "./llm"}}
	// Container present; the device would even report layers present if asked —
	// but the fingerprint records none, so there is nothing to verify.
	fake := &multiSvcContainerClient{appName: appID, services: []string{"llm"}, presentLayers: map[string]bool{"sha256:layer0": true}}
	conn := &grpcclient.AgentConnection{ContainerService: fake}

	skip, hashes := planServicePushSkips(context.Background(), conn, cwd, appID, deviceKey, platform, services, nil)
	if skip["llm"] {
		t.Fatal("registry-push service was skipped despite no verifiable recorded content (WDY-1824)")
	}
	if hashes["llm"] != hash {
		t.Fatalf("hash for llm = %q, want %q", hashes["llm"], hash)
	}
}

// TestContentPresentForService_VerifiesRecordedLayers documents the content-
// verification primitive itself: should a multi-service push ever record
// device-verifiable layer diff IDs (e.g. a future chunk-diff-based push), a skip
// is authorized only when the device confirms every one of them, and declined
// when any is missing.
func TestContentPresentForService_VerifiesRecordedLayers(t *testing.T) {
	const (
		a = "sha256:aaaa"
		b = "sha256:bbbb"
	)

	present := &multiSvcContainerClient{presentLayers: map[string]bool{a: true, b: true}}
	if !contentPresentForService(context.Background(), &grpcclient.AgentConnection{ContainerService: present}, &deployFingerprint{LayerDiffIDs: []string{a, b}}) {
		t.Fatal("expected content present when the device holds every recorded layer")
	}

	missing := &multiSvcContainerClient{presentLayers: map[string]bool{a: true}}
	if contentPresentForService(context.Background(), &grpcclient.AgentConnection{ContainerService: missing}, &deployFingerprint{LayerDiffIDs: []string{a, b}}) {
		t.Fatal("expected content absent when a recorded layer is missing")
	}

	// No recorded layers (the registry-push case): fail closed.
	if contentPresentForService(context.Background(), &grpcclient.AgentConnection{ContainerService: present}, &deployFingerprint{}) {
		t.Fatal("expected content absent when the fingerprint records no layers")
	}
}
