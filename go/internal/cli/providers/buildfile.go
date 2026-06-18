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
