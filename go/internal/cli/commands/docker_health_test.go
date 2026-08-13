package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParsePOSIXDFAvailableBytes(t *testing.T) {
	output := []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vdb1 41922560 40873984 1048576 98% /var/lib/docker\n")
	got, err := parsePOSIXDFAvailableBytes(output)
	if err != nil {
		t.Fatalf("parsePOSIXDFAvailableBytes: %v", err)
	}
	if want := int64(1 << 30); got != want {
		t.Fatalf("available bytes = %d, want %d", got, want)
	}
}

func TestParsePOSIXDFAvailableBytesRejectsMalformedOutput(t *testing.T) {
	if _, err := parsePOSIXDFAvailableBytes([]byte("not df output")); err == nil {
		t.Fatal("expected malformed df output to fail")
	}
}

func TestDockerOperationErrorClassifiesStorageIO(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")
	err := dockerOperationError(
		context.Background(),
		"bootstrapping BuildKit builder",
		[]byte("write /var/lib/desktop-containerd/daemon/io.containerd.metadata.v1.bolt/meta.db: input/output error"),
		errors.New("exit status 1"),
	)
	msg := err.Error()
	for _, want := range []string{
		"Docker's local image/container store returned an I/O error",
		"DOCKER_HOST=unix:///tmp/test-docker.sock",
		"select a healthy one with DOCKER_HOST",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

func TestDockerOperationErrorClassifiesFullBuilder(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")
	err := dockerOperationError(
		context.Background(),
		"exporting image",
		[]byte("write /var/lib/buildkit/content/ingest/data: no space left on device"),
		errors.New("exit status 1"),
	)
	if msg := err.Error(); !strings.Contains(msg, "Docker/BuildKit filesystem is full") {
		t.Fatalf("error = %q, want full-filesystem diagnostic", msg)
	}
}

func TestDockerOperationErrorClassifiesBuilderPendingRemoval(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")
	err := dockerOperationError(
		context.Background(),
		"bootstrapping BuildKit builder",
		[]byte("container is marked for removal and cannot be started"),
		errors.New("exit status 1"),
	)
	if msg := err.Error(); !strings.Contains(msg, "stuck pending removal") {
		t.Fatalf("error = %q, want pending-removal diagnostic", msg)
	}
}

func TestDiagnoseDockerBuilderErrorFindsUnderlyingImageStoreIO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker command is a POSIX shell script")
	}
	binDir := t.TempDir()
	dockerPath := filepath.Join(binDir, "docker")
	const script = `#!/bin/sh
printf '%s\n' 'open /var/lib/desktop-containerd/daemon/io.containerd.content.v1.content/blobs/sha256/test: input/output error' >&2
exit 1
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")

	err := diagnoseDockerBuilderError(
		context.Background(),
		"bootstrapping BuildKit builder",
		[]byte("container is marked for removal and cannot be started"),
		errors.New("exit status 1"),
	)
	msg := err.Error()
	for _, want := range []string{
		"Docker's local image/container store returned an I/O error",
		"diagnosed after bootstrapping BuildKit builder failed",
		"container is marked for removal",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

func TestEnsureBuildxBuilderFreeSpaceRejectsExhaustedBuilder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker command is a POSIX shell script")
	}
	installFakeDockerDF(t, "0")
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")

	err := ensureBuildxBuilderFreeSpace(context.Background(), "test", &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected exhausted builder filesystem to fail")
	}
	for _, want := range []string{"has only 0 B free", "at least 1.1 GB", "increase the Docker/Colima disk allocation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestEnsureBuildxBuilderFreeSpaceWarnsWhenLow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake docker command is a POSIX shell script")
	}
	// Five GiB is enough to proceed but below the ten-GiB warning threshold.
	installFakeDockerDF(t, "5242880")
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")
	var output bytes.Buffer

	if err := ensureBuildxBuilderFreeSpace(context.Background(), "test", &output); err != nil {
		t.Fatalf("ensureBuildxBuilderFreeSpace: %v", err)
	}
	if msg := output.String(); !strings.Contains(msg, "large CUDA or Isaac images may exhaust it") {
		t.Fatalf("warning = %q, want large-image capacity warning", msg)
	}
}

func installFakeDockerDF(t *testing.T, availableKiB string) {
	t.Helper()
	binDir := t.TempDir()
	dockerPath := filepath.Join(binDir, "docker")
	script := "#!/bin/sh\n" +
		"printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n'\n" +
		"printf '/dev/vdb1 41922560 41922560 " + availableKiB + " 100%% /var/lib/docker\\n'\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
