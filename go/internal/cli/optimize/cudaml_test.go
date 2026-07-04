package optimize

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func gpuCfg() *appconfig.AppConfig {
	return &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}}
}

func TestCudaCPUWheelWithGPUEntitlement(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       gpuCfg(),
		Requirements: ParseRequirements("requirements.txt", []byte("torch==2.3.0+cpu\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning, got %+v", got)
	}
	if got[0].Fix != nil {
		t.Fatalf("cuda findings are report-only")
	}
}

func TestCudaGPUWheelWithoutEntitlement(t *testing.T) {
	cfg := &appconfig.AppConfig{}
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       cfg,
		Requirements: ParseRequirements("requirements.txt", []byte("onnxruntime-gpu\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning, got %+v", got)
	}
}

func TestCudaX86BaseImageOnArm(t *testing.T) {
	tg := dockerfileTarget(t, "FROM nvidia/cuda:12.4.0-runtime-ubuntu22.04\n")
	cfg := &appconfig.AppConfig{}
	tg.Config = cfg
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning for x86 cuda base, got %+v", got)
	}
}

func TestCudaSilentWhenAligned(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       gpuCfg(),
		Requirements: ParseRequirements("requirements.txt", []byte("numpy>=1.26\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("want 0, got %+v", got)
	}
}

func TestCudaCPUIndexURLEqualsFormWithGPUEntitlement(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       gpuCfg(),
		Requirements: ParseRequirements("requirements.txt", []byte("--index-url=https://download.pytorch.org/whl/cpu\ntorch\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning for cpu-index + gpu entitlement, got %+v", got)
	}
}
