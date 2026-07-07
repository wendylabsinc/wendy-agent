package commands

import (
	"os/exec"
	"slices"
	"testing"
)

func TestNormalizeImageBuilder_Buildkit(t *testing.T) {
	got, err := normalizeImageBuilder("buildkit")
	if err != nil {
		t.Fatalf("normalizeImageBuilder(buildkit) error = %v", err)
	}
	if got != imageBuilderBuildkit {
		t.Fatalf("got %q, want %q", got, imageBuilderBuildkit)
	}
}

func TestBuildkitOCIArgs(t *testing.T) {
	args := buildkitOCIArgs("/work", "/work", "Dockerfile", "linux/arm64",
		map[string]string{"FOO": "bar", "ABC": "1"}, "/tmp/out.tar")
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/work",
		"--local", "dockerfile=/work",
		"--opt", "filename=Dockerfile",
		"--opt", "platform=linux/arm64",
		"--opt", "build-arg:ABC=1", // sorted keys → ABC before FOO
		"--opt", "build-arg:FOO=bar",
		"--output", "type=oci,dest=/tmp/out.tar",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("buildkitOCIArgs mismatch:\n got: %v\nwant: %v", args, want)
	}
}

func TestRedactBuildctlArgsForLog(t *testing.T) {
	in := []string{"--opt", "build-arg:TOKEN=secret", "--output", "type=oci,dest=/x"}
	out := redactBuildctlArgsForLog(in)
	for _, a := range out {
		if a == "build-arg:TOKEN=secret" {
			t.Fatal("build-arg value was not redacted")
		}
	}
	if !slices.Contains(out, "build-arg:TOKEN=<redacted>") {
		t.Fatalf("expected redacted build-arg, got %v", out)
	}
	// Non-build-arg tokens must be preserved unchanged.
	if !slices.Contains(out, "--output") {
		t.Fatalf("--output token missing after redaction, got %v", out)
	}
	if !slices.Contains(out, "type=oci,dest=/x") {
		t.Fatalf("output value token missing after redaction, got %v", out)
	}
}

func TestBuildkitRejectsFlagInjectionBuildArg(t *testing.T) {
	if _, err := sortedValidatedBuildArgKeys(map[string]string{"FOO": "-rm-rf"}); err == nil {
		t.Fatal("expected a build-arg value starting with '-' to be rejected")
	}
}

func TestShouldUseBuildkitOnDevice(t *testing.T) {
	origLook := imageBuilderLookPath
	t.Cleanup(func() { imageBuilderLookPath = origLook })

	// On-device (socket set) + docker absent → use buildkit.
	t.Setenv("WENDY_AGENT_SOCKET", "/run/wendy/agent/agent.sock")
	imageBuilderLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if !shouldUseBuildkitOnDevice() {
		t.Error("on-device with no docker should select buildkit")
	}

	// docker present → do not auto-select (let docker handle it).
	imageBuilderLookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	if shouldUseBuildkitOnDevice() {
		t.Error("docker present must not auto-select buildkit")
	}

	// Off-device (no socket) → never auto-select, regardless of docker.
	t.Setenv("WENDY_AGENT_SOCKET", "")
	imageBuilderLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if shouldUseBuildkitOnDevice() {
		t.Error("off-device must not auto-select buildkit")
	}
}
