package board

import (
	"os"
	"path/filepath"
	"testing"
)

// installPaths redirects the package-level detection paths into a tempdir
// and creates any of the fixture files listed in `files`. Missing keys are
// left absent.
func installPaths(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	origTegra := tegraReleasePath
	origSoC := socFamilyPath
	origModel := deviceTreeModelPath
	t.Cleanup(func() {
		tegraReleasePath = origTegra
		socFamilyPath = origSoC
		deviceTreeModelPath = origModel
		resetForTest()
	})
	resetForTest()
	tegraReleasePath = filepath.Join(dir, "nv_tegra_release")
	socFamilyPath = filepath.Join(dir, "soc_family")
	deviceTreeModelPath = filepath.Join(dir, "model")
	pathMap := map[string]string{
		"tegra": tegraReleasePath,
		"soc":   socFamilyPath,
		"model": deviceTreeModelPath,
	}
	for key, content := range files {
		path := pathMap[key]
		if path == "" {
			t.Fatalf("unknown fixture key %q", key)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestDetect_Jetson_TegraRelease(t *testing.T) {
	installPaths(t, map[string]string{
		"tegra": "# R36 (release), REVISION: 4.0\n",
		"model": "NVIDIA Jetson Orin Nano Developer Kit\x00",
	})
	got := Detect()
	if !got.IsJetson() {
		t.Fatalf("expected Jetson, got %+v", got)
	}
	if got.Model != "NVIDIA Jetson Orin Nano Developer Kit" {
		t.Errorf("model: %q", got.Model)
	}
}

func TestDetect_Jetson_SoCFamily(t *testing.T) {
	installPaths(t, map[string]string{
		"soc": "tegra\n",
	})
	got := Detect()
	if !got.IsJetson() {
		t.Fatalf("expected Jetson, got %+v", got)
	}
}

func TestDetect_RaspberryPi(t *testing.T) {
	installPaths(t, map[string]string{
		"model": "Raspberry Pi 5 Model B Rev 1.0\x00",
	})
	got := Detect()
	if !got.IsRaspberryPi() {
		t.Fatalf("expected RaspberryPi, got %+v", got)
	}
	if got.Model != "Raspberry Pi 5 Model B Rev 1.0" {
		t.Errorf("model: %q", got.Model)
	}
}

func TestDetect_Generic(t *testing.T) {
	installPaths(t, nil)
	got := Detect()
	if got.Kind != Generic {
		t.Fatalf("expected Generic, got %+v", got)
	}
	if got.IsJetson() || got.IsRaspberryPi() {
		t.Errorf("predicates wrong: %+v", got)
	}
}

func TestDetect_CachingAcrossCalls(t *testing.T) {
	installPaths(t, map[string]string{"soc": "tegra\n"})
	first := Detect()
	// Mutate the underlying file after the first call.
	if err := os.WriteFile(socFamilyPath, []byte("not-tegra"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	second := Detect()
	if first.Kind != second.Kind {
		t.Errorf("expected cached result, got first=%v second=%v", first.Kind, second.Kind)
	}
}
