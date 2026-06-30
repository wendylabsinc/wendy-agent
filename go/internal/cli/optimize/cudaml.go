package optimize

import (
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

type cudaMLAnalyzer struct{}

func (cudaMLAnalyzer) ID() string { return "cuda-ml" }

var mlPackages = map[string]bool{
	"torch":           true,
	"tensorflow":      true,
	"onnxruntime":     true,
	"onnxruntime-gpu": true,
	"tensorflow-gpu":  true,
}

func hasGPUEntitlement(cfg *appconfig.AppConfig) bool {
	if cfg == nil {
		return false
	}
	for _, e := range cfg.Entitlements {
		if e.Type == appconfig.EntitlementGPU {
			return true
		}
	}
	return false
}

func (a cudaMLAnalyzer) Analyze(t *Target) []Finding {
	var out []Finding
	gpu := hasGPUEntitlement(t.Config)

	if t.Requirements != nil {
		cpuIndex := false
		for _, u := range t.Requirements.IndexURLs {
			if strings.Contains(u, "/whl/cpu") {
				cpuIndex = true
			}
		}
		for _, p := range t.Requirements.Packages {
			if !mlPackages[p.Name] {
				continue
			}
			isCPU := p.LocalLabel == "cpu" || cpuIndex
			isGPU := p.Name == "onnxruntime-gpu" || p.Name == "tensorflow-gpu" || strings.HasPrefix(p.LocalLabel, "cu")

			if isCPU && gpu {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "GPU entitlement set but a CPU-only ML wheel is pinned",
					Detail: "wendy.json declares the gpu entitlement, but " + p.Name + " resolves to a CPU-only build. " +
						"Pin a CUDA wheel matching the device's JetPack/CUDA version to actually use the GPU.",
					Location: &Loc{File: t.Requirements.Path, Line: p.Line},
				})
			}
			if isGPU && !gpu {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "CUDA ML wheel pinned but no gpu entitlement declared",
					Detail: p.Name + " is a CUDA build, but wendy.json does not declare the gpu entitlement, " +
						"so the container will not get GPU access on the device.",
					Location: &Loc{File: t.Requirements.Path, Line: p.Line},
				})
			}
		}
	}

	if t.Dockerfile != nil && t.Arch == "arm64" {
		for _, inst := range t.Dockerfile.Instructions {
			if inst.Cmd == "FROM" && strings.Contains(inst.Args, "nvidia/cuda") {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "x86 CUDA base image on an arm64 target",
					Detail: "nvidia/cuda images are x86-first. On a Jetson (arm64) use an L4T base such as " +
						"nvcr.io/nvidia/l4t-base or an l4t-* runtime image that matches the device JetPack.",
					Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
				})
			}
		}
	}

	return out
}
