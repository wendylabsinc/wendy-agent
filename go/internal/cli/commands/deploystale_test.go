package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// TestDeviceHasAllLayers covers the content check that gates every push-skip
// (WDY-1824): a skip is only authorized when the device confirms it still holds
// every recorded layer.
func TestDeviceHasAllLayers(t *testing.T) {
	const (
		a = "sha256:aaaa"
		b = "sha256:bbbb"
	)

	t.Run("all present", func(t *testing.T) {
		fake := &fastPathContainerClient{presentLayers: map[string]bool{a: true, b: true}}
		conn := &grpcclient.AgentConnection{ContainerService: fake}
		if !deviceHasAllLayers(context.Background(), conn, []string{a, b}) {
			t.Fatal("expected true when the device holds every layer")
		}
	})

	t.Run("one missing", func(t *testing.T) {
		// The device has the manifest's first layer but lost the second (the
		// "content digest not found / missing blobs" case). This is the exact
		// stale/partial-image condition WDY-1824 must never skip on.
		fake := &fastPathContainerClient{presentLayers: map[string]bool{a: true}}
		conn := &grpcclient.AgentConnection{ContainerService: fake}
		if deviceHasAllLayers(context.Background(), conn, []string{a, b}) {
			t.Fatal("expected false when the device is missing a layer")
		}
	})

	t.Run("empty diff IDs fail closed", func(t *testing.T) {
		// A fingerprint written by a push path that didn't surface diff IDs must
		// not authorize a skip — we cannot prove the device holds the content.
		fake := &fastPathContainerClient{presentLayers: map[string]bool{}}
		conn := &grpcclient.AgentConnection{ContainerService: fake}
		if deviceHasAllLayers(context.Background(), conn, nil) {
			t.Fatal("expected false when no diff IDs were recorded")
		}
	})

	t.Run("unimplemented fails closed", func(t *testing.T) {
		// An agent too old for QueryLayers must fall back to a real build+push
		// rather than blindly skip. fakeContainerClient (chunkpush_test) returns
		// Unimplemented when its queryLayersFn is unset.
		conn := &grpcclient.AgentConnection{ContainerService: &fakeContainerClient{}}
		if deviceHasAllLayers(context.Background(), conn, []string{a}) {
			t.Fatal("expected false when QueryLayers is unimplemented")
		}
	})
}

// TestTryDeployFastPath_StaleImageForcesRebuild is the WDY-1824 regression: the
// input hash matches and the container is present, but the device no longer
// holds the recorded image content. The fast path must decline (done=false) so
// the caller rebuilds and re-pushes, instead of reporting success on a stale or
// partial image.
func TestTryDeployFastPath_StaleImageForcesRebuild(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "stale-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
		layerID   = "sha256:layer0"
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, LayerDiffIDs: []string{layerID}})

	appCfg := &appconfig.AppConfig{AppID: appID}

	// Container is RUNNING and the input hash matches, but the device reports NO
	// layers present — the stale/partial-image condition.
	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, inputHash, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done {
		t.Fatal("fast path skipped despite the device missing the image content (WDY-1824)")
	}
	if fake.startCalls != 0 {
		t.Fatalf("StartContainer must not run when falling back to a full deploy, got %d calls", fake.startCalls)
	}
}

// TestTryDeployFastPath_NoRecordedLayersForcesRebuild verifies that a legacy
// fingerprint carrying no layer diff IDs never takes the skip: we cannot verify
// the device holds the content, so we fall back to a real deploy.
func TestTryDeployFastPath_NoRecordedLayersForcesRebuild(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "legacy-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash})

	appCfg := &appconfig.AppConfig{AppID: appID}
	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, inputHash, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done {
		t.Fatal("fast path skipped on a fingerprint with no recorded layers (cannot verify content)")
	}
}
