package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
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

// errBuildHostOnBuildCmd is returned by `wendy build --build-host`. Only
// `wendy run` can delegate a build: the remote path's whole delivery step is a
// push into the TARGET device's registry, and `wendy build` has no target. A
// remote `wendy build` would leave the image on the build host and nowhere the
// developer asked for, so the flag is refused rather than half-honoured.
var errBuildHostOnBuildCmd = errors.New(
	"--build-host is supported by `wendy run`, not `wendy build`: a remote build delivers the image to the target device, and `wendy build` has no target; use `wendy run --build-host` instead")

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

// assertNoLocalBuilderNeeded documents and enforces the neo → spark → robot
// requirement: with --build-host, nothing on this path may look for a local
// container builder. It exists so that guarantee is covered by a test rather
// than left as a convention someone later breaks by adding a daemon bootstrap.
func assertNoLocalBuilderNeeded(host string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("assertNoLocalBuilderNeeded called without a build host")
	}
	return nil
}

// classifyRemoteBuildError separates "the build failed" from "the build
// succeeded but could not be delivered". The remedies diverge — a build-file
// fix versus mesh reachability or registry auth — so collapsing them would send
// the developer to debug the wrong layer.
func classifyRemoteBuildError(host string, err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("image built on %s but could not be delivered to the device: %w", host, err)
	default:
		if strings.Contains(err.Error(), "push:") {
			return fmt.Errorf("image built on %s but could not be delivered to the device: %w", host, err)
		}
		// Marked as an image-build failure so the caller surfaces it directly
		// rather than masking it behind a fallback that would fail identically.
		return &imageBuildFailedError{err: fmt.Errorf("build on %s failed: %w", host, err)}
	}
}

// runRemoteBuild builds the image on another WendyOS device, has that device
// push it straight into the target's registry over the mesh, and then creates
// the container through the unchanged registry-push deploy path.
//
// Nothing here touches a local container builder: no ensureDockerDaemon, no
// ensureBuildxBuilder, no ensureAppleContainerSystemForBuilder. That is the
// point of the feature — see assertNoLocalBuilderNeeded.
func runRemoteBuild(
	ctx context.Context,
	target *grpcclient.AgentConnection,
	host, cwd string,
	appCfg *appconfig.AppConfig,
	platform, dockerfile string,
	buildArgs map[string]string,
	deployEnv []string,
	opts runOptions,
) error {
	if err := assertNoLocalBuilderNeeded(host); err != nil {
		return err
	}

	builder, err := connectBuildHost(ctx, host)
	if err != nil {
		return fmt.Errorf("connecting to build host %s: %w", host, err)
	}
	defer builder.Close()

	caps, err := builder.BuildService.GetBuildCapabilities(ctx, &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		return fmt.Errorf("querying build host %s: %w", host, err)
	}
	if err := checkBuildHostCapabilities(host, caps, platform); err != nil {
		return err
	}

	// Resolve the build file on THIS machine: a Stagefile compile pins digests
	// and writes its lockfile into the project, which must happen where the repo
	// is, not in a scratch dir on the builder.
	resolved, err := prepareDockerBuildFile(cwd, dockerfile)
	if err != nil {
		return err
	}
	resolvedPath := filepath.Join(cwd, resolved)

	tarBytes, err := packBuildContext(cwd, resolvedPath)
	if err != nil {
		return err
	}

	pushTarget, err := targetPushTarget(ctx, target, appCfg)
	if err != nil {
		return err
	}

	buildTitle := fmt.Sprintf("Building on %s for %s...", tui.Value(host), tui.Value(platform))
	if err := runBuildWithProgress(ctx, buildTitle, dumpRawAlways, func(stream, logw io.Writer) error {
		manifest, err := pushBuildContext(ctx, builder.ContainerService, tarBytes)
		if err != nil {
			return err
		}
		return streamRemoteBuild(ctx, builder, &agentpbv2.BuildSpec{
			AppId:      appCfg.AppID,
			Platform:   platform,
			PushTarget: pushTarget,
			Context:    manifest,
			Definition: &agentpbv2.BuildSpec_DockerfileBuild{
				DockerfileBuild: &agentpbv2.DockerfileBuild{
					Dockerfile: resolved,
					BuildArgs:  buildArgs,
				},
			},
		}, stream)
	}); err != nil {
		return classifyRemoteBuildError(host, err)
	}

	appConfigData, err := json.Marshal(appCfg)
	if err != nil {
		return fmt.Errorf("marshaling app config: %w", err)
	}
	createReq := &agentpb.CreateContainerRequest{
		ImageName:     localRegistryReference(ctx, target, appCfg),
		AppName:       appCfg.AppID,
		AppConfig:     appConfigData,
		RestartPolicy: resolveRestartPolicy(opts),
		UserArgs:      opts.userArgs,
		Env:           deployEnv,
	}
	return startAndStreamContainer(ctx, target, appCfg, createReq, opts)
}

