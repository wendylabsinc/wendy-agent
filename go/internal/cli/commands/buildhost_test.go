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

	deliveryErr := classifyRemoteBuildError("spark-office", status.Error(codes.Unavailable, "pushing the built image to the target device failed: dial tcp: no route to host"))
	if isImageBuildFailure(deliveryErr) {
		t.Error("a delivery failure must NOT classify as a build failure")
	}
	if !strings.Contains(deliveryErr.Error(), "spark-office") {
		t.Errorf("delivery error must name the build host, got: %v", deliveryErr)
	}
}

func TestClassifyRemoteBuildError_DoesNotAssumeGenericTransportErrorIsDelivery(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded} {
		err := classifyRemoteBuildError("spark-office", status.Error(code, "connection closed"))
		if !isImageBuildFailure(err) {
			t.Errorf("%s must not claim the image was built without the server's delivery marker: %v", code, err)
		}
		if strings.Contains(err.Error(), "image built") {
			t.Errorf("%s error makes an unsupported delivery claim: %v", code, err)
		}
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

func TestResolveAndValidateRunBuildHost_RejectsBuilderWithConfiguredDefault(t *testing.T) {
	orig := loadBuildHostDefault
	t.Cleanup(func() { loadBuildHostDefault = orig })
	loadBuildHostDefault = func() (string, error) { return "spark-office", nil }

	host, err := resolveAndValidateRunBuildHost("", "docker")
	if host != "" {
		t.Fatalf("host = %q, want no usable host on conflict", host)
	}
	if !errors.Is(err, errBuilderWithBuildHost) {
		t.Fatalf("got %v, want errBuilderWithBuildHost", err)
	}
}

func TestRejectUnsupportedBuildHostProject(t *testing.T) {
	if err := rejectUnsupportedBuildHostProject("", "Compose projects"); err != nil {
		t.Fatalf("local builds remain supported: %v", err)
	}
	err := rejectUnsupportedBuildHostProject("spark-office", "Compose projects")
	if err == nil || !strings.Contains(err.Error(), "single-service container image projects") {
		t.Fatalf("got %v, want a clear remote-build support boundary", err)
	}
}

// TestCloudFallbackDeviceName_ExplicitWinsOverDeviceFlag is the contract that
// keeps `wendy run --build-host` honest. Two devices are in flight -- the build
// host and the deploy target -- and --device names the TARGET. If the cloud
// fallback preferred the flag, connectBuildHost would tunnel to the target and
// build there, putting the image on the machine the developer meant to deploy
// to while reporting success.
func TestCloudFallbackDeviceName_ExplicitWinsOverDeviceFlag(t *testing.T) {
	got := cloudFallbackDeviceName("spark-office", "ccr2", "some-default")
	if got != "spark-office" {
		t.Fatalf("got %q, want the explicit build host to win over --device", got)
	}
}

// A caller with no explicit name is resolving the deploy target itself, where
// --device IS the device being resolved.
func TestCloudFallbackDeviceName_FallsBackToFlagThenDefault(t *testing.T) {
	if got := cloudFallbackDeviceName("", "ccr2", "some-default"); got != "ccr2" {
		t.Fatalf("got %q, want the --device flag", got)
	}
	if got := cloudFallbackDeviceName("", "", "some-default"); got != "some-default" {
		t.Fatalf("got %q, want the configured default", got)
	}
	if got := cloudFallbackDeviceName("", "", ""); got != "" {
		t.Fatalf("got %q, want empty so the caller reports the original error", got)
	}
}

// TestCheckFleetDeliverySupported_RefusesOldAgent: proto3 drops unknown fields,
// so an agent predating push_targets would receive a spec whose fleet it cannot
// see and deliver nowhere, while the CLI reported a fleet deploy. Degrading to
// the first device would be worse: deploying to one machine and claiming
// several.
func TestCheckFleetDeliverySupported_RefusesOldAgent(t *testing.T) {
	old := &agentpbv2.GetBuildCapabilitiesResponse{MultiTargetDelivery: false}
	if err := checkFleetDeliverySupported("spark-office", old, 3); err == nil {
		t.Fatal("want a refusal when several devices are requested of an agent that cannot deliver to them")
	}
	// One device needs nothing new, so an older agent stays usable.
	if err := checkFleetDeliverySupported("spark-office", old, 1); err != nil {
		t.Fatalf("single-device builds must still work against an older agent: %v", err)
	}
	newer := &agentpbv2.GetBuildCapabilitiesResponse{MultiTargetDelivery: true}
	if err := checkFleetDeliverySupported("spark-office", newer, 3); err != nil {
		t.Fatalf("a capable agent must be accepted: %v", err)
	}
}

// TestFleetDeliveryReport_CountsFailures is the rule that keeps a partial fleet
// deploy from reading as a success.
func TestFleetDeliveryReport_CountsFailures(t *testing.T) {
	names := map[int32]string{1: "ccr1", 2: "ccr2", 3: "theta"}
	lines, failed := fleetDeliveryReport([]*agentpbv2.DeliveryResult{
		{AssetId: 1, Delivered: true},
		{AssetId: 2, Delivered: false, Error: "mesh dial failed"},
		{AssetId: 3, Delivered: true},
	}, names)
	if failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"ccr1", "ccr2", "theta", "mesh dial failed", "FAILED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q; got:\n%s", want, joined)
		}
	}
}

