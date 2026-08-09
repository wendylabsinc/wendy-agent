package commands

import (
	"errors"
	"testing"
)

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
