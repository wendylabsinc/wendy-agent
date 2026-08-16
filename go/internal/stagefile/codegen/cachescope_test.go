package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// gen compiles one stage at a target platform and returns the Dockerfile.
func gen(t *testing.T, platform string, s spec.Stage) string {
	t.Helper()
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{s}},
		map[string]string{s.From: "sha256:abc123"}, nil, platform, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// mountIDs returns every id= value in out, in order.
func mountIDs(t *testing.T, out string) []string {
	t.Helper()
	var ids []string
	rest := out
	for {
		_, after, found := strings.Cut(rest, "id=")
		if !found {
			return ids
		}
		id, tail, _ := strings.Cut(after, ",")
		ids = append(ids, id)
		rest = tail
	}
}

func pipMountIDs(t *testing.T, out string) []string {
	t.Helper()
	var ids []string
	for _, id := range mountIDs(t, out) {
		if strings.HasPrefix(id, "stagefile-pip-") {
			ids = append(ids, id)
		}
	}
	return ids
}

func pipStage(platform string, groups ...spec.PipInstall) spec.Stage {
	return spec.Stage{Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{Pip: groups}}
}

func aptStage(from string, packages ...string) spec.Stage {
	return spec.Stage{Name: "app", From: from,
		Install: &spec.Install{Apt: &spec.AptInstall{Packages: packages}}}
}

// Explicit IDs, rather than target-path defaults, are what make the cache
// visible to separately compiled Stagefiles and concurrent buildx clients.
func TestAptCachesAreSharedAcrossCompatibleStagefiles(t *testing.T) {
	a := mountIDs(t, gen(t, "linux/arm64", aptStage("debian:12", "curl")))
	b := mountIDs(t, gen(t, "linux/arm64", aptStage("debian:12", "git")))
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("APT must mount lists and archives caches, got %v / %v", a, b)
	}
	if a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("compatible Stagefiles do not share APT caches: %v / %v", a, b)
	}
}

// APT indexes and .debs are distribution- and architecture-specific. Keeping
// those scopes apart prevents a cache hit from crossing incompatible roots.
func TestAptCachesSeparateBaseImagesAndPlatforms(t *testing.T) {
	base := mountIDs(t, gen(t, "linux/arm64", aptStage("debian:12", "curl")))
	otherImage := mountIDs(t, gen(t, "linux/arm64", aptStage("ubuntu:24.04", "curl")))
	otherPlatform := mountIDs(t, gen(t, "linux/amd64", aptStage("debian:12", "curl")))
	if base[0] == otherImage[0] || base[1] == otherImage[1] {
		t.Fatalf("different base images share APT caches: %v / %v", base, otherImage)
	}
	if base[0] == otherPlatform[0] || base[1] == otherPlatform[1] {
		t.Fatalf("different platforms share APT caches: %v / %v", base, otherPlatform)
	}
}

// pin: false intentionally avoids resolving the base image, so the compiler
// cannot safely persist package state across builds: the same tag may refer to
// a different distribution snapshot next time.
func TestAptCachesDisabledForUnpinnedBase(t *testing.T) {
	no := false
	s := aptStage("debian:12", "curl")
	s.Pin = &no
	out := gen(t, "linux/arm64", s)
	if strings.Contains(out, "type=cache") {
		t.Fatalf("unpinned base uses persistent APT caches:\n%s", out)
	}
	if !strings.Contains(out, "rm -rf /var/lib/apt/lists/*") {
		t.Fatalf("uncached APT install leaves package indexes in the image:\n%s", out)
	}
}

// Without an id BuildKit scopes a cache mount by its target path alone, so
// every pip install in every concurrently-built service queues on one
// sharing=locked mount — including installs that could not share a single
// cached wheel because they pull from different indexes.
func TestPipCacheMountIsScoped(t *testing.T) {
	out := gen(t, "linux/arm64", pipStage("linux/arm64", spec.PipInstall{Packages: []string{"flask"}}))
	ids := pipMountIDs(t, out)
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("pip mount carries no id:\n%s", out)
	}
}

