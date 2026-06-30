package optimize

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// TargetKind is the kind of buildable unit being analyzed.
type TargetKind int

const (
	KindDockerfile TargetKind = iota
	KindComposeService
	KindNativeSwift
	KindNativeBrew
)

func (k TargetKind) String() string {
	switch k {
	case KindDockerfile:
		return "dockerfile"
	case KindComposeService:
		return "compose-service"
	case KindNativeSwift:
		return "native-swift"
	case KindNativeBrew:
		return "native-brew"
	default:
		return "unknown"
	}
}

// Target is one buildable unit to analyze.
type Target struct {
	Name         string
	Kind         TargetKind
	Dir          string
	Dockerfile   *Dockerfile
	Requirements *Requirements
	Config       *appconfig.AppConfig
	Arch         string
}

// resolveArch returns the override if set, else the offline default "arm64".
func resolveArch(override string) string {
	if override != "" {
		return override
	}
	return "arm64"
}

// isWithinDir reports whether target is base itself or a descendant of it,
// guarding against path-traversal (e.g. a "../../etc" build context).
func isWithinDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// loadDockerfile parses Dockerfile or Containerfile in dir, returning nil if neither exists.
func loadDockerfile(dir string) *Dockerfile {
	for _, name := range []string{"Dockerfile", "Containerfile"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			data, err := os.ReadFile(p)
			if err == nil {
				return ParseDockerfile(p, data)
			}
		}
	}
	return nil
}

// loadRequirements parses requirements.txt in dir, returning nil if absent.
func loadRequirements(dir string) *Requirements {
	p := filepath.Join(dir, "requirements.txt")
	if fileExists(p) {
		data, err := os.ReadFile(p)
		if err == nil {
			return ParseRequirements(p, data)
		}
	}
	return nil
}

// DiscoverTargets decides what to analyze in dir.
// arch is the already-resolved arch string.
// It never errors on a missing Dockerfile; errors only when dir is unstat-able.
func DiscoverTargets(dir string, cfg *appconfig.AppConfig, arch string) ([]Target, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	// Multi-service / compose: one target per service, sorted by name.
	if cfg != nil && len(cfg.Services) > 0 {
		names := make([]string, 0, len(cfg.Services))
		for name := range cfg.Services {
			names = append(names, name)
		}
		sort.Strings(names)

		targets := make([]Target, 0, len(names))
		for _, name := range names {
			svcDir := dir
			if svc := cfg.Services[name]; svc != nil && svc.Context != "" {
				candidate := filepath.Join(dir, svc.Context)
				// Refuse to read a build context that escapes the project tree.
				// A malicious wendy.json could otherwise point Context at
				// "../../etc" and have us read (and, via --agentic, surface)
				// arbitrary files. Skip such services entirely.
				if !isWithinDir(dir, candidate) {
					continue
				}
				svcDir = candidate
			}
			targets = append(targets, Target{
				Name:         name,
				Kind:         KindComposeService,
				Dir:          svcDir,
				Dockerfile:   loadDockerfile(svcDir),
				Requirements: loadRequirements(svcDir),
				Config:       cfg,
				Arch:         arch,
			})
		}
		return targets, nil
	}

	// Single Dockerfile or Containerfile.
	if df := loadDockerfile(dir); df != nil {
		return []Target{{
			Name:         "app",
			Kind:         KindDockerfile,
			Dir:          dir,
			Dockerfile:   df,
			Requirements: loadRequirements(dir),
			Config:       cfg,
			Arch:         arch,
		}}, nil
	}

	// Native Swift.
	if fileExists(filepath.Join(dir, "Package.swift")) {
		return []Target{{
			Name:         "app",
			Kind:         KindNativeSwift,
			Dir:          dir,
			Requirements: loadRequirements(dir),
			Config:       cfg,
			Arch:         arch,
		}}, nil
	}

	// Native Brew.
	if fileExists(filepath.Join(dir, "Brewfile")) {
		return []Target{{
			Name:         "app",
			Kind:         KindNativeBrew,
			Dir:          dir,
			Requirements: loadRequirements(dir),
			Config:       cfg,
			Arch:         arch,
		}}, nil
	}

	return []Target{}, nil
}
