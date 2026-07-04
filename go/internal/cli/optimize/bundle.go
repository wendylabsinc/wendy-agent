package optimize

import "strings"

// BundleTarget is one target's verbatim inputs for the agent.
type BundleTarget struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Dockerfile      string  `json:"dockerfile"`
	RequirementsTxt *string `json:"requirements_txt"`
}

// BundleProject is project-level context for the agent.
type BundleProject struct {
	Dir      string `json:"dir"`
	AppID    string `json:"app_id"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

// Bundle is the --agentic output: static findings plus verbatim context.
type Bundle struct {
	Schema         int            `json:"schema"`
	Project        BundleProject  `json:"project"`
	Targets        []BundleTarget `json:"targets"`
	WendyJSON      string         `json:"wendy_json"`
	StaticFindings []Finding      `json:"static_findings"`
	Instructions   string         `json:"instructions"`
}

const bundleInstructions = "You are optimizing a Wendy edge-device app build. " +
	"The static_findings below were already detected by deterministic rules — do not just repeat them. " +
	"Using the verbatim Dockerfile, requirements.txt, and wendy.json, look for the contextual optimizations rules cannot catch: " +
	"converting to multi-stage builds, choosing the correct CUDA/PyTorch wheel for the device's JetPack/CUDA version, " +
	"swapping to a slimmer or arch-correct base image, consolidating layers, and removing build-only deps from the runtime image. " +
	"Propose concrete unified diffs against the files provided. The target architecture is given in project.arch."

// BuildBundle assembles the agentic context bundle.
func BuildBundle(dir, wendyJSON string, targets []Target, findings []Finding) Bundle {
	b := Bundle{
		Schema:         1,
		Project:        BundleProject{Dir: dir},
		WendyJSON:      wendyJSON,
		StaticFindings: findings,
		Instructions:   bundleInstructions,
	}
	if len(targets) > 0 {
		b.Project.Arch = targets[0].Arch
		if cfg := targets[0].Config; cfg != nil {
			b.Project.AppID = cfg.AppID
			b.Project.Platform = cfg.Platform
		}
	}
	for _, t := range targets {
		bt := BundleTarget{Name: t.Name, Kind: t.Kind.String()}
		if t.Dockerfile != nil {
			bt.Dockerfile = strings.Join(t.Dockerfile.Lines, "\n")
		}
		if t.Requirements != nil {
			raw := t.Requirements.Raw
			bt.RequirementsTxt = &raw
		}
		b.Targets = append(b.Targets, bt)
	}
	return b
}
