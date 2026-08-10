package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFiles creates each named file in dir with placeholder content.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func buildOptionFiles(options []BuildOption) []string {
	files := make([]string, 0, len(options))
	for _, o := range options {
		files = append(files, o.File)
	}
	return files
}

// The reported bug: a project with several Dockerfiles showed every one of them
// in the build-file picker while its Stagefiles stayed invisible, because
// container build files were matched by pattern and Stagefiles by a single
// hard-coded filename.
func TestDetectBuildOptionsListsStagefileVariantsAlongsideDockerfiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"Dockerfile",
		"Dockerfile.prod",
		"Dockerfile.gpu",
		"Containerfile",
		"build.stagefile.yaml",
		"prod.stagefile.yaml",
		"gpu.stagefile.yaml",
	)

	got := buildOptionFiles(detectBuildOptions(dir))
	want := []string{
		// Stagefiles first (canonical leading its family), then container build files.
		"build.stagefile.yaml",
		"gpu.stagefile.yaml",
		"prod.stagefile.yaml",
		"Containerfile",
		"Dockerfile",
		"Dockerfile.gpu",
		"Dockerfile.prod",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("detectBuildOptions files:\ngot:  %v\nwant: %v", got, want)
	}

	for _, o := range detectBuildOptions(dir) {
		if o.Type != "docker" {
			t.Errorf("option %q has type %q, want docker", o.File, o.Type)
		}
	}
}

// Stagefile options must be labelled so the picker distinguishes them from the
// Dockerfiles listed beside them.
func TestDetectBuildOptionsLabelsEveryStagefile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "build.stagefile.yaml", "prod.stagefile.yaml", "Dockerfile")

	for _, o := range detectBuildOptions(dir) {
		wantLabelled := strings.HasSuffix(o.File, ".stagefile.yaml")
		gotLabelled := strings.HasSuffix(o.Label, " (Stagefile)")
		if gotLabelled != wantLabelled {
			t.Errorf("option %q label = %q; labelled-as-Stagefile = %v, want %v", o.File, o.Label, gotLabelled, wantLabelled)
		}
	}
}

