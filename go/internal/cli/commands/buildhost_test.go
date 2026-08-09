package commands

import (
	"errors"
	"strings"
	"testing"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

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
