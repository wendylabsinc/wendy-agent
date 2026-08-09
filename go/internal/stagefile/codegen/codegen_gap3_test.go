package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// The CUDA-on-Jetson examples install torch from a Jetson-specific index and
// the matching nvidia-* runtime from PyPI. Folding both into one pip
// invocation is not equivalent: with the Jetson index primary and PyPI extra,
// pip resolves the highest version across both and can pull a PyPI torch —
// which is the exact failure the split exists to avoid.
func TestGeneratePipGroupsKeepTheirOwnIndexes(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "ubuntu:22.04",
		Install: &spec.Install{Pip: []spec.PipInstall{
			{Packages: []string{"torch==2.8.0"}, Index: "https://pypi.jetson-ai-lab.io/jp6/cu126/"},
			{Packages: []string{"nvidia-cudnn-cu12"}},
		}},
	}, nil)

	jetson := strings.Index(out, "--index-url 'https://pypi.jetson-ai-lab.io/jp6/cu126/' 'torch==2.8.0'")
	plain := strings.Index(out, "pip install 'nvidia-cudnn-cu12'")
	if jetson < 0 || plain < 0 {
		t.Fatalf("pip groups not emitted independently:\n%s", out)
	}
	if jetson > plain {
		t.Fatalf("pip groups emitted out of declaration order:\n%s", out)
	}
	if strings.Contains(out, "--index-url 'https://pypi.jetson-ai-lab.io/jp6/cu126/' 'nvidia-cudnn-cu12'") {
		t.Fatalf("second group inherited the first group's index:\n%s", out)
	}
}

// The remaining half of DSL gap #3: collecting shared objects out of an
// installed dependency tree and putting that directory first on the loader
// path. Expressed as a typed op, so the no-raw-shell boundary holds.
func TestGenerateSharedLibrariesCollectsAndRegisters(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "ubuntu:22.04",
		Install: &spec.Install{Pip: []spec.PipInstall{{Packages: []string{"nvidia-cudnn-cu12"}}}},
		SharedLibraries: []spec.SharedLibrary{{
			Dir:     "/opt/cuda12/lib",
			Collect: []string{"/usr/local/lib/python3.10/dist-packages/nvidia"},
		}},
	}, nil)

	for _, want := range []string{
		"mkdir -p '/opt/cuda12/lib'",
		"find '/usr/local/lib/python3.10/dist-packages/nvidia' -name '*.so*' -exec ln -sf '{}' '/opt/cuda12/lib/' ';'",
		"> '/etc/ld.so.conf.d/000-stagefile.conf'",
		"ldconfig",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	// It must run after the installs that produce the libraries, and before
	// app source is copied — otherwise editing app.py reruns the collection.
	pipAt := strings.Index(out, "pip install")
	collectAt := strings.Index(out, "mkdir -p '/opt/cuda12/lib'")
	if pipAt < 0 || collectAt < 0 || pipAt > collectAt {
		t.Fatalf("sharedLibraries must be emitted after install:\n%s", out)
	}
}

// Two collected directories must land in a deterministic loader precedence,
// declaration order first, or the resulting image depends on map iteration.
func TestGenerateSharedLibrariesPrecedenceFollowsDeclarationOrder(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "ubuntu:22.04",
		SharedLibraries: []spec.SharedLibrary{
			{Dir: "/opt/first/lib", Collect: []string{"/src/a"}},
			{Dir: "/opt/second/lib", Collect: []string{"/src/b"}},
		},
	}, nil)

	first := strings.Index(out, "'/etc/ld.so.conf.d/000-stagefile.conf'")
	second := strings.Index(out, "'/etc/ld.so.conf.d/001-stagefile.conf'")
	if first < 0 || second < 0 {
		t.Fatalf("expected indexed ld.so.conf.d entries:\n%s", out)
	}
	if first > second {
		t.Fatalf("ld.so.conf.d precedence does not follow declaration order:\n%s", out)
	}
}

func TestGenerateSharedLibrariesQuotesUserControlledPaths(t *testing.T) {
	out := genOne(t, spec.Stage{
		Name: "app", From: "ubuntu:22.04",
		SharedLibraries: []spec.SharedLibrary{{
			Dir:     "/opt/a'; touch /pwned; '",
			Collect: []string{"/src/x'; rm -rf /; '"},
		}},
	}, nil)
	for _, want := range []string{
		`'/opt/a'"'"'; touch /pwned; '"'"''`,
		`'/src/x'"'"'; rm -rf /; '"'"''`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing safely quoted %q in:\n%s", want, out)
		}
	}
}
