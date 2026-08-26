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
	identity := unchangedTestIdentity(inputHash)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, ContainerIdentityHash: identity.Container, LiveMetadataHash: identity.Metadata, LayerDiffIDs: []string{layerID}})

	appCfg := &appconfig.AppConfig{AppID: appID}

	// Container is RUNNING and the input hash matches, but the device reports NO
	// layers present — the stale/partial-image condition.
	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, identity, runOptions{detach: true})
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
	identity := unchangedTestIdentity(inputHash)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, ContainerIdentityHash: identity.Container, LiveMetadataHash: identity.Metadata})

	appCfg := &appconfig.AppConfig{AppID: appID}
	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, identity, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done {
		t.Fatal("fast path skipped on a fingerprint with no recorded layers (cannot verify content)")
	}
}

func TestTryDeployFastPath_LegacyFingerprintForcesRebuild(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "legacy-identity-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
	)
	// Fingerprints written before immutable/live identity separation cannot
	// prove which part of the old combined hash changed, so they fail closed.
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:    "sha256:legacy-combined",
		LayerDiffIDs: []string{layerID},
	})
	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_RUNNING,
		presentLayers: map[string]bool{layerID: true},
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, &appconfig.AppConfig{AppID: appID}, deviceKey, unchangedTestIdentity("sha256:container"), runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done || fake.startCalls != 0 || fake.metadataCalls != 0 {
		t.Fatalf("legacy fingerprint must full deploy: done=%t starts=%d metadata=%d", done, fake.startCalls, fake.metadataCalls)
	}
}

func TestTryDeployFastPath_ImmutableMismatchDoesNotReconcileMetadata(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "immutable-change-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:             "sha256:old-container",
		ContainerIdentityHash: "sha256:old-container",
		LiveMetadataHash:      "sha256:metadata",
		LayerDiffIDs:          []string{layerID},
	})
	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_RUNNING,
		presentLayers: map[string]bool{layerID: true},
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, &appconfig.AppConfig{AppID: appID}, deviceKey, unchangedTestIdentity("sha256:new-container"), runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done || fake.metadataCalls != 0 || fake.startCalls != 0 {
		t.Fatalf("immutable mismatch must full deploy: done=%t starts=%d metadata=%d", done, fake.startCalls, fake.metadataCalls)
	}
}