// Two groups pulling from different indexes cache different wheels, so making
// them contend buys nothing. This is exactly the CUDA case: a Jetson-index
// install and a PyPI install in the same stage.
func TestPipCacheIDSeparatesIndexes(t *testing.T) {
	out := gen(t, "linux/arm64", pipStage("linux/arm64",
		spec.PipInstall{Packages: []string{"torch"}, Index: "https://pypi.jetson-ai-lab.io/jp6/cu126/"},
		spec.PipInstall{Packages: []string{"nvidia-cudnn-cu12"}},
	))
	ids := pipMountIDs(t, out)
	if len(ids) != 2 {
		t.Fatalf("want 2 pip mount ids, got %v:\n%s", ids, out)
	}
	if ids[0] == ids[1] {
		t.Fatalf("groups with different indexes share cache id %q, so they serialize for nothing", ids[0])
	}
}

// extraIndex changes which wheels can land in the cache, so it belongs in the
// scope too.
func TestPipCacheIDSeparatesExtraIndexes(t *testing.T) {
	a := pipMountIDs(t, gen(t, "linux/arm64", pipStage("linux/arm64",
		spec.PipInstall{Packages: []string{"x"}, ExtraIndex: []string{"https://a.example/simple"}})))
	b := pipMountIDs(t, gen(t, "linux/arm64", pipStage("linux/arm64",
		spec.PipInstall{Packages: []string{"x"}, ExtraIndex: []string{"https://b.example/simple"}})))
	if a[0] == b[0] {
		t.Fatalf("different extraIndex sets share cache id %q", a[0])
	}
}

// Wheels are platform-specific, so an arm64 and an amd64 build store disjoint
// content. Serializing them is pure loss.
func TestPipCacheIDSeparatesPlatforms(t *testing.T) {
	arm := pipMountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	amd := pipMountIDs(t, gen(t, "linux/amd64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	if arm[0] == amd[0] {
		t.Fatalf("arm64 and amd64 pip installs share cache id %q", arm[0])
	}
}

// Locally-built wheels may embed or link against headers and libraries from
// buildPackages. Reusing one after that toolchain changes is a cache hit with
// the wrong binary, not an optimization.
func TestPipCacheIDSeparatesBuildPackages(t *testing.T) {
	a := pipMountIDs(t, gen(t, "linux/arm64", pipStage("",
		spec.PipInstall{Packages: []string{"native"}, BuildPackages: []string{"gcc"}})))
	b := pipMountIDs(t, gen(t, "linux/arm64", pipStage("",
		spec.PipInstall{Packages: []string{"native"}, BuildPackages: []string{"clang"}})))
	if a[len(a)-1] == b[len(b)-1] {
		t.Fatalf("different buildPackages share pip cache id %q", a[len(a)-1])
	}
}

// The point of keeping sharing=locked is that builds which CAN share a wheel
// still do. Same index, same platform — different packages — must land on one
// mount, so the second build gets the first one's downloads.
func TestPipCacheIDSharesWhenContentCanBeShared(t *testing.T) {
	a := pipMountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	b := pipMountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"django"}})))
	if a[0] != b[0] {
		t.Fatalf("same index and platform must share a pip cache: %q vs %q", a[0], b[0])
	}
	if !strings.Contains(gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})), "sharing=locked") {
		t.Fatal("pip mount must stay sharing=locked; scoping narrows contention, it does not remove the lock")
	}
}

// swiftStage is a Swift build stage at the given toolchain and profile.
func swiftStage(from, profile string) spec.Stage {
	return spec.Stage{Name: "app", From: from, Workdir: "/app",
		Build: &spec.Build{Lang: "swift", Profile: profile}}
}

