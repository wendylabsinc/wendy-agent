package commands

import (
	"context"
	"strings"
	"testing"
)

func TestDockerControlCommandAppliesDeadline(t *testing.T) {
	cmd, stop := dockerControlCommand(context.Background(), "buildx", "rm", "wendy-mtls")
	defer stop()

	if got := cmd.Args[0]; !strings.HasSuffix(got, "docker") {
		t.Fatalf("expected a docker invocation, got %q", got)
	}
	want := []string{"docker", "buildx", "rm", "wendy-mtls"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := 1; i < len(want); i++ {
		if cmd.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", cmd.Args, want)
		}
	}
}
