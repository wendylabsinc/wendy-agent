package codegen

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// testProfile is a small stand-in for a real GPU profile: the assertions are
// about what the compiler does with a profile, not about which values the
// shipped table happens to hold today.
var testProfile = gpu.Profile{
	Arch:        "sm_99",
	Board:       "Test Board",
	CUDAVersion: "12.6",
	Index:       "https://wheels.example.com/cu126/",
	Runtime:     []string{"nvidia-cuda-runtime-cu12", "nvidia-cudnn-cu12"},
	LibDir:      "/opt/cuda12/lib",
}

func generateCUDA(t *testing.T, s spec.Stage, profile *gpu.Profile) (string, error) {
	t.Helper()
	s.Name = "app"
	s.From = "ubuntu:22.04"
	return Generate(
		&spec.File{Version: 1, Stages: []spec.Stage{s}},
		map[string]string{"ubuntu:22.04": "sha256:abc123"}, nil, "", profile)
}

func mustGenerateCUDA(t *testing.T, s spec.Stage) string {
	t.Helper()
	out, err := generateCUDA(t, s, &testProfile)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

// A cuda: stage should need nothing else in the Stagefile: the index, the
// runtime set, the collection, the loader path and the user all come from the
// profile. This is the whole point of the feature, so it is asserted as one
// test rather than split across the pieces.
func TestGenerateCUDAStageNeedsNothingElseDeclared(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{
		CUDA: true,
		Install: &spec.Install{Pip: []spec.PipInstall{
			{Packages: []string{"torch==2.8.0"}, CUDA: true},
		}},
		Cmd: []string{"python3", "app.py"},
	})

	for _, want := range []string{
		`--index-url 'https://wheels.example.com/cu126/'`,
		`'nvidia-cuda-runtime-cu12' 'nvidia-cudnn-cu12'`,
		`ENV LD_LIBRARY_PATH="/opt/cuda12/lib"`,
		`mkdir -p '/opt/cuda12/lib'`,
		`import nvidia, os`,
		`find "$NVIDIA_DIR" -name '*.so*' -exec ln -sf '{}' '/opt/cuda12/lib/' ';'`,
		"ldconfig",
		"USER root",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Generate: output missing %q\n%s", want, out)
		}
	}
}

// The runtime must be its own pip invocation with no --index-url. Merged into
// the wheel group it would make the vendor index primary and PyPI an extra
// index, and pip could then resolve torch from PyPI — the wrong-architecture
// wheel the split exists to prevent.
func TestGenerateCUDARuntimeIsASeparateUnindexedInstall(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{
		CUDA: true,
		Install: &spec.Install{Pip: []spec.PipInstall{
			{Packages: []string{"torch==2.8.0"}, CUDA: true},
		}},
	})

	var runtimeLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "nvidia-cuda-runtime-cu12") {
			runtimeLine = line
		}
	}
	if runtimeLine == "" {
		t.Fatalf("Generate: no runtime install line\n%s", out)
	}
	if strings.Contains(runtimeLine, "--index-url") {
		t.Errorf("runtime install carries an index; it must resolve from PyPI:\n%s", runtimeLine)
	}
	if strings.Contains(runtimeLine, "torch") {
		t.Errorf("runtime install merged with the wheel group:\n%s", runtimeLine)
	}
	if !strings.Contains(runtimeLine, "--target '/opt/stagefile/cuda/python'") {
		t.Errorf("runtime install is not isolated under the compiler-owned prefix:\n%s", runtimeLine)
	}
	if !strings.Contains(out, "COPY --link --from=stagefile-cuda-runtime-0 /opt/stagefile/cuda/python /opt/stagefile/cuda/python") {
		t.Errorf("runtime is not promoted as an independent linked layer:\n%s", out)
	}
}

// The runtime layer holds hundreds of megabytes and changes only when the
// profile does. Emitting it after ordinary app dependencies would let a
// one-character edit to those invalidate it on every build.
func TestGenerateCUDARuntimePrecedesOrdinaryPipGroups(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{
		CUDA: true,
		Install: &spec.Install{Pip: []spec.PipInstall{
			{Packages: []string{"torch==2.8.0"}, CUDA: true},
			{Packages: []string{"flask"}},
		}},
	})

	runtime := strings.Index(out, "nvidia-cuda-runtime-cu12")
	app := strings.Index(out, "'flask'")
	if runtime < 0 || app < 0 {
		t.Fatalf("Generate: missing expected installs\n%s", out)
	}
	if runtime > app {
		t.Errorf("runtime install comes after app dependencies\n%s", out)
	}
}

// A GPU stage that installs no GPU wheels of its own still needs the runtime —
// it may reach CUDA through something apt or cmake installed.
func TestGenerateCUDARuntimeEmittedWithoutAnyCUDAPipGroup(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{
		CUDA:    true,
		Install: &spec.Install{Pip: []spec.PipInstall{{Packages: []string{"flask"}}}},
	})
	if !strings.Contains(out, "nvidia-cuda-runtime-cu12") {
		t.Errorf("Generate: GPU stage without a cuda: pip group got no runtime\n%s", out)
	}
}