// genScoped is gen with a cache scope, standing in for CompileFile passing the
// project directory.
func genScoped(t *testing.T, scope, platform string, s spec.Stage) string {
	t.Helper()
	out, err := Generate(&spec.File{Version: 1, Stages: []spec.Stage{s}},
		map[string]string{s.From: "sha256:abc123"}, nil, platform, nil, WithCacheScope(scope))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// scratchID returns the id of the mount targeting the Swift build tree.
func scratchID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "target="+swiftScratchPath) {
			continue
		}
		_, after, _ := strings.Cut(line, "id=")
		id, _, _ := strings.Cut(after, ",")
		return id
	}
	t.Fatalf("no scratch mount in:\n%s", out)
	return ""
}

// The Swift build tree is the one compiler cache here holding object files
// rather than self-describing downloads, so two projects sharing it would take
// turns invalidating each other — and, being sharing=locked, queue to do it.
func TestSwiftScratchCacheIDSeparatesProjects(t *testing.T) {
	s := swiftStage("swift:6.1", "")
	a := scratchID(t, genScoped(t, "/home/dev/alpha", "linux/arm64", s))
	b := scratchID(t, genScoped(t, "/home/dev/beta", "linux/arm64", s))
	if a == b {
		t.Fatalf("two projects share one Swift build tree (id %q)", a)
	}
}

// The same project rebuilding must land on the same tree, or the cache buys
// nothing at all — this is the case the mount exists for.
func TestSwiftScratchCacheIDIsStableAcrossRebuilds(t *testing.T) {
	s := swiftStage("swift:6.1", "")
	a := scratchID(t, genScoped(t, "/home/dev/alpha", "linux/arm64", s))
	b := scratchID(t, genScoped(t, "/home/dev/alpha", "linux/arm64", s))
	if a != b {
		t.Fatalf("same project got two build trees: %q vs %q", a, b)
	}
}

// Object files are per-architecture, per-toolchain and per-profile. Mixing any
// of the three in one tree means SwiftPM discards it and rebuilds.
func TestSwiftScratchCacheIDSeparatesBuildInputs(t *testing.T) {
	const scope = "/home/dev/alpha"
	base := scratchID(t, genScoped(t, scope, "linux/arm64", swiftStage("swift:6.1", "release")))
	for _, c := range []struct {
		name string
		id   string
	}{
		{"platform", scratchID(t, genScoped(t, scope, "linux/amd64", swiftStage("swift:6.1", "release")))},
		{"toolchain", scratchID(t, genScoped(t, scope, "linux/arm64", swiftStage("swift:6.2", "release")))},
		{"profile", scratchID(t, genScoped(t, scope, "linux/arm64", swiftStage("swift:6.1", "debug")))},
	} {
		if c.id == base {
			t.Errorf("a different %s shares the build tree (id %q)", c.name, base)
		}
	}
}

// SwiftPM's shared cache is scoped the other way on purpose: it holds bare
// clones keyed by repository URL, so two projects depending on swift-nio should
// share the one clone rather than each fetching it.
func TestSwiftPMCacheIsSharedAcrossProjects(t *testing.T) {
	s := swiftStage("swift:6.1", "")
	ids := func(scope string) []string {
		return mountIDs(t, genScoped(t, scope, "linux/arm64", s))
	}
	a, b := ids("/home/dev/alpha"), ids("/home/dev/beta")
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("want a scratch mount and a SwiftPM cache mount, got %v / %v", a, b)
	}
	// mountIDs returns them in emission order: scratch first, then the cache.
	if a[1] != b[1] {
		t.Fatalf("two projects cannot share SwiftPM's download cache: %q vs %q", a[1], b[1])
	}
}

// uv resolves wheels the same way pip does and has the same platform split.
func TestUvCacheIDSeparatesPlatforms(t *testing.T) {
	s := spec.Stage{Name: "app", From: "python:3.12-slim", Install: &spec.Install{Uv: &spec.UvInstall{}}}
	arm := mountIDs(t, gen(t, "linux/arm64", s))
	amd := mountIDs(t, gen(t, "linux/amd64", s))
	if len(arm) != 1 || len(amd) != 1 {
		t.Fatalf("uv mount carries no id: %v / %v", arm, amd)
	}
	if arm[0] == amd[0] {
		t.Fatalf("arm64 and amd64 uv syncs share cache id %q", arm[0])
	}
}
