package core

import (
	"runtime/debug"
	"strings"
)

// buildVersion is stamped by scripts/build.py for packaged engines.
var buildVersion string

// Version returns the effective Wendy Core version without a leading v.
// Packaged engines report their build identity. Go module consumers report the
// dependency version recorded by the Go toolchain, including pseudo-versions.
// Unversioned source builds return "0.1.0-dev".
func Version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		return moduleVersion(info)
	}
	return "0.1.0-dev"
}

func moduleVersion(info *debug.BuildInfo) string {
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/wendylabsinc/wendy-core" {
			continue
		}
		// A replacement's bytes identify the running engine. Local replacements
		// have no module version and must not claim the replaced release.
		if dependency.Replace != nil {
			dependency = dependency.Replace
		}
		if dependency.Version != "" && dependency.Version != "(devel)" {
			return strings.TrimPrefix(dependency.Version, "v")
		}
	}
	return "0.1.0-dev"
}