// An agent that reports a failure without a reason must still not be summarised
// as fine.
func TestFleetDeliveryReport_FailureWithoutReasonStillFails(t *testing.T) {
	lines, failed := fleetDeliveryReport([]*agentpbv2.DeliveryResult{
		{AssetId: 9, Delivered: false},
	}, nil)
	if failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "asset 9") {
		t.Errorf("an unnamed device must still be identifiable; got %v", lines)
	}
}

func TestPartialFleetDeployError_StatesTheSplit(t *testing.T) {
	err := &errPartialFleetDeploy{failed: 1, total: 3}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("the message must say how many succeeded; got %q", err.Error())
	}
}

// TestSplitFleetDevices_KeepsOrderAndNamesPrimary: the primary is not merely
// first — it is the device the GPU architecture, agent OS and build-arg hints
// are read from, so which name lands there decides what image gets built.
func TestSplitFleetDevices_KeepsOrderAndNamesPrimary(t *testing.T) {
	primary, extras, err := splitFleetDevices("ccr1, ccr2 ,theta")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary != "ccr1" {
		t.Errorf("primary = %q, want ccr1", primary)
	}
	if len(extras) != 2 || extras[0] != "ccr2" || extras[1] != "theta" {
		t.Errorf("extras = %v, want [ccr2 theta] with whitespace trimmed", extras)
	}
}

// A duplicate would report one device twice and hide that another was never
// named — a fleet that looks larger than it is.
func TestSplitFleetDevices_RefusesDuplicatesAndBlanks(t *testing.T) {
	if _, _, err := splitFleetDevices("ccr1,ccr2,ccr1"); err == nil {
		t.Error("want an error for a repeated device")
	}
	if _, _, err := splitFleetDevices("ccr1,,ccr2"); err == nil {
		t.Error("want an error for an empty entry")
	}
}

// A single device must be untouched by any of this.
func TestSplitFleetDevices_SingleDeviceHasNoExtras(t *testing.T) {
	primary, extras, err := splitFleetDevices("ccr1")
	if err != nil || primary != "ccr1" || len(extras) != 0 {
		t.Fatalf("got (%q, %v, %v), want ccr1 with no extras", primary, extras, err)
	}
}

