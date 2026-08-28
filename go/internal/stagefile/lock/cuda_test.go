package lock

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
)

func TestResolveCUDARecordsTheProfileItPicked(t *testing.T) {
	f := &File{Version: 1}
	arch := gpu.KnownArches()[0]

	got, err := f.ResolveCUDA(arch)
	if err != nil {
		t.Fatalf("ResolveCUDA: %v", err)
	}
	if got.Index == "" || len(got.Runtime) == 0 || got.LibDir == "" {
		t.Fatalf("ResolveCUDA: incomplete profile %+v", got)
	}
	if f.CUDA[arch].Index != got.Index {
		t.Errorf("ResolveCUDA: profile not recorded in the lockfile (%+v)", f.CUDA)
	}
}

// A pinned profile must win over the compiler's current table. Without this,
// upgrading the CLI would silently rebuild an app against a different CUDA
// runtime with nothing in the project having changed.
func TestResolveCUDAPrefersThePinOverTheTable(t *testing.T) {
	arch := gpu.KnownArches()[0]
	f := &File{Version: 1, CUDA: map[string]gpu.Profile{
		arch: {
			Board:       "Pinned Board",
			CUDAVersion: "11.8",
			Index:       "https://pinned.example.com/",
			Runtime:     []string{"nvidia-cuda-runtime-cu11"},
			LibDir:      "/opt/cuda11/lib",
		},
	}}

	got, err := f.ResolveCUDA(arch)
	if err != nil {
		t.Fatalf("ResolveCUDA: %v", err)
	}
	if got.Index != "https://pinned.example.com/" {
		t.Errorf("ResolveCUDA: index = %q, want the pinned one", got.Index)
	}
	if got.Arch != arch {
		t.Errorf("ResolveCUDA: arch = %q, want %q", got.Arch, arch)
	}
}

// An architecture with no profile must fail by name, listing the ones that do
// have one — the alternative is a guessed wheel index whose wrongness only
// surfaces on the device.
func TestResolveCUDAUnknownArchNamesTheKnownOnes(t *testing.T) {
	f := &File{Version: 1}
	_, err := f.ResolveCUDA("sm_00")
	if err == nil {
		t.Fatal("ResolveCUDA: error = nil, want one for an unknown architecture")
	}
	for _, known := range gpu.KnownArches() {
		if !contains(err.Error(), known) {
			t.Errorf("ResolveCUDA: error %q does not mention known arch %q", err, known)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