// Build artifacts must never re-enter detection as rival user build files. Each
// Stagefile variant compiles to its own "Dockerfile.generated.<variant>", which
// matches the ordinary Dockerfile-variant pattern and so needs excluding
// explicitly — as do the lockfiles the compiler maintains.
func TestDetectBuildOptionsExcludesGeneratedArtifactsAndLockfiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"build.stagefile.yaml",
		"prod.stagefile.yaml",
		"build.stagefile.lock.yaml",
		"prod.stagefile.lock.yaml",
		generatedDockerfileName,
		generatedDockerignoreName,
		"Dockerfile.generated.prod",
		"Dockerfile.generated.prod.dockerignore",
	)

	got := buildOptionFiles(detectBuildOptions(dir))
	want := []string{"build.stagefile.yaml", "prod.stagefile.yaml"}
	if !slices.Equal(got, want) {
		t.Fatalf("detectBuildOptions files:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestDetectProjectTypeRecognisesAVariantOnlyProject(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "prod.stagefile.yaml")

	got, err := detectProjectType(dir)
	if err != nil {
		t.Fatalf("detectProjectType: %v", err)
	}
	if got != "docker" {
		t.Fatalf("detectProjectType = %q, want docker", got)
	}
	if !hasContainerBuildFile(dir) {
		t.Error("hasContainerBuildFile = false, want true")
	}
}

// --dockerfile is how CI names a build file when there is no picker, so it has
// to accept the Stagefiles the picker now lists.
func TestValidateDockerfileNameAcceptsStagefiles(t *testing.T) {
	for _, name := range []string{"build.stagefile.yaml", "prod.stagefile.yaml", "Dockerfile", "Dockerfile.prod", "Containerfile-arm"} {
		if err := validateDockerfileName(name); err != nil {
			t.Errorf("validateDockerfileName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{
		"build.stagefile.lock.yaml", // an artifact, not a build file
		"notes.yaml",
		"../Dockerfile",
		"Dockerfile.dockerignore",
	} {
		if err := validateDockerfileName(name); err == nil {
			t.Errorf("validateDockerfileName(%q) = nil, want an error", name)
		}
	}
}

// Each variant compiles to a distinct artifact. Compose routinely points several
// services at one build context, so a shared artifact name would let two
// concurrent builds overwrite each other's compiled Dockerfile.
func TestGeneratedBuildFileForIsDistinctPerVariant(t *testing.T) {
	canonicalDF, canonicalDI := generatedBuildFileFor("build.stagefile.yaml")
	if canonicalDF != generatedDockerfileName || canonicalDI != generatedDockerignoreName {
		t.Fatalf("canonical source = (%q, %q), want (%q, %q) for backwards compatibility",
			canonicalDF, canonicalDI, generatedDockerfileName, generatedDockerignoreName)
	}

	prodDF, prodDI := generatedBuildFileFor("prod.stagefile.yaml")
	gpuDF, _ := generatedBuildFileFor("gpu.stagefile.yaml")
	if prodDF == canonicalDF || prodDF == gpuDF {
		t.Fatalf("variant artifacts collide: canonical=%q prod=%q gpu=%q", canonicalDF, prodDF, gpuDF)
	}
	if prodDI != prodDF+".dockerignore" {
		t.Errorf("dockerignore %q is not paired with %q", prodDI, prodDF)
	}

	// Every generated artifact must round-trip back to the source that made it,
	// and must be excluded from build-file detection.
	for _, source := range []string{"build.stagefile.yaml", "prod.stagefile.yaml", "gpu.stagefile.yaml"} {
		df, _ := generatedBuildFileFor(source)
		if !isGeneratedBuildFileName(df) {
			t.Errorf("isGeneratedBuildFileName(%q) = false, want true", df)
		}
		if isContainerBuildFileName(df) {
			t.Errorf("isContainerBuildFileName(%q) = true; the artifact would reappear as a rival build file", df)
		}
		back, ok := stagefileSourceForGenerated(df)
		if !ok || back != source {
			t.Errorf("stagefileSourceForGenerated(%q) = %q, %v; want %q, true", df, back, ok, source)
		}
	}

	if _, ok := stagefileSourceForGenerated("Dockerfile.prod"); ok {
		t.Error("stagefileSourceForGenerated(\"Dockerfile.prod\") = ok; a user Dockerfile is not a generated artifact")
	}
}

// A project holding only variants has no conventional default, so the
// non-interactive path must not silently pretend one exists.
func TestPreferredContainerBuildFileOptionAmongVariants(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "prod.stagefile.yaml", "gpu.stagefile.yaml", "Dockerfile.prod")
	if got := preferredContainerBuildFileOption(detectBuildOptions(dir)); got != nil {
		t.Fatalf("preferredContainerBuildFileOption = %q, want nil (no conventional default among variants)", got.File)
	}

	writeFiles(t, dir, "build.stagefile.yaml")
	got := preferredContainerBuildFileOption(detectBuildOptions(dir))
	if got == nil || got.File != "build.stagefile.yaml" {
		t.Fatalf("preferredContainerBuildFileOption = %v, want build.stagefile.yaml", got)
	}
}

// End-to-end for the CI path: --dockerfile names a Stagefile variant, and what
// comes back is that variant's COMPILED Dockerfile. Returning the name verbatim
// (as the explicit branch does for a real Dockerfile) would hand the builder a
// YAML file as its -f argument.
func TestResolveDockerfileCompilesAnExplicitlyNamedStagefileVariant(t *testing.T) {
	dir := t.TempDir()
	// pin: false keeps the compile offline — no registry digest lookup.
	variant := "version: 1\nstages:\n  - name: app\n    from: alpine:3.20\n    pin: false\n    entrypoint:\n      exec: [/app]\n"
	if err := os.WriteFile(filepath.Join(dir, "prod.stagefile.yaml"), []byte(variant), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, "Dockerfile", "Dockerfile.dev")

	got, err := resolveDockerfile(dir, "prod.stagefile.yaml", false, "")
	if err != nil {
		t.Fatalf("resolveDockerfile(--dockerfile prod.stagefile.yaml): %v", err)
	}

	wantName, wantIgnore := generatedBuildFileFor("prod.stagefile.yaml")
	if got != wantName {
		t.Fatalf("resolveDockerfile = %q, want the compiled %q", got, wantName)
	}
	compiled, err := os.ReadFile(filepath.Join(dir, got))
	if err != nil {
		t.Fatalf("reading compiled build file: %v", err)
	}
	if !strings.Contains(string(compiled), "FROM alpine:3.20") {
		t.Fatalf("compiled output does not come from the named variant:\n%s", compiled)
	}
	if _, err := os.Stat(filepath.Join(dir, wantIgnore)); err != nil {
		t.Errorf("paired dockerignore %q not written: %v", wantIgnore, err)
	}
	// The canonical artifact belongs to the canonical source; compiling a variant
	// must not squat on it.
	if _, err := os.Stat(filepath.Join(dir, generatedDockerfileName)); !os.IsNotExist(err) {
		t.Errorf("compiling a variant wrote %s (err=%v)", generatedDockerfileName, err)
	}
}

// A compose service whose context holds only a variant must build that variant
// rather than fall through to a "Dockerfile" that isn't there.
func TestDefaultComposeBuildFilePrefersStagefiles(t *testing.T) {
	canonical := t.TempDir()
	writeFiles(t, canonical, "build.stagefile.yaml", "prod.stagefile.yaml", "Dockerfile")
	if got := defaultComposeBuildFile(canonical); got != "build.stagefile.yaml" {
		t.Errorf("defaultComposeBuildFile(canonical+variant) = %q, want build.stagefile.yaml", got)
	}

	variantOnly := t.TempDir()
	writeFiles(t, variantOnly, "prod.stagefile.yaml")
	if got := defaultComposeBuildFile(variantOnly); got != "prod.stagefile.yaml" {
		t.Errorf("defaultComposeBuildFile(variant only) = %q, want prod.stagefile.yaml", got)
	}

	plain := t.TempDir()
	writeFiles(t, plain, "Dockerfile")
	if got := defaultComposeBuildFile(plain); got != "Dockerfile" {
		t.Errorf("defaultComposeBuildFile(Dockerfile only) = %q, want Dockerfile", got)
	}
}
