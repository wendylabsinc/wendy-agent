package stagefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/lock"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestCompileFileGeneratesDockerfileAndDockerignore(t *testing.T) {
	dir := t.TempDir()
	source := `version: 1
stages:
  - name: app
    from: debian:12
    copy:
      - from: local
        paths: ["app.py"]
    entrypoint:
      exec: [python3, app.py]
`
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeResolver := func(ref string) (string, error) {
		if ref == "debian:12" {
			return "sha256:abc123", nil
		}
		return "", fmt.Errorf("no fake digest for %q", ref)
	}

	dockerfile, dockerignore, err := compileFile(dir, SourceName, "linux/arm64", "", "", fakeResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}

	wantDockerfile := "FROM --platform=linux/arm64 debian:12@sha256:abc123 AS app\n" +
		"COPY app.py app.py\n" +
		`ENTRYPOINT ["python3", "app.py"]` + "\n" +
		"USER 65532\n"
	if dockerfile != wantDockerfile {
		t.Fatalf("dockerfile:\ngot:\n%q\nwant:\n%q", dockerfile, wantDockerfile)
	}

	// Derive cannot know whether "app.py" is a file or a directory, so it emits
	// the directory forms too; they are inert for a plain file.
	wantDockerignore := "*\n!app.py\n!app.py/\n!app.py/**\n"
	if dockerignore != wantDockerignore {
		t.Fatalf("dockerignore:\ngot:\n%q\nwant:\n%q", dockerignore, wantDockerignore)
	}

	lockData, err := os.ReadFile(filepath.Join(dir, "build.stagefile.lock.yaml"))
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}
	if !strings.Contains(string(lockData), "sha256:abc123") {
		t.Fatalf("lockfile missing resolved digest: %s", lockData)
	}
}

func TestCompileFileReusesExistingLockPin(t *testing.T) {
	dir := t.TempDir()
	source := "version: 1\nstages:\n  - name: app\n    from: debian:12\n"
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	existingLock := &lock.File{Version: 1, SourceHash: spec.SourceHash([]byte(source)), Images: map[string]string{"debian:12": "sha256:preexisting"}}
	if err := existingLock.Save(filepath.Join(dir, "build.stagefile.lock.yaml")); err != nil {
		t.Fatal(err)
	}

	resolverCalled := false
	fakeResolver := func(ref string) (string, error) {
		resolverCalled = true
		return "sha256:shouldnothappen", nil
	}

	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", fakeResolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if resolverCalled {
		t.Fatal("resolver was called even though the ref was already pinned")
	}
	if !strings.Contains(dockerfile, "sha256:preexisting") {
		t.Fatalf("expected the pre-existing pin to be reused, got:\n%s", dockerfile)
	}
}

func TestCompileFileReturnsErrorForInvalidSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte("version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compileFile(dir, SourceName, "", "", "", func(string) (string, error) { return "", nil }, refuseHasher(t)); err == nil {
		t.Fatal("expected an error for an unsupported version, got nil")
	}
}

func TestCompileFileReturnsErrorWhenSourceMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := compileFile(dir, SourceName, "", "", "", func(string) (string, error) { return "", nil }, refuseHasher(t)); err == nil {
		t.Fatal("expected an error when build.stagefile.yaml is missing, got nil")
	}
}

// TestCompileFileResolvesASharedBaseImageOncePerProcess covers the compose
// case: every service in a project typically shares one base image, each
// service compiles its own Stagefile, and those compiles now run concurrently —
// so without a process-wide memo they all issue the same registry lookup at the
// same time. Two unpinned projects on one base must cost one lookup.
//
// The ref is unique to this test because the memo deliberately outlives a
// single compile; a shared ref would let another test's lookup satisfy this one
// and the assertion would pass for the wrong reason.
func TestCompileFileResolvesASharedBaseImageOncePerProcess(t *testing.T) {
	const ref = "example.invalid/shared-base:once"

	var calls atomic.Int32
	restore := baseResolver
	baseResolver = func(got string) (string, error) {
		if got != ref {
			return "", fmt.Errorf("unexpected ref %q", got)
		}
		calls.Add(1)
		return "sha256:shared", nil
	}
	t.Cleanup(func() { baseResolver = restore })

	source := "version: 1\nstages:\n  - name: app\n    from: " + ref + "\n"
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, dir := range dirs {
		if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, dir := range dirs {
		dockerfile, _, err := CompileFile(dir, "")
		if err != nil {
			t.Fatalf("CompileFile(%s): %v", dir, err)
		}
		if !strings.Contains(dockerfile, "sha256:shared") {
			t.Fatalf("CompileFile(%s) did not pin the resolved digest:\n%s", dir, dockerfile)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("registry resolver called %d times for one shared base image across %d projects, want 1", got, len(dirs))
	}
}

func TestCompileFileSkipsResolutionForUnpinnedStage(t *testing.T) {
	dir := t.TempDir()
	source := "version: 1\nstages:\n  - name: app\n    from: mlx-server:0.1\n    pin: false\n"
	if err := os.WriteFile(filepath.Join(dir, "build.stagefile.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := func(ref string) (string, error) {
		t.Fatalf("resolver must not be called for an unpinned ref, got %q", ref)
		return "", nil
	}
	dockerfile, _, err := compileFile(dir, SourceName, "", "", "", resolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if !strings.Contains(dockerfile, "FROM mlx-server:0.1 AS app") {
		t.Fatalf("unpinned FROM must have no digest:\n%s", dockerfile)
	}
}

func TestCompileFileDoesNotResolvePriorStageAsImage(t *testing.T) {
	dir := t.TempDir()
	source := `version: 1
stages:
  - name: native
    from: python:3.11-slim
  - name: app
    from: native
    install:
      pip:
        - packages: [cyclonedds==0.10.2]
`
	if err := os.WriteFile(filepath.Join(dir, SourceName), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var refs []string
	resolver := func(ref string) (string, error) {
		refs = append(refs, ref)
		return "sha256:abc123", nil
	}
	dockerfile, _, err := compileFile(dir, SourceName, "linux/arm64", "", "", resolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile: %v", err)
	}
	if len(refs) != 1 || refs[0] != "python:3.11-slim" {
		t.Fatalf("resolved refs = %v, want only the external base image", refs)
	}
	if !strings.Contains(dockerfile, "FROM --platform=linux/arm64 native AS stagefile-pip-deps-1") {
		t.Fatalf("pip overlay does not inherit the prior stage:\n%s", dockerfile)
	}
}
