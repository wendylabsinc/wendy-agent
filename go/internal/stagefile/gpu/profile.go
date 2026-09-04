// Package gpu holds the one table that turns a target GPU architecture into
// everything a CUDA build needs to know: which CUDA runtime to install, which
// wheel index serves it, and where the collected libraries live.
//
// This table exists so a Stagefile never has to state any of it. The facts in
// here are hard-won, board-specific, and change when NVIDIA ships a new
// JetPack — exactly the kind of thing an app author should not be expected to
// remember, or to notice has gone stale.
package gpu

import (
	"fmt"
	"sort"
	"strings"
)

// Profile is everything one GPU architecture implies for a CUDA build.
//
// A profile is resolved once and then recorded in build.stagefile.lock.yaml.
// A later CLI whose table has moved on does not silently rebuild an app
// against a different CUDA runtime — the pin has to be removed deliberately,
// the same stance the image digests take.
type Profile struct {
	// Arch is the vendor architecture identifier the device reports as
	// gpu_arch, e.g. "sm_87".
	Arch string `yaml:"-"`
	// Board is the human name this architecture belongs to, used only in
	// diagnostics.
	Board string `yaml:"board"`
	// CUDAVersion is the CUDA major.minor the wheels below are built for.
	// It is deliberately the *wheel's* CUDA, not the host's: the host's can
	// change under a deployed image with a JetPack upgrade, and baking it in
	// would make that image quietly wrong later.
	CUDAVersion string `yaml:"cudaVersion"`
	// Index is the pip index that serves GPU wheels for this architecture.
	Index string `yaml:"index"`
	// Runtime is the CUDA runtime package set installed alongside those
	// wheels, from PyPI rather than Index — see spec.PipInstall.CUDA for why
	// the two cannot be one invocation.
	Runtime []string `yaml:"runtime"`
	// LibDir is where the runtime's shared objects are collected and given
	// loader precedence.
	LibDir string `yaml:"libDir"`
}

// cuda12Runtime is the CUDA-12 runtime set. It is a superset of what any one
// framework needs — ONNX Runtime uses six of these — because the cost of an
// unused wheel is disk, while the cost of a missing one is a run-time load
// failure on the device, far from the build that caused it.
var cuda12Runtime = []string{
	"nvidia-cuda-runtime-cu12",
	"nvidia-cuda-nvrtc-cu12",
	"nvidia-cuda-cupti-cu12",
	"nvidia-cublas-cu12",
	"nvidia-cudnn-cu12",
	"nvidia-cufft-cu12",
	"nvidia-curand-cu12",
	"nvidia-cusolver-cu12",
	"nvidia-cusparse-cu12",
	"nvidia-cusparselt-cu12",
	"nvidia-cufile-cu12",
	"nvidia-nccl-cu12",
	"nvidia-nvtx-cu12",
	"nvidia-nvjitlink-cu12",
}

// profiles is keyed by the gpu_arch string `wendy device info` reports.
//
// Only architectures whose wheel index has actually been used to build and
// run something are listed. An unlisted architecture is an error naming what
// is known (see ProfileFor) rather than a guessed index URL, because a wrong
// index does not fail here — it fails much later, on the device, as a missing
// symbol or an absent kernel image.
var profiles = map[string]Profile{
	// Orin is sm_87. The CUDA-13 "sbsa" wheels are compiled only for Thor
	// (sm_110) and Spark (sm_121) and carry no sm_87 kernels, so they load
	// and then crash on the first GPU op with "no kernel image is available
	// for execution on the device". The CUDA-12.6 JetPack-6 wheels do carry
	// sm_87, and CUDA 12.6 runs on the JetPack-7 driver via backward
	// compatibility — which is why an Orin on WendyOS 0.17 (JetPack 7,
	// CUDA 13) still builds against CUDA 12.
	"sm_87": {
		Arch:        "sm_87",
		Board:       "NVIDIA Jetson Orin",
		CUDAVersion: "12.6",
		Index:       "https://pypi.jetson-ai-lab.io/jp6/cu126/",
		Runtime:     cuda12Runtime,
		LibDir:      "/opt/cuda12/lib",
	},
}

// ProfileFor returns the profile for arch, or an error naming every
// architecture that has one.
func ProfileFor(arch string) (Profile, error) {
	p, ok := profiles[arch]
	if !ok {
		return Profile{}, fmt.Errorf(
			"no CUDA profile for GPU architecture %q (known: %s); a stage declaring cuda: cannot be built for it — install the wheels explicitly with install.pip[].index instead",
			arch, strings.Join(KnownArches(), ", "))
	}
	return p, nil
}

// KnownArches lists every architecture with a profile, sorted, for error
// messages and flag help.
func KnownArches() []string {
	arches := make([]string, 0, len(profiles))
	for a := range profiles {
		arches = append(arches, a)
	}
	sort.Strings(arches)
	return arches
}
