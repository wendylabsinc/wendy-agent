package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestGenerateCMakeInstallPinnedDeterministicAndPipIsIndependent(t *testing.T) {
	commit := strings.Repeat("a", 40)
	out := genOne(t, spec.Stage{
		Name: "app", From: "python:3.11-slim",
		Install: &spec.Install{
			Apt: &spec.AptInstall{Packages: []string{"cmake", "git"}},
			CMake: []spec.CMakeInstall{{
				Repository: "https://github.com/eclipse-cyclonedds/cyclonedds.git",
				Commit:     commit,
				Prefix:     "/opt/native libs",
				Defines: map[string]string{
					"BUILD_TESTING":  "OFF",
					"BUILD_EXAMPLES": "OFF",
				},
				Jobs: 2,
			}},
			Pip: []spec.PipInstall{{Packages: []string{"cyclonedds==0.10.2"}}},
		},
	}, nil)

	wants := []string{
		"git init '/tmp/stagefile-cmake-0/source'",
		"remote add origin 'https://github.com/eclipse-cyclonedds/cyclonedds.git'",
		"fetch --depth 1 origin '" + commit + "'",
		"checkout --detach FETCH_HEAD",
		"'-DCMAKE_BUILD_TYPE=Release' '-DCMAKE_INSTALL_PREFIX=/opt/native libs' '-DBUILD_EXAMPLES=OFF' '-DBUILD_TESTING=OFF'",
		"cmake --build '/tmp/stagefile-cmake-0/build' --parallel 2",
		"cmake --install '/tmp/stagefile-cmake-0/build'",
		"rm -rf '/tmp/stagefile-cmake-0/source'",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	appAt := strings.Index(out, " AS app\n")
	if appAt < 0 {
		t.Fatalf("missing app stage:\n%s", out)
	}
	aptAt := strings.Index(out[appAt:], "apt-get install")
	if aptAt >= 0 {
		aptAt += appAt
	}
	cmakeAt := strings.Index(out, "git init '/tmp/stagefile-cmake-0/source'")
	pipAt := strings.Index(out, "pip install")
	overlayAt := strings.Index(out, "COPY --link --from=stagefile-pip-deps-0")
	if !(pipAt >= 0 && pipAt < appAt && aptAt >= appAt && aptAt < cmakeAt && overlayAt > cmakeAt) {
		t.Fatalf("pip must branch from the base while APT/CMake finish before its linked overlay:\n%s", out)
	}
}

func TestGeneratePipOverlayCanBuildFromPriorNativeStage(t *testing.T) {
	commit := strings.Repeat("a", 40)
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{
			Name: "native", From: "python:3.11-slim",
			Install: &spec.Install{CMake: []spec.CMakeInstall{{
				Repository: "https://github.com/eclipse-cyclonedds/cyclonedds.git",
				Commit:     commit,
			}}},
		},
		{
			Name: "builder", From: "native",
			Install: &spec.Install{Pip: []spec.PipInstall{{
				Packages: []string{"cyclonedds==0.10.2"},
			}}},
		},
	}}
	out, err := Generate(f, map[string]string{"python:3.11-slim": "sha256:abc123"}, nil, "linux/arm64", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"FROM --platform=linux/arm64 python:3.11-slim@sha256:abc123 AS native",
		"FROM --platform=linux/arm64 native AS stagefile-pip-deps-1",
		"FROM --platform=linux/arm64 native AS builder",
		"COPY --link --from=stagefile-pip-deps-1 /opt/stagefile/pip/root/ /",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "native@sha256:") {
		t.Fatalf("prior stage reference was treated as an external image:\n%s", out)
	}
}

