package commands

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// errBuilderWithBuildHost is returned when --builder and --build-host are both
// given. --builder selects the LOCAL image builder, which the remote path never
// runs, so honouring either one silently means ignoring the other.
var errBuilderWithBuildHost = errors.New("--builder selects a local image builder and cannot be combined with --build-host; drop one")

// loadBuildHostDefault is the seam tests replace, in the style docker.go's
// imageBuilderLookPath already establishes. Nothing here calls os.Setenv, which
// is process-global and would leak between parallel tests.
var loadBuildHostDefault = configBuildHostDefault

func configBuildHostDefault() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	return strings.TrimSpace(cfg.DefaultBuildHost), nil
}

// resolveBuildHostName returns the device that should build, most explicit
// signal first: the --build-host flag, then the persisted per-developer
// default. An empty result means "build locally" and must leave every existing
// local path untouched.
func resolveBuildHostName(flagValue string) (string, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v, nil
	}
	return loadBuildHostDefault()
}

// validateBuildHostFlags rejects flag combinations where both values cannot be
// honoured, so the conflict surfaces before a build rather than as a silently
// ignored flag.
func validateBuildHostFlags(buildHost, builder string) error {
	if strings.TrimSpace(buildHost) != "" && strings.TrimSpace(builder) != "" {
		return errBuilderWithBuildHost
	}
	return nil
}

// checkBuildHostCapabilities refuses a build host before any context is
// transferred. Every failure names the host, and none falls back to a local
// build: a long build the developer believed was running on the Spark is worse
// than an error.
func checkBuildHostCapabilities(host string, resp *agentpbv2.GetBuildCapabilitiesResponse, platform string) error {
	if !resp.GetBuilderEnabled() {
		return fmt.Errorf("%s is not configured as a build host; enable the builder role on that device, or omit --build-host to build locally", host)
	}
	if !resp.GetBuildkitAvailable() {
		// On darwin this is a design fact, not a misconfiguration: the Mac agent
		// runs Linux containers through Apple Container, which has no BuildKit
		// underneath. Saying so stops it reading as a bug to be fixed.
		if strings.EqualFold(resp.GetOs(), "darwin") {
			return fmt.Errorf("%s has no BuildKit daemon: macOS hosts run containers through Apple Container, which has no BuildKit underneath, so a Mac cannot be a build host", host)
		}
		return fmt.Errorf("%s has no BuildKit daemon and cannot build", host)
	}
	if slices.Contains(resp.GetNativePlatforms(), platform) {
		return nil
	}
	if slices.Contains(resp.GetEmulatedPlatforms(), platform) {
		cliNotice("%s builds %s under emulation; expect it to be slower than a native build", host, platform)
		return nil
	}
	return fmt.Errorf("%s cannot build %s: it builds %s natively and emulates %s",
		host, platform,
		formatPlatformList(resp.GetNativePlatforms()),
		formatPlatformList(resp.GetEmulatedPlatforms()))
}

func formatPlatformList(platforms []string) string {
	if len(platforms) == 0 {
		return "nothing"
	}
	return strings.Join(platforms, ", ")
}
