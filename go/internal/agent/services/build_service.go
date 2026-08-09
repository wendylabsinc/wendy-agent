package services

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/buildargs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// maxContextEntryBytes bounds a single build-context file so one entry cannot
// fill the build host's disk. 2 GiB is far above any plausible source file
// while still being a real ceiling.
const maxContextEntryBytes = 2 << 30

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

// contextDir returns the per-app directory a build context is reassembled into.
// It is deliberately stable rather than a fresh temp dir: BuildKit keys its
// local-source cache on this path, so a path that changed between builds would
// make it re-transfer the whole context internally every time.
func (s *BuildService) contextDir(appID string) (string, error) {
	clean := sanitizeAppID(appID)
	if clean == "" {
		return "", status.Errorf(codes.InvalidArgument, "app id %q contains no usable characters", appID)
	}
	dir := filepath.Join(s.stateDir, clean)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating build context directory: %w", err)
	}
	return dir, nil
}

// sanitizeAppID reduces an app id to characters that cannot traverse or escape
// a path component. Dropping unknown characters rather than rejecting outright
// keeps ordinary ids working; an id that sanitises to nothing is rejected by
// contextDir.
func sanitizeAppID(appID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, appID)
}

// extractContextTar writes a build-context tar into dir, refusing any entry
// that would land outside it. The client is not trusted, even an in-org one.
func extractContextTar(r io.Reader, dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading build context: %w", err)
		}
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(hdr.Name, "/") {
			return status.Errorf(codes.InvalidArgument, "build context entry %q is an absolute path", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return status.Errorf(codes.InvalidArgument, "build context entry %q escapes the context root", hdr.Name)
		}
		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
		if err != nil {
			return err
		}
		// Bounded copy: a manifest-declared total size is checked by the caller,
		// but a single entry must not be able to fill the disk on its own.
		if _, err := io.Copy(f, io.LimitReader(tr, maxContextEntryBytes)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}

// buildctlArgs builds the buildctl invocation. It mirrors the CLI's
// buildkitOCIArgs, differing only in the output: an image export that pushes,
// rather than an OCI tar on disk.
func buildctlArgs(contextDir, dockerfile, platform, pushRef string, buildArgs map[string]string) ([]string, error) {
	if filepath.IsAbs(dockerfile) {
		return nil, status.Errorf(codes.InvalidArgument, "dockerfile %q must be relative to the build context", dockerfile)
	}
	target := filepath.Join(contextDir, filepath.FromSlash(dockerfile))
	if !strings.HasPrefix(target, contextDir+string(os.PathSeparator)) {
		return nil, status.Errorf(codes.InvalidArgument, "dockerfile %q escapes the build context", dockerfile)
	}

	keys, err := buildargs.SortedValidatedKeys(buildArgs)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid build arg: %v", err)
	}

	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + contextDir,
		"--local", "dockerfile=" + contextDir,
		"--opt", "filename=" + dockerfile,
		"--opt", "platform=" + platform,
	}
	for _, k := range keys {
		args = append(args, "--opt", "build-arg:"+k+"="+buildArgs[k])
	}
	return append(args, "--output", "type=image,name="+pushRef+",push=true"), nil
}
