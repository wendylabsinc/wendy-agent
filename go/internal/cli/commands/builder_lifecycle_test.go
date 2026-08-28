package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A registry proxy gets a new port on every CLI invocation. Changing that
// endpoint must reconfigure and restart the existing builder, not replace it:
// replacement deletes the warm BuildKit snapshots and makes large CUDA images
// rehydrate their complete local-cache chain on every run.
func TestPlaintextBuilderConfigChangePreservesBuilder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker command is a POSIX shell script")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	dockerPath := filepath.Join(binDir, "docker")
	const fakeDocker = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG"
if [ "$1" = "exec" ] && [ "$3" = "cat" ]; then
  printf 'stale buildkit config'
fi
`
	if err := os.WriteFile(dockerPath, []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("WENDY_BUILDX_BUILDER", "cache-preserve-test")

	if _, err := ensurePlaintextBuilder(context.Background(), t.TempDir(), "127.0.0.1:61234", io.Discard); err != nil {
		t.Fatalf("ensurePlaintextBuilder: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Contains(log, "buildx rm") || strings.Contains(log, "buildx create") {
		t.Fatalf("config change replaced the builder instead of preserving its cache:\n%s", log)
	}
	if !strings.Contains(log, "restart buildx_buildkit_cache-preserve-test0") {
		t.Fatalf("config change did not restart the existing builder:\n%s", log)
	}
}