// connectBuildHost resolves and connects to the build host by name, reusing the
// same connect machinery as --device so LKG cache, mDNS and cloud fallback all
// apply unchanged.
func connectBuildHost(ctx context.Context, host string) (*grpcclient.AgentConnection, error) {
	prev := deviceFlag
	deviceFlag = host
	defer func() { deviceFlag = prev }()

	// resolveTarget short-circuits to the cloud when the context carries a cloud
	// device config, and that path reads the config's DeviceName rather than
	// deviceFlag. Left alone, a cloud-routed invocation would silently connect
	// to the TARGET again and "build on the build host" would quietly become
	// "build on the target" — repoint it at the build host instead.
	if cloudCfg, ok := cloudDeviceConfigFromContext(ctx); ok {
		cloudCfg.DeviceName = host
		ctx = context.WithValue(ctx, cloudDeviceContextKey{}, cloudCfg)
	}

	sel, err := resolveTarget(ctx, NonInteractive(), SuppressUpdateCheck())
	if err != nil {
		return nil, err
	}
	if sel.Agent == nil {
		sel.Close()
		return nil, fmt.Errorf("%s is not a WendyOS device with an agent; a build host must be one", host)
	}
	return sel.Agent, nil
}

// targetPushTarget describes where the BUILD HOST should push, as a mesh peer
// rather than a hostname.
//
// The asset id is deliberate: a name like device-<id>.cloud.wendy.dev only
// resolves where the mesh DNS server runs, which excludes adopted Linux hosts
// whose resolver already owns 127.0.0.53 — so a reachable peer looked
// unreachable. The build host's peer dialer needs only the id (WDY-2356).
func targetPushTarget(ctx context.Context, target *grpcclient.AgentConnection, appCfg *appconfig.AppConfig) (*agentpbv2.PushTarget, error) {
	resp, err := target.ProvisioningService.IsProvisioned(ctx, &agentpb.IsProvisionedRequest{})
	if err != nil {
		return nil, fmt.Errorf("determining the target device's mesh identity: %w", err)
	}
	prov, ok := resp.GetResponse().(*agentpb.IsProvisionedResponse_Provisioned)
	if !ok {
		return nil, fmt.Errorf("the target device is not provisioned, so a build host cannot reach its registry; provision it or omit --build-host")
	}
	agentOS, err := targetAgentOS(ctx, target)
	if err != nil {
		return nil, err
	}
	return &agentpbv2.PushTarget{
		AssetId:      prov.Provisioned.GetAssetId(),
		RegistryPort: uint32(registryPort(agentOS)),
		Repository:   strings.ToLower(appCfg.AppID) + ":latest",
	}, nil
}

// localRegistryReference is the same image as the target itself sees it. The
// target's registry names images from its OWN listen address, so whatever
// address the build host pushed to, the image lands here.
func localRegistryReference(ctx context.Context, target *grpcclient.AgentConnection, appCfg *appconfig.AppConfig) string {
	agentOS, _ := targetAgentOS(ctx, target)
	return fmt.Sprintf("localhost:%d/%s:latest", registryPort(agentOS), strings.ToLower(appCfg.AppID))
}

func targetAgentOS(ctx context.Context, target *grpcclient.AgentConnection) (string, error) {
	resp, err := target.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return "", fmt.Errorf("querying the target device: %w", err)
	}
	if os := resp.GetOs(); os != "" {
		return os, nil
	}
	return "linux", nil
}

// streamRemoteBuild drives one BuildImage stream, forwarding the build host's
// plain-mode output into the CLI's existing progress renderer.
func streamRemoteBuild(ctx context.Context, builder *grpcclient.AgentConnection, spec *agentpbv2.BuildSpec, out io.Writer) error {
	stream, err := builder.BuildService.BuildImage(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&agentpbv2.BuildImageRequest{Spec: spec}); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if line := msg.GetLogLine(); line != "" {
			fmt.Fprintln(out, line)
		}
	}
}
