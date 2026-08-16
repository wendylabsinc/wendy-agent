package stagefile

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsSourceNameAcceptsVariantsAndRejectsArtifacts(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"build.stagefile.yaml", true},
		{"prod.stagefile.yaml", true},
		{"gpu.stagefile.yaml", true},
		{"jetson-orin.stagefile.yaml", true},
		{"api_worker.stagefile.yaml", true},

		// The lockfile sits next to its source in every project. It must never
		// read as a rival build file — that is the whole reason the variant token
		// forbids dots.
		{"build.stagefile.lock.yaml", false},
		{"prod.stagefile.lock.yaml", false},

		{"", false},
		{"stagefile.yaml", false},       // no variant token
		{".stagefile.yaml", false},      // empty variant token
		{"-prod.stagefile.yaml", false}, // must start alphanumeric
		{"build.stagefile.yml", false},  // .yml is not the convention
		{"build.stagefile.json", false}, // wrong extension entirely
		{"Dockerfile", false},           //
		{"a/b.stagefile.yaml", false},   // a path is never a source name
		{"build.stagefile.yaml.bak", false},
	}
	for _, c := range cases {
		if got := IsSourceName(c.name); got != c.want {
			t.Errorf("IsSourceName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSourceVariantAndLockName(t *testing.T) {
	cases := []struct {
		source  string
		variant string
		lock    string
	}{
		{"build.stagefile.yaml", "build", "build.stagefile.lock.yaml"},
		{"prod.stagefile.yaml", "prod", "prod.stagefile.lock.yaml"},
		{"jetson-orin.stagefile.yaml", "jetson-orin", "jetson-orin.stagefile.lock.yaml"},
	}
	for _, c := range cases {
		variant, ok := SourceVariant(c.source)
		if !ok || variant != c.variant {
			t.Errorf("SourceVariant(%q) = %q, %v; want %q, true", c.source, variant, ok, c.variant)
		}
		if got := LockName(c.source); got != c.lock {
			t.Errorf("LockName(%q) = %q, want %q", c.source, got, c.lock)
		}
		if !IsLockName(c.lock) {
			t.Errorf("IsLockName(%q) = false, want true", c.lock)
		}
	}

	if got := LockName("Dockerfile"); got != "" {
		t.Errorf("LockName(non-source) = %q, want \"\"", got)
	}
	// Two variants must not share a lockfile: each pins its own base images, and
	// a shared one would make every build re-resolve the other's pins.
	if LockName("build.stagefile.yaml") == LockName("prod.stagefile.yaml") {
		t.Error("variants share a lockfile name; each must own one")
	}
}

func TestIsLockNameRejectsSourcesAndStrays(t *testing.T) {
	for _, name := range []string{
		"build.stagefile.yaml",
		"build.stagefile.lock.yml",
		".stagefile.lock.yaml",
		"lock.yaml",
		"",
	} {
		if IsLockName(name) {
			t.Errorf("IsLockName(%q) = true, want false", name)
		}
	}
}

func TestSourceNamesEnumeratesFamilyCanonicalFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Deliberately written out of order, so a passing result proves sorting.
	for _, n := range []string{
		"prod.stagefile.yaml",
		"build.stagefile.yaml",
		"gpu.stagefile.yaml",
		"alpha.stagefile.yaml",
		// Neighbours that must NOT be listed.
		"build.stagefile.lock.yaml",
		"prod.stagefile.lock.yaml",
		"Dockerfile",
		"Dockerfile.prod",
		"notes.yaml",
	} {
		write(n)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.stagefile.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := SourceNames(dir)
	want := []string{
		"build.stagefile.yaml", // canonical leads, regardless of alphabet
		"alpha.stagefile.yaml",
		"gpu.stagefile.yaml",
		"prod.stagefile.yaml",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SourceNames = %v, want %v", got, want)
	}
}

func TestSourceNamesOnDirWithoutStagefiles(t *testing.T) {
	if got := SourceNames(t.TempDir()); got != nil {
		t.Errorf("SourceNames(empty dir) = %v, want nil", got)
	}
	if got := SourceNames(filepath.Join(t.TempDir(), "does-not-exist")); got != nil {
		t.Errorf("SourceNames(missing dir) = %v, want nil", got)
	}
}

// A variant compiles from its own source and writes its own lockfile, leaving
// the canonical source's lockfile untouched — the property that lets two
// variants share one build context safely.
func TestCompileFileWithSourceUsesVariantSourceAndLock(t *testing.T) {
	dir := t.TempDir()
	writeStage := func(name, from string) {
		t.Helper()
		src := fmt.Sprintf("version: 1\nstages:\n  - name: app\n    from: %s\n    entrypoint:\n      exec: [/app]\n", from)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeStage("build.stagefile.yaml", "debian:12")
	writeStage("prod.stagefile.yaml", "alpine:3.20")

	resolver := func(ref string) (string, error) {
		switch ref {
		case "debian:12":
			return "sha256:aaa", nil
		case "alpine:3.20":
			return "sha256:bbb", nil
		}
		return "", fmt.Errorf("unexpected ref %q", ref)
	}

	dockerfile, _, err := compileFile(dir, "prod.stagefile.yaml", "", "", "", resolver, refuseHasher(t))
	if err != nil {
		t.Fatalf("compileFile(variant): %v", err)
	}
	if want := "alpine:3.20@sha256:bbb"; !strings.Contains(dockerfile, want) {
		t.Fatalf("compiled the wrong source: %q does not contain %q", dockerfile, want)
	}

	if _, err := os.Stat(filepath.Join(dir, "prod.stagefile.lock.yaml")); err != nil {
		t.Fatalf("variant lockfile not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.stagefile.lock.yaml")); !os.IsNotExist(err) {
		t.Fatalf("compiling the variant touched the canonical lockfile (err=%v)", err)
	}
}

func TestCompileFileRejectsNonStagefileSource(t *testing.T) {
	_, _, err := CompileFile(t.TempDir(), "", WithSource("Dockerfile"))
	if err == nil {
		t.Fatal("CompileFile(WithSource(\"Dockerfile\")): error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "is not a Stagefile name") {
		t.Fatalf("error %q does not explain the naming rule", err)
	}
}

// NeedsGPUTargetFile is exported alongside CompileFile, so it validates its
// source the same way: a name outside the family never reaches a file read.
//
// Every rejected name below is backed by a real file holding a cuda: stage, so
// each case fails if the guard is removed rather than passing vacuously on a
// missing file.
func TestNeedsGPUTargetFileRejectsNonSourceNames(t *testing.T) {
	const cuda = "version: 1\nstages:\n  - name: app\n    from: ubuntu:22.04\n    pin: false\n    cuda: true\n"

	root := t.TempDir()
	dir := filepath.Join(root, "project")
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// One cuda: Stagefile at every location a rejected name could reach.
	for _, p := range []string{
		filepath.Join(dir, SourceName),                  // the legitimate read
		filepath.Join(root, SourceName),                 // reached by "../"
		filepath.Join(sub, SourceName),                  // reached by "sub/"
		filepath.Join(dir, "build.stagefile.lock.yaml"), // a lockfile, not a source
		filepath.Join(dir, "Dockerfile"),                // not the family at all
	} {
		if err := os.WriteFile(p, []byte(cuda), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !NeedsGPUTargetFile(dir, SourceName) {
		t.Fatal("canonical source declaring cuda: should need a GPU target")
	}
	for _, bad := range []string{
		"../" + SourceName,
		"sub/" + SourceName,
		"build.stagefile.lock.yaml",
		"Dockerfile",
		"",
	} {
		if NeedsGPUTargetFile(dir, bad) {
			t.Fatalf("NeedsGPUTargetFile(%q) = true, want false for a non-source name", bad)
		}
	}
}