// A cuda: stage with no install: block at all is the same case: the collection
// step imports the nvidia package, so a stage that got the collection but not
// the runtime would fail at build time on a Stagefile validation accepts.
func TestGenerateCUDARuntimeEmittedWithoutAnyInstallBlock(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{CUDA: true, Cmd: []string{"./app"}})
	if !strings.Contains(out, "nvidia-cuda-runtime-cu12") {
		t.Errorf("Generate: GPU stage without an install: block got no runtime\n%s", out)
	}
	if strings.Index(out, "nvidia-cuda-runtime-cu12") > strings.Index(out, "import nvidia, os") {
		t.Errorf("Generate: collection runs before the runtime it imports\n%s", out)
	}
}

// The pip cache mount is scoped by the index a group resolves from. A cuda:
// group's index comes from the profile, not the Stagefile, so it must not land
// in the same cache as the PyPI groups beside it — sharing one directory
// between a vendor index and PyPI is the mixing that scoping exists to stop.
func TestGenerateCUDAPipCacheScopedByTheResolvedIndex(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{
		CUDA: true,
		Install: &spec.Install{Pip: []spec.PipInstall{
			{Packages: []string{"torch==2.8.0"}, CUDA: true},
			{Packages: []string{"flask"}},
		}},
	})

	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "/root/.cache/pip") {
			continue
		}
		start := strings.Index(line, ",id=")
		if start < 0 {
			t.Fatalf("pip cache mount with no id:\n%s", line)
		}
		rest := line[start+len(",id="):]
		ids = append(ids, rest[:strings.Index(rest, ",")])
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 pip installs (wheels, runtime, app), got %d\n%s", len(ids), out)
	}
	// The generated runtime stage is emitted first. It and the ordinary app
	// dependencies resolve from PyPI and may share; the GPU wheels use the
	// profile's vendor index and must remain separate.
	if ids[0] != ids[2] {
		t.Errorf("two PyPI groups landed in different caches: %q vs %q", ids[0], ids[2])
	}
	if ids[0] == ids[1] {
		t.Errorf("GPU wheel group shares a cache with the PyPI runtime group (id %q)", ids[0])
	}
}

// The generated CUDA runtime stage must be a pure function of the pinned base,
// target platform, and GPU profile. User APT edits belong only to the app stage
// and therefore cannot invalidate the multi-gigabyte runtime layer.
func TestGenerateCUDARuntimeStageIgnoresUserAPTChanges(t *testing.T) {
	generate := func(packages ...string) string {
		return mustGenerateCUDA(t, spec.Stage{
			CUDA: true,
			Install: &spec.Install{
				Apt: &spec.AptInstall{Packages: packages},
				Pip: []spec.PipInstall{{Packages: []string{"torch==2.8.0"}, CUDA: true}},
			},
		})
	}
	prefix := func(out string) string {
		marker := "\nFROM ubuntu:22.04@sha256:abc123 AS app\n"
		before, _, ok := strings.Cut(out, marker)
		if !ok {
			t.Fatalf("generated output missing app-stage marker:\n%s", out)
		}
		return before
	}

	before := prefix(generate("python3-pip"))
	after := prefix(generate("python3-pip", "ros-humble-rmw-cyclonedds-cpp"))
	if before != after {
		t.Errorf("CUDA runtime stage changed after an app APT edit\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// An explicit user: is a deliberate statement and outranks the root a GPU
// stage would otherwise take.
func TestGenerateCUDAExplicitUserWins(t *testing.T) {
	out := mustGenerateCUDA(t, spec.Stage{CUDA: true, User: "1000"})
	if !strings.Contains(out, "USER 1000") {
		t.Errorf("Generate: explicit user overridden\n%s", out)
	}
}

// Without cuda:, none of this appears — the feature must not leak into
// ordinary stages.
func TestGenerateWithoutCUDAEmitsNothingExtra(t *testing.T) {
	out, err := generateCUDA(t, spec.Stage{
		Install: &spec.Install{Pip: []spec.PipInstall{{Packages: []string{"flask"}}}},
	}, &testProfile)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, unwanted := range []string{"nvidia-cuda-runtime-cu12", "LD_LIBRARY_PATH", "ldconfig", "USER root"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("Generate: non-GPU stage picked up %q\n%s", unwanted, out)
		}
	}
}

// A GPU stage compiled with no profile is a programming error in the caller,
// and must fail rather than silently produce a CPU-only image whose failure
// would only appear on the device.
func TestGenerateCUDAWithoutProfileIsAnError(t *testing.T) {
	_, err := generateCUDA(t, spec.Stage{CUDA: true}, nil)
	if err == nil {
		t.Fatal("Generate: error = nil, want one for a cuda: stage with no profile")
	}
	if !strings.Contains(err.Error(), "no GPU target") {
		t.Errorf("Generate: error = %q, want it to name the missing target", err)
	}
}