// TestValidateFleetRun_RequiresBuildHostAndDetach: both refusals exist because
// the alternative is silently doing something other than what was asked —
// N local builds, or logs from a fleet interleaved into one terminal.
func TestValidateFleetRun_RequiresBuildHostAndDetach(t *testing.T) {
	extras := []string{"ccr2"}
	if err := validateFleetRun(extras, "", true); err == nil {
		t.Error("a fleet without --build-host must be refused")
	}
	if err := validateFleetRun(extras, "spark-office", false); err == nil {
		t.Error("a fleet without --detach must be refused")
	}
	if err := validateFleetRun(extras, "spark-office", true); err != nil {
		t.Errorf("a valid fleet run must be allowed: %v", err)
	}
	// A single device keeps working with no build host and no --detach.
	if err := validateFleetRun(nil, "", false); err != nil {
		t.Errorf("ordinary single-device runs must be unaffected: %v", err)
	}
}

func TestDeliveryOutcome_RecordsFailureReason(t *testing.T) {
	ok := deliveryOutcome(7, nil)
	if !ok.GetDelivered() || ok.GetError() != "" {
		t.Errorf("got %v, want a clean success", ok)
	}
	bad := deliveryOutcome(8, errors.New("container create refused"))
	if bad.GetDelivered() {
		t.Error("a failed create must not be recorded as delivered")
	}
	if !strings.Contains(bad.GetError(), "container create refused") {
		t.Errorf("the reason must survive; got %q", bad.GetError())
	}
}

// TestBuildkitAbsentNotice_SilentWhenBuildable: enabling on a working build host
// must stay quiet. A note that fires when nothing is wrong trains people to
// ignore the one that matters.
func TestBuildkitAbsentNotice_SilentWhenBuildable(t *testing.T) {
	if got := buildkitAbsentNotice(true, "linux"); got != nil {
		t.Fatalf("got %v, want no notice when BuildKit is available", got)
	}
	if got := buildkitAbsentNotice(true, "darwin"); got != nil {
		t.Fatalf("got %v, want no notice when BuildKit is available", got)
	}
}

// TestBuildkitAbsentNotice_TellsLinuxHostsWhatToInstall covers the case that
// motivated this: `enable` reports success and points at `wendy run
// --build-host`, but the device has no build engine.
func TestBuildkitAbsentNotice_TellsLinuxHostsWhatToInstall(t *testing.T) {
	got := buildkitAbsentNotice(false, "linux")
	if len(got) == 0 {
		t.Fatal("want a notice when BuildKit is absent")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "buildkitd") {
		t.Errorf("notice must name the daemon to install; got %q", joined)
	}
	if !strings.Contains(joined, "unix:///run/buildkit/buildkitd.sock") {
		t.Errorf("notice must name the socket the agent looks at; got %q", joined)
	}
}

// A Mac cannot have BuildKit at all, so it must not be told to install one.
func TestBuildkitAbsentNotice_DoesNotSendMacsAfterADaemon(t *testing.T) {
	joined := strings.Join(buildkitAbsentNotice(false, "darwin"), "\n")
	if joined == "" {
		t.Fatal("want a notice explaining why a Mac cannot build")
	}
	if strings.Contains(joined, "Install buildkitd") {
		t.Errorf("must not tell a Mac to install buildkitd; got %q", joined)
	}
	if !strings.Contains(joined, "Apple Container") {
		t.Errorf("should say why; got %q", joined)
	}
}

// TestCheckBuildHostCapabilities_NamesTheRemedy: this is the message a developer
// actually lands on. The agent's own refusal names the socket and says what to
// install, but the CLI refuses first -- before any context transfer -- so that
// more useful wording is unreachable in practice.
func TestCheckBuildHostCapabilities_NamesTheRemedy(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: false,
		Os:                "linux",
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a host with no BuildKit must be refused")
	}
	if !strings.Contains(err.Error(), "buildkitd") || !strings.Contains(err.Error(), "unix:///run/buildkit/buildkitd.sock") {
		t.Errorf("the error must say what to install and where; got %q", err)
	}
}
