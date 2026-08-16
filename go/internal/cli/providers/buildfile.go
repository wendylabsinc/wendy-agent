package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var providerBuildFileNameRe = regexp.MustCompile(`^(Dockerfile|Containerfile)([.\-][a-zA-Z0-9][a-zA-Z0-9._-]*)?$`)

func isContainerBuildFileName(name string) bool {
	if strings.HasSuffix(name, ".dockerignore") {
		return false
	}
	// Internal artifacts the CLI's prepareDockerBuildFile writes (compiled
	// Stagefiles or an auto-fixed Dockerfile copy) and never deletes — they must
	// not be picked up as user build files (mirrors the commands package).
	// Prefix, not equality: each Stagefile variant compiles to its own
	// "Dockerfile.generated.<variant>", which the regex below would otherwise
	// accept as an ordinary Dockerfile variant.
	if name == "Dockerfile.generated" || strings.HasPrefix(name, "Dockerfile.generated.") {
		return false
	}
	return providerBuildFileNameRe.MatchString(name)
}

func validateContainerBuildFileName(name string) error {
	cleaned := filepath.Clean(name)
	if cleaned != filepath.Base(cleaned) {
		return fmt.Errorf("invalid container build file name %q: path separators are not allowed", name)
	}
	if strings.HasSuffix(cleaned, ".dockerignore") {
		return fmt.Errorf("invalid container build file name %q: .dockerignore files are not build files", cleaned)
	}
	if !providerBuildFileNameRe.MatchString(cleaned) {
		return fmt.Errorf("invalid container build file name %q: must be Dockerfile, Containerfile, or a dot/hyphen variant of either", cleaned)
	}
	return nil
}

func hasContainerBuildFile(projectPath string) bool {
	return defaultContainerBuildFile(projectPath) != ""
}

func defaultContainerBuildFile(projectPath string) string {
	entries, err := os.ReadDir(projectPath)
	if err == nil {
		var firstVariant string
		for _, preferred := range []string{"Dockerfile", "Containerfile"} {
			for _, e := range entries {
				if !e.IsDir() && e.Name() == preferred {
					return preferred
				}
			}
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if isContainerBuildFileName(name) {
				if firstVariant == "" {
					firstVariant = name
				}
			}
		}
		return firstVariant
	}
	for _, preferred := range []string{"Dockerfile", "Containerfile"} {
		if _, statErr := os.Stat(filepath.Join(projectPath, preferred)); statErr == nil {
			return preferred
		}
	}
	return ""
}
