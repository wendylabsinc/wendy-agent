package commands

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// `wendy build` never reaches runRemoteBuild, so accepting --build-host there
// would build locally while the developer believed the Spark was doing it.
func TestBuildCmd_RefusesBuildHostRatherThanIgnoringIt(t *testing.T) {
	cmd := newBuildCmd()
	cmd.SetArgs([]string{"--build-host", "spark-office"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if !errors.Is(err, errBuildHostOnBuildCmd) {
		t.Fatalf("got %v, want the build-host-unsupported error; a silently ignored flag builds on the wrong machine", err)
	}
	if !strings.Contains(err.Error(), "wendy run") {
		t.Fatalf("the refusal must point at the command that does support it, got: %v", err)
	}
}

func TestBuildCmd_HidesUnsupportedBuildHostFlagFromHelp(t *testing.T) {
	cmd := newBuildCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("build --help: %v", err)
	}
	if strings.Contains(out.String(), "--build-host") {
		t.Fatalf("wendy build help must not advertise a flag that it always refuses:\n%s", out.String())
	}
}

func TestSelectDevice_DoesNotMutateGlobalDeviceFlag(t *testing.T) {
	original := deviceFlag
	deviceFlag = "target-device"
	t.Cleanup(func() { deviceFlag = original })

	cfg := resolveConfig{}
	SelectDevice(" spark-office ")(&cfg)
	if cfg.device != "spark-office" {
		t.Fatalf("explicit resolver device = %q, want spark-office", cfg.device)
	}
	if deviceFlag != "target-device" {
		t.Fatalf("explicit nested selection mutated global deviceFlag to %q", deviceFlag)
	}
}

func TestCheckBuildHostCapabilities_NotOptedIn(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    false,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a host that has not opted in must be refused")
	}
	if !strings.Contains(err.Error(), "spark-office") {
		t.Fatalf("error must name the host, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_NoBuildkitOnMacSaysWhy(t *testing.T) {
	err := checkBuildHostCapabilities("neo-lab", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: false,
		Os:                "darwin",
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a host without buildkit must be refused")
	}
	if !strings.Contains(err.Error(), "neo-lab") {
		t.Fatalf("error must name the host, got: %v", err)
	}
	// A bare "no BuildKit" on a Mac reads as a bug rather than a design fact.
	if !strings.Contains(err.Error(), "Apple Container") {
		t.Fatalf("a darwin host should explain why it has no BuildKit, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_NoBuildkitElsewhere(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: false,
		Os:                "linux",
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a linux host without buildkit must also be refused")
	}
	if strings.Contains(err.Error(), "Apple Container") {
		t.Fatalf("the darwin explanation must not leak onto a linux host, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_PlatformUnsupported(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/amd64")
	if err == nil {
		t.Fatal("a platform that is neither native nor emulated must be refused")
	}
	if !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("error must name the requested platform, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_EmulatedIsAllowed(t *testing.T) {
	if err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
		EmulatedPlatforms: []string{"linux/amd64"},
	}, "linux/amd64"); err != nil {
		t.Fatalf("an emulated platform must be allowed, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_NativePasses(t *testing.T) {
	if err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/arm64"); err != nil {
		t.Fatalf("a native platform must pass, got: %v", err)
	}
}

func TestResolveBuildHostName_FlagBeatsConfig(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "spark-office", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("neo-lab")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "neo-lab" {
		t.Fatalf("got %q, want the flag value %q", got, "neo-lab")
	}
}

func TestResolveBuildHostName_FallsBackToConfig(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "spark-office", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "spark-office" {
		t.Fatalf("got %q, want the config default", got)
	}
}

func TestResolveBuildHostName_EmptyWhenUnset(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty so the local path stays untouched", got)
	}
}

// Whitespace-only values must read as unset rather than as a device literally
// named " ", which would fail much later with a confusing resolution error.
func TestResolveBuildHostName_TrimsWhitespace(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("   ")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty for a whitespace-only flag", got)
	}
}

// The neo → spark → robot case: the developer's machine has no Docker, no
// Apple Container and no buildkitd, and the remote path must still work. This
// is a hard requirement, not a happy accident — the local path bootstraps
// daemons in several places that the remote path must not reach.
func TestRemoteBuildPath_NeedsNoLocalBuilder(t *testing.T) {
	origLook := imageBuilderLookPath
	t.Cleanup(func() { imageBuilderLookPath = origLook })
	imageBuilderLookPath = func(string) (string, error) {
		t.Error("the remote build path must not look for a local container builder")
		return "", exec.ErrNotFound
	}

	if err := assertNoLocalBuilderNeeded("spark-office"); err != nil {
		t.Fatalf("assertNoLocalBuilderNeeded: %v", err)
	}
}

// Build failure and delivery failure have different remedies — a Dockerfile fix
// versus mesh reachability or registry auth — so collapsing them sends the
// developer to debug the wrong layer.
func TestClassifyRemoteBuildError_SeparatesDeliveryFromBuild(t *testing.T) {
	buildErr := classifyRemoteBuildError("spark-office", status.Error(codes.Internal, "build failed: exit status 1"))
	if !isImageBuildFailure(buildErr) {
		t.Error("a build failure must classify as an image build failure so no fallback masks it")
	}
	if !strings.Contains(buildErr.Error(), "spark-office") {
		t.Errorf("build error must name the build host, got: %v", buildErr)
	}

	deliveryErr := classifyRemoteBuildError("spark-office", status.Error(codes.Unavailable, "dial tcp: no route to host"))
	if isImageBuildFailure(deliveryErr) {
		t.Error("a delivery failure must NOT classify as a build failure")
	}
	if !strings.Contains(deliveryErr.Error(), "spark-office") {
		t.Errorf("delivery error must name the build host, got: %v", deliveryErr)
	}
}

func TestClassifyRemoteBuildError_NilStaysNil(t *testing.T) {
	if err := classifyRemoteBuildError("spark-office", nil); err != nil {
		t.Fatalf("a nil error must stay nil, got %v", err)
	}
}

// The result event ends the build. A client that waits for EOF instead hangs on
// a finished build the moment the server keeps the stream open past it — a
// trailing event a later agent version is free to add.
func TestConsumeBuildProgress_StopsAtResultWithoutWaitingForEOF(t *testing.T) {
	events := []*agentpbv2.BuildImageProgress{
		{Event: &agentpbv2.BuildImageProgress_LogLine{LogLine: "#1 [app 1/2] FROM python"}},
		{Event: &agentpbv2.BuildImageProgress_Result{
			Result: &agentpbv2.BuildImageResult{ImageDigest: "sha256:abc"},
		}},
	}
	i := 0
	recv := func() (*agentpbv2.BuildImageProgress, error) {
		if i >= len(events) {
			// Never EOF: a stream held open past the result is exactly the case
			// a client that only stops on EOF would block on forever.
			t.Fatal("consumed past the result event; the build had already finished")
		}
		e := events[i]
		i++
		return e, nil
	}

	var out strings.Builder
	if err := consumeBuildProgress(recv, &out); err != nil {
		t.Fatalf("consumeBuildProgress: %v", err)
	}
	if !strings.Contains(out.String(), "[app 1/2] FROM python") {
		t.Fatalf("log lines must still be forwarded, got %q", out.String())
	}
}

// EOF without a result is still a clean end: an older agent may not send one.
func TestConsumeBuildProgress_StopsAtEOF(t *testing.T) {
	recv := func() (*agentpbv2.BuildImageProgress, error) { return nil, io.EOF }
	if err := consumeBuildProgress(recv, io.Discard); err != nil {
		t.Fatalf("a stream that just ends must not be an error, got %v", err)
	}
}

func TestValidateBuildHostFlags_RejectsBuilderCombo(t *testing.T) {
	err := validateBuildHostFlags("spark-office", "docker")
	if !errors.Is(err, errBuilderWithBuildHost) {
		t.Fatalf("got %v, want errBuilderWithBuildHost", err)
	}
}

func TestValidateBuildHostFlags_AllowsEitherAlone(t *testing.T) {
	if err := validateBuildHostFlags("spark-office", ""); err != nil {
		t.Fatalf("build host alone must be allowed: %v", err)
	}
	if err := validateBuildHostFlags("", "docker"); err != nil {
		t.Fatalf("builder alone must be allowed: %v", err)
	}
	if err := validateBuildHostFlags("", ""); err != nil {
		t.Fatalf("neither flag set must be allowed: %v", err)
	}
}