// A cmake install is the most expensive step the DSL can express, and it was
// the only one emitting a bare RUN. The build tree has to survive the layer so
// bumping the pinned commit recompiles the delta instead of the project.
func TestGenerateCMakeInstallCachesTheBuildTree(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Install: &spec.Install{CMake: []spec.CMakeInstall{{
			Repository: "https://github.com/opencv/opencv.git",
			Commit:     strings.Repeat("a", 40),
		}}},
	}, nil)

	if !strings.Contains(out, "sharing=locked") ||
		!strings.Contains(out, "target=/tmp/stagefile-cmake-0/build") {
		t.Fatalf("cmake build tree is not on a locked cache mount:\n%s", out)
	}
	// The cleanup must spare the mount: rm -rf over the whole root would empty
	// the cache it just populated.
	if strings.Contains(out, "rm -rf '/tmp/stagefile-cmake-0'") {
		t.Fatalf("cleanup still deletes the cached build tree:\n%s", out)
	}
	if !strings.Contains(out, "rm -rf '/tmp/stagefile-cmake-0/source'") {
		t.Fatalf("cleanup no longer removes the source tree:\n%s", out)
	}
}

// The mount id must separate build trees that cannot share object files.
// BuildKit scopes a cache mount by id alone, so without this an arm64 build
// and an amd64 build of the same project would hand each other object files
// for the wrong architecture — which cmake cannot detect and will happily link.
func TestGenerateCMakeCacheIDSeparatesPlatformsAndProjects(t *testing.T) {
	gen := func(repo, platform string) string {
		t.Helper()
		out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{{
			Name: "app", From: "debian:12",
			Install: &spec.Install{CMake: []spec.CMakeInstall{{
				Repository: repo, Commit: strings.Repeat("a", 40),
			}}},
		}}}, map[string]string{"debian:12": "sha256:abc123"}, nil, platform, nil)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		return cacheMountID(t, out)
	}

	arm := gen("https://github.com/opencv/opencv.git", "linux/arm64")
	amd := gen("https://github.com/opencv/opencv.git", "linux/amd64")
	other := gen("https://github.com/other/project.git", "linux/arm64")

	if arm == "" {
		t.Fatal("no cache mount id emitted")
	}
	if arm == amd {
		t.Fatalf("same cache id %q across platforms: object files would cross architectures", arm)
	}
	if arm == other {
		t.Fatalf("same cache id %q across projects: unrelated build trees would collide", arm)
	}
	if again := gen("https://github.com/opencv/opencv.git", "linux/arm64"); again != arm {
		t.Fatalf("cache id is not stable: %q then %q", arm, again)
	}
	// A bumped commit must land in the SAME tree — that is the whole point.
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{{
		Name: "app", From: "debian:12",
		Install: &spec.Install{CMake: []spec.CMakeInstall{{
			Repository: "https://github.com/opencv/opencv.git",
			Commit:     strings.Repeat("b", 40),
		}}},
	}}}, map[string]string{"debian:12": "sha256:abc123"}, nil, "linux/arm64", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := cacheMountID(t, out); got != arm {
		t.Fatalf("bumping the commit changed the cache id (%q -> %q), losing the incremental rebuild", arm, got)
	}
}

// cacheMountID pulls the id= value out of the first cache mount in out.
func cacheMountID(t *testing.T, out string) string {
	t.Helper()
	_, after, found := strings.Cut(out, "id=")
	if !found {
		return ""
	}
	id, _, _ := strings.Cut(after, ",")
	return id
}

func TestGenerateCMakeInstallQuotesUserControlledValues(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "debian:12",
		Install: &spec.Install{CMake: []spec.CMakeInstall{{
			Repository: "https://example.com/project.git?x='safe'",
			Commit:     strings.Repeat("b", 40),
			Defines:    map[string]string{"LABEL": "a; touch /pwned"},
		}}},
	}, nil)
	for _, want := range []string{
		`'https://example.com/project.git?x='"'"'safe'"'"''`,
		`'-DLABEL=a; touch /pwned'`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing safely quoted %q in:\n%s", want, out)
		}
	}
}
