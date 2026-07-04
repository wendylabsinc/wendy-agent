package optimize

import (
	"path/filepath"
	"strings"
)

type archImageAnalyzer struct{}

func (archImageAnalyzer) ID() string { return "arch-image" }

const defaultDockerignore = `.git
**/.build
**/.swiftpm
node_modules
target
__pycache__
*.pyc
.venv
dist
build
`

func (a archImageAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding

	fromCount := 0
	hasStageName := false
	installsToolchain := false

	for _, inst := range t.Dockerfile.Instructions {
		switch inst.Cmd {
		case "FROM":
			fromCount++
			if strings.Contains(strings.ToUpper(" "+inst.Args+" "), " AS ") {
				hasStageName = true
			}
			if t.Arch == "arm64" {
				for _, fl := range inst.Flags {
					if fl == "--platform=linux/amd64" || fl == "--platform=linux/x86_64" {
						out = append(out, Finding{
							Analyzer: a.ID(),
							Severity: SeverityError,
							Title:    "amd64 base image on arm64 target",
							Detail:   "This FROM forces linux/amd64 but the target device is arm64. It will run under QEMU emulation (slow) or fail to start. Use an arm64/multi-arch base image.",
							Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
						})
					}
				}
			}
		case "RUN":
			if strings.Contains(inst.Args, "build-essential") ||
				(strings.Contains(inst.Args, "apt-get install") && strings.Contains(inst.Args, "-dev")) ||
				strings.Contains(inst.Args, "cargo build") ||
				strings.Contains(inst.Args, "go build") ||
				strings.Contains(inst.Args, "swift build") {
				installsToolchain = true
			}
		}
	}

	if !fileExists(filepath.Join(t.Dir, ".dockerignore")) {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    "No .dockerignore",
			Detail:   "Without a .dockerignore the whole context (including .git and build artifacts) is sent to the builder, slowing builds and bloating layers.",
			Location: nil,
			Fix: &Fix{
				Description: "create a default .dockerignore",
				Op:          FixCreateFile,
				File:        filepath.Join(t.Dir, ".dockerignore"),
				New:         defaultDockerignore,
			},
		})
	}

	if fromCount == 1 && !hasStageName && installsToolchain {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "Single-stage build ships its build toolchain",
			Detail:   "This image builds and runs in one stage, leaving compilers and -dev packages in the deployed image. A multi-stage build that copies only the final artifact into a slim runtime stage is much smaller.",
			Location: nil,
		})
	}

	return out
}
