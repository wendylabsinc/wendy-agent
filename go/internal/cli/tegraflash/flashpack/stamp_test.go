package flashpack

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

func TestStampRoundTripAndStaleness(t *testing.T) {
	dir := t.TempDir()
	extracted := filepath.Join(dir, "jetson-agx-thor-pr-1.flashpack")
	const sum = "ABCDEF0123456789"

	// Missing stamp is grandfathered as current, never stale.
	if _, ok := ReadStamp(extracted); ok {
		t.Fatal("ReadStamp reported a stamp before one was written")
	}
	if StampStale(extracted, sum) {
		t.Fatal("a missing stamp must be treated as current, not stale")
	}

	if err := WriteStamp(extracted, sum); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
	got, ok := ReadStamp(extracted)
	if !ok || got != sum {
		t.Fatalf("ReadStamp = (%q, %v), want (%q, true)", got, ok, sum)
	}

	// Whitespace and hex case must not cause a false mismatch.
	if StampStale(extracted, "  abcdef0123456789\n") {
		t.Fatal("stamp compare should trim whitespace and ignore hex case")
	}
	// A genuinely different checksum is stale.
	if !StampStale(extracted, "0000000000000000") {
		t.Fatal("a differing manifest checksum must be reported stale")
	}
}

func TestPurgeRemovesTreeTarballAndStamp(t *testing.T) {
	dir := t.TempDir()
	extracted := filepath.Join(dir, "pack.flashpack")
	tarball := extracted + ".tar.zst"
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tarball, filepath.Join(extracted, "manifest.json")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteStamp(extracted, "deadbeef"); err != nil {
		t.Fatal(err)
	}

	Purge(extracted, tarball)

	for _, p := range []string{extracted, tarball, StampPath(extracted)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("Purge left %s behind (err=%v)", p, err)
		}
	}
}

// TestResolveDropsTarballAfterExtract pins the Thor/T234 convergence: once the
// extracted tree verifies, resolvePaths deletes the .tar.zst so a version's
// footprint isn't doubled.
func TestResolveDropsTarballAfterExtract(t *testing.T) {
	cache := t.TempDir()
	const version = "0.18.0"
	tarball := TarballCachePath(cache, version)
	writeZstTar(t, tarball,
		[]tar.Header{{Name: "manifest.json", Mode: 0o644, Typeflag: tar.TypeReg}},
		[]string{`{"schema":1,"wendyos_version":"0.18.0","layout":{"stage1":"stage1","flash_workspace":"stage2/out/flash_workspace"},"files":{}}`},
	)

	if _, err := Resolve(cache, version); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := os.Stat(tarball); !os.IsNotExist(err) {
		t.Fatalf("tarball should be dropped after extraction (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(cache, FlashpackName(version), "manifest.json")); err != nil {
		t.Fatalf("extracted tree missing after Resolve: %v", err)
	}
}
