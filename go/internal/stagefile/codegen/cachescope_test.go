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

func pipStage(platform string, groups ...spec.PipInstall) spec.Stage {
	return spec.Stage{Name: "app", From: "python:3.12-slim",
		Install: &spec.Install{Pip: groups}}
}

// Without an id BuildKit scopes a cache mount by its target path alone, so
// every pip install in every concurrently-built service queues on one
// sharing=locked mount — including installs that could not share a single
// cached wheel because they pull from different indexes.
func TestPipCacheMountIsScoped(t *testing.T) {
	out := gen(t, "linux/arm64", pipStage("linux/arm64", spec.PipInstall{Packages: []string{"flask"}}))
	ids := mountIDs(t, out)
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
	ids := mountIDs(t, out)
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
	a := mountIDs(t, gen(t, "linux/arm64", pipStage("linux/arm64",
		spec.PipInstall{Packages: []string{"x"}, ExtraIndex: []string{"https://a.example/simple"}})))
	b := mountIDs(t, gen(t, "linux/arm64", pipStage("linux/arm64",
		spec.PipInstall{Packages: []string{"x"}, ExtraIndex: []string{"https://b.example/simple"}})))
	if a[0] == b[0] {
		t.Fatalf("different extraIndex sets share cache id %q", a[0])
	}
}

// Wheels are platform-specific, so an arm64 and an amd64 build store disjoint
// content. Serializing them is pure loss.
func TestPipCacheIDSeparatesPlatforms(t *testing.T) {
	arm := mountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	amd := mountIDs(t, gen(t, "linux/amd64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	if arm[0] == amd[0] {
		t.Fatalf("arm64 and amd64 pip installs share cache id %q", arm[0])
	}
}

// The point of keeping sharing=locked is that builds which CAN share a wheel
// still do. Same index, same platform — different packages — must land on one
// mount, so the second build gets the first one's downloads.
func TestPipCacheIDSharesWhenContentCanBeShared(t *testing.T) {
	a := mountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})))
	b := mountIDs(t, gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"django"}})))
	if a[0] != b[0] {
		t.Fatalf("same index and platform must share a pip cache: %q vs %q", a[0], b[0])
	}
	if !strings.Contains(gen(t, "linux/arm64", pipStage("", spec.PipInstall{Packages: []string{"flask"}})), "sharing=locked") {
		t.Fatal("pip mount must stay sharing=locked; scoping narrows contention, it does not remove the lock")
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
