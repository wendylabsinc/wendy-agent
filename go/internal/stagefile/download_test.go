package stagefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/lock"
)

// refuseHasher is the hasher for every test whose Stagefile declares no
// download, or declares only pinned ones. It fails the test rather than
// returning a plausible digest, so "nothing was fetched" is asserted rather
// than assumed.
func refuseHasher(t *testing.T) lock.Hasher {
	t.Helper()
	return func(url string) (string, error) {
		t.Errorf("hasher called for %q; nothing should have been fetched", url)
		return "", fmt.Errorf("unexpected fetch of %q", url)
	}
}

// fakeDigest is a well-formed sha256 built rather than spelled out, so it
// reads as the placeholder it is.
var fakeDigest = "sha256:" + strings.Repeat("ab", 32)

func writeStagefile(t *testing.T, dir, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeImageResolver(ref string) (string, error) {
	if ref == "debian:12" {
		return "sha256:abc123", nil
	}
	return "", fmt.Errorf("no fake digest for %q", ref)
}

func TestCompileFileResolvesUnpinnedDownloadIntoLockfile(t *testing.T) {
	dir := t.TempDir()
	writeStagefile(t, dir, `version: 1
stages:
  - name: app
    from: debian:12
    download:
      - url: https://example.com/model.onnx
        dest: /app/model.onnx
    entrypoint:
      exec: [/app/run]
`)

	var fetched []string
	hasher := func(url string) (string, error) {
		fetched = append(fetched, url)
		return fakeDigest, nil
	}

	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", fakeImageResolver, hasher)
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if len(fetched) != 1 || fetched[0] != "https://example.com/model.onnx" {
		t.Fatalf("expected one fetch of the url, got %+v", fetched)
	}
	want := "ADD --checksum=" + fakeDigest + " https://example.com/model.onnx /app/model.onnx"
	if !strings.Contains(dockerfile, want) {
		t.Fatalf("missing pinned ADD:\nwant line: %s\ngot:\n%s", want, dockerfile)
	}

	locked, err := lock.Load(filepath.Join(dir, "build.stagefile.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := locked.Downloads["https://example.com/model.onnx"]; got != fakeDigest {
		t.Fatalf("lockfile download pin: got %q, want %q", got, fakeDigest)
	}
}

func TestCompileFileReusesLockedDownloadPinWithoutFetching(t *testing.T) {
	dir := t.TempDir()
	writeStagefile(t, dir, `version: 1
stages:
  - name: app
    from: debian:12
    download:
      - url: https://example.com/model.onnx
        dest: /app/model.onnx
    entrypoint:
      exec: [/app/run]
`)
	// A second build must not re-fetch: refuseHasher fails the test if it
	// does, which is the property that makes the pin worth having.
	first := &lock.File{Version: 1, Images: map[string]string{"debian:12": "sha256:abc123"},
		Downloads: map[string]string{"https://example.com/model.onnx": fakeDigest}}
	if err := first.Save(filepath.Join(dir, "build.stagefile.lock.yaml")); err != nil {
		t.Fatal(err)
	}

	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", fakeImageResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if !strings.Contains(dockerfile, "--checksum="+fakeDigest) {
		t.Fatalf("expected the locked digest to be used:\n%s", dockerfile)
	}
}

func TestCompileFileDoesNotFetchAnInlinePinnedDownload(t *testing.T) {
	dir := t.TempDir()
	writeStagefile(t, dir, `version: 1
stages:
  - name: app
    from: debian:12
    download:
      - url: https://example.com/model.onnx
        sha256: `+strings.TrimPrefix(fakeDigest, "sha256:")+`
        dest: /app/model.onnx
    entrypoint:
      exec: [/app/run]
`)

	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", fakeImageResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if !strings.Contains(dockerfile, "--checksum="+fakeDigest) {
		t.Fatalf("expected the inline digest to be used:\n%s", dockerfile)
	}
	// An inline pin resolves nothing, so it must not add a lockfile entry
	// either: two records of one fact can disagree.
	locked, err := lock.Load(filepath.Join(dir, "build.stagefile.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(locked.Downloads) != 0 {
		t.Fatalf("inline-pinned download must not be locked, got %+v", locked.Downloads)
	}
}

func TestCompileFileDownloadsDoNotEnterTheBuildContextAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeStagefile(t, dir, `version: 1
stages:
  - name: app
    from: debian:12
    download:
      - url: https://example.com/model.onnx
        dest: /app/model.onnx
    copy:
      - from: local
        paths: [app.py]
    entrypoint:
      exec: [python3, app.py]
`)

	_, dockerignore, err := compileFile(dir, SourceName, "", "", "", fakeImageResolver,
		func(string) (string, error) { return fakeDigest, nil })
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if strings.Contains(dockerignore, "model.onnx") {
		t.Fatalf("a remote download must not become a context allowlist entry:\n%s", dockerignore)
	}
	if !strings.Contains(dockerignore, "!app.py") {
		t.Fatalf("local copy still belongs in the allowlist:\n%s", dockerignore)
	}
}

func TestWithProgressNamesEachFetchedURL(t *testing.T) {
	var seen []string
	var o options
	WithProgress(func(url string) { seen = append(seen, url) })(&o)
	if o.progress == nil {
		t.Fatal("WithProgress did not set a callback")
	}
	o.progress("https://example.com/model.onnx")
	if len(seen) != 1 || seen[0] != "https://example.com/model.onnx" {
		t.Fatalf("got %+v", seen)
	}
}
