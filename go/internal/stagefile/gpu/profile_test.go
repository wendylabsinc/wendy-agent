package gpu

import (
	"slices"
	"testing"
)

func TestCUDA12RuntimeIncludesCUFile(t *testing.T) {
	if !slices.Contains(cuda12Runtime, "nvidia-cufile-cu12") {
		t.Fatal("CUDA 12 runtime is missing nvidia-cufile-cu12; PyTorch's Jetson wheel links libcufile.so.0")
	}
}
