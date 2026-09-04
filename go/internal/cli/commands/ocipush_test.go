package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestOciLayoutManifestForPlatform_PicksNewestForPlatform verifies
// ociLayoutManifestForPlatform selects the SAME manifest pickOCIDescriptor
// would (last entry matching the requested platform, skipping the
// unknown/unknown attestation entry) — it must never disagree with the
// chunk-diff read path (readOCILayoutDirLayers) about which manifest is "the
// image", or a registry-push fallback could push different content than what
// was just diffed against the device.
func TestOciLayoutManifestForPlatform_PicksNewestForPlatform(t *testing.T) {
	dir := t.TempDir()

	oldManifest, oldEntries := imageManifestBytes(t, "arm64", []byte("old layer"))
	newManifest, newEntries := imageManifestBytes(t, "arm64", []byte("new layer"))
	for name, data := range oldEntries {
		writeOCILayoutDir(t, dir, map[string][]byte{name: data})
	}
	for name, data := range newEntries {
		writeOCILayoutDir(t, dir, map[string][]byte{name: data})
	}
	writeOCILayoutDir(t, dir, map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)})

	oldDigest := "sha256:" + sha256Hex(oldManifest)
	newDigest := "sha256:" + sha256Hex(newManifest)
	oldEntry := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + oldDigest + `","size":` + strconv.Itoa(len(oldManifest)) + `,"platform":{"architecture":"arm64","os":"linux"}}`
	newEntry := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + newDigest + `","size":` + strconv.Itoa(len(newManifest)) + `,"platform":{"architecture":"arm64","os":"linux"}}`
	writeLayoutIndex(t, dir, oldEntry, newEntry)

	img, err := ociLayoutManifestForPlatform(dir, "linux/arm64")
	if err != nil {
		t.Fatalf("ociLayoutManifestForPlatform: %v", err)
	}
	gotDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}
	if gotDigest.String() != newDigest {
		t.Fatalf("got manifest %s, want the newest (last) entry %s", gotDigest.String(), newDigest)
	}
}

// TestOciLayoutManifestForPlatform_NoDeployableManifest verifies a layout
// containing only a buildx attestation manifest (platform "unknown/unknown")
// is correctly reported as having no deployable image, matching
// pickOCIDescriptor's own "never a deployable image" rule for that platform.
func TestOciLayoutManifestForPlatform_NoDeployableManifest(t *testing.T) {
	dir := t.TempDir()
	digest := writeBlob(t, dir, []byte(`{"schemaVersion":2}`))
	entry := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + digest + `","size":1,"platform":{"architecture":"unknown","os":"unknown"}}`
	writeLayoutIndex(t, dir, entry)
	writeOCILayoutDir(t, dir, map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)})

	if _, err := ociLayoutManifestForPlatform(dir, "linux/arm64"); err == nil {
		t.Fatal("want an error for a layout with only an attestation manifest, got nil")
	}
}

// TestRegistryPushWouldUseDocker pins registryPushWouldUseDocker's precedence
// against the same builder-selection order buildAndPushImageForAgent applies
// (explicit --builder wins outright; otherwise Docker unless the macOS Apple
// Container auto-attempt would run first) — this is what gates whether the
// chunk-diff deploy's OCI layout is safe to reuse for the registry-push
// fallback (it was only ever built with Docker/buildx).
func TestRegistryPushWouldUseDocker(t *testing.T) {
	if !registryPushWouldUseDocker("docker") {
		t.Error(`registryPushWouldUseDocker("docker") = false, want true`)
	}
	if registryPushWouldUseDocker("apple-container") {
		t.Error(`registryPushWouldUseDocker("apple-container") = true, want false`)
	}
	if registryPushWouldUseDocker("not-a-real-builder") {
		t.Error(`registryPushWouldUseDocker("not-a-real-builder") = true, want false (invalid builder)`)
	}
	// The empty (default) builder's outcome is host-dependent — it mirrors
	// buildAndPushImageForAgent's own precedence exactly, so assert against
	// that same helper rather than a fixed boolean.
	if got, want := registryPushWouldUseDocker(""), !shouldAutoAttemptAppleContainerBuilder(); got != want {
		t.Errorf(`registryPushWouldUseDocker("") = %v, want %v`, got, want)
	}
}

// TestPushOCILayoutToRegistry_PlaintextRoundTrip pushes a locally-built OCI
// layout directory straight to an in-memory registry via
// pushOCILayoutToRegistry (no docker/buildx invoked) and fetches the result
// back to confirm it landed as the exact same manifest — this is the
// mechanism the registry-push fallback uses to skip a second, redundant
// buildx build when the chunk-diff deploy already built this image.
func TestPushOCILayoutToRegistry_PlaintextRoundTrip(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	registryAddr := strings.TrimPrefix(srv.URL, "http://")

	dir := t.TempDir()
	layerData := []byte("wendy chunk-diff reuse test layer")
	entries := minimalOCILayoutEntries(t, layerData, "application/vnd.oci.image.layer.v1.tar", layerData)
	writeOCILayoutDir(t, dir, entries)

	img, err := ociLayoutManifestForPlatform(dir, "linux/amd64")
	if err != nil {
		t.Fatalf("ociLayoutManifestForPlatform: %v", err)
	}
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pushOCILayoutToRegistry(ctx, dir, "linux/amd64", registryAddr, "wendy/reuse-test", false); err != nil {
		t.Fatalf("pushOCILayoutToRegistry: %v", err)
	}

	ref, err := name.ParseReference(registryAddr+"/wendy/reuse-test:latest", name.Insecure)
	if err != nil {
		t.Fatalf("name.ParseReference: %v", err)
	}
	pulled, err := remote.Get(ref, remote.WithTransport(http.DefaultTransport))
	if err != nil {
		t.Fatalf("remote.Get after push: %v", err)
	}
	if pulled.Digest.String() != wantDigest.String() {
		t.Fatalf("pushed manifest digest = %s, want %s", pulled.Digest.String(), wantDigest.String())
	}
}
