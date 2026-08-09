package services

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// buildHostEnabledFile, when present in the agent config dir, opts this device
// in as a build host. Unlike meshDisabledFile this is opt-IN, and deliberately
// so: BuildImage runs build instructions supplied by a client, so a device must
// volunteer for that role rather than acquire it by being reachable with an
// org certificate.
const buildHostEnabledFile = "build-host-enabled"

// DefaultBuildkitAddress is where buildkitd listens on a WendyOS device — the
// same socket the on-device buildctl path already uses.
const DefaultBuildkitAddress = "unix:///run/buildkit/buildkitd.sock"

// defaultBuildStateDir holds reassembled build contexts, one directory per app.
const defaultBuildStateDir = "/var/lib/wendy/buildctx"

// BuildServiceOptions configures the build service.
type BuildServiceOptions struct {
	// ConfigPath is the agent config dir searched for the opt-in marker.
	ConfigPath string
	// BuildkitAddress overrides DefaultBuildkitAddress.
	BuildkitAddress string
	// StateDir overrides defaultBuildStateDir.
	StateDir string
}

// BuildService lets a CLI delegate an image build to this device. See the
// service comment in build_service.proto for the trust model.
type BuildService struct {
	agentpbv2.UnimplementedWendyBuildServiceServer
	logger               *zap.Logger
	buildHostEnabledPath string
	buildkitAddress      string
	stateDir             string
}

func NewBuildService(logger *zap.Logger, opts BuildServiceOptions) *BuildService {
	if opts.BuildkitAddress == "" {
		opts.BuildkitAddress = DefaultBuildkitAddress
	}
	if opts.StateDir == "" {
		opts.StateDir = defaultBuildStateDir
	}
	return &BuildService{
		logger:               logger,
		buildHostEnabledPath: filepath.Join(opts.ConfigPath, buildHostEnabledFile),
		buildkitAddress:      opts.BuildkitAddress,
		stateDir:             opts.StateDir,
	}
}

// builderEnabled reports whether this device has opted in to serving builds.
func (s *BuildService) builderEnabled() bool {
	_, err := os.Stat(s.buildHostEnabledPath)
	return err == nil
}

func (s *BuildService) GetBuildCapabilities(_ context.Context, _ *agentpbv2.GetBuildCapabilitiesRequest) (*agentpbv2.GetBuildCapabilitiesResponse, error) {
	available := buildkitSocketPresent(s.buildkitAddress)
	resp := &agentpbv2.GetBuildCapabilitiesResponse{
		BuildkitAvailable: available,
		Os:                runtime.GOOS,
		CpuArchitecture:   runtime.GOARCH,
		BuilderEnabled:    s.builderEnabled(),
	}
	if available {
		// Only the host's own platform is claimed native. Emulated platforms stay
		// empty until binfmt detection lands: over-claiming here would turn a
		// fast CLI refusal into a slow, mysterious remote failure.
		resp.NativePlatforms = []string{runtime.GOOS + "/" + runtime.GOARCH}
	}
	return resp, nil
}

// buildkitSocketPresent reports whether a unix-socket buildkitd address points
// at something that exists. A non-unix address is taken at face value: only the
// device socket form can be checked without dialing.
func buildkitSocketPresent(addr string) bool {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return addr != ""
	}
	_, err := os.Stat(path)
	return err == nil
}

func (s *BuildService) BuildImage(stream agentpbv2.WendyBuildService_BuildImageServer) error {
	if !s.builderEnabled() {
		return status.Error(codes.FailedPrecondition,
			"this device is not configured as a build host; create the build-host-enabled marker in the agent config directory to allow remote builds")
	}
	return status.Error(codes.Unimplemented, "build execution lands in a later task")
}
