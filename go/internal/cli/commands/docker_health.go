package commands

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	minimumBuildkitFreeBytes = int64(1 << 30)
	warnBuildkitFreeBytes    = int64(10 << 30)
)

// dockerBackendDescription identifies the daemon Wendy is actually using.
// DOCKER_HOST overrides the selected context, so report it first when present.
func dockerBackendDescription(ctx context.Context) string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return "DOCKER_HOST=" + host
	}
	out, err := exec.CommandContext(ctx, "docker", "context", "show").Output()
	if err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return "context=" + name
		}
	}
	return "current Docker context"
}

// dockerOperationError turns common local-daemon failures into actionable
// diagnostics. These errors happen before Wendy transfers or replaces an app,
// so users should repair or select a healthy builder rather than retry blindly.
func dockerOperationError(ctx context.Context, operation string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	backend := dockerBackendDescription(ctx)
	lower := strings.ToLower(detail + " " + err.Error())

	switch {
	case strings.Contains(lower, "input/output error") || strings.Contains(lower, "i/o error"):
		return fmt.Errorf(
			"%s failed on %s because Docker's local image/container store returned an I/O error; the daemon may answer 'docker version' while its storage is still unreadable. Repair or restart that Docker backend, or select a healthy one with DOCKER_HOST, before retrying: %s: %w",
			operation, backend, detail, err,
		)
	case strings.Contains(lower, "no space left on device"):
		return fmt.Errorf(
			"%s failed on %s because the Docker/BuildKit filesystem is full. Free unused builder data or increase that backend's disk allocation before retrying: %s: %w",
			operation, backend, detail, err,
		)
	case strings.Contains(lower, "marked for removal"):
		return fmt.Errorf(
			"%s failed on %s because the BuildKit container is stuck pending removal. Check the Docker backend for storage errors before recreating the builder; if the backend is healthy, select or recreate the builder and retry: %s: %w",
			operation, backend, detail, err,
		)
	default:
		return fmt.Errorf("%s failed on %s: %s: %w", operation, backend, detail, err)
	}
}

// diagnoseDockerBuilderError checks whether a failed builder operation is only
// the visible symptom of an unreadable Docker image store. The read-only image
// listing runs only after a failure, so healthy builds keep the fast path.
func diagnoseDockerBuilderError(ctx context.Context, operation string, operationOutput []byte, operationErr error) error {
	out, err := exec.CommandContext(ctx, "docker", "image", "ls", "--quiet", "--no-trunc").CombinedOutput()
	if err != nil {
		storageErr := dockerOperationError(ctx, "checking Docker image-store health", out, err)
		return fmt.Errorf("%w (diagnosed after %s failed: %s)", storageErr, operation, strings.TrimSpace(string(operationOutput)))
	}
	return dockerOperationError(ctx, operation, operationOutput, operationErr)
}

// parsePOSIXDFAvailableBytes reads the Available column from `df -Pk` output.
func parsePOSIXDFAvailableBytes(output []byte) (int64, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Fields(lines[i])
		if len(fields) < 4 {
			continue
		}
		availableKiB, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || availableKiB < 0 {
			continue
		}
		if availableKiB > math.MaxInt64/1024 {
			return 0, fmt.Errorf("available block count overflows bytes")
		}
		return availableKiB * 1024, nil
	}
	return 0, fmt.Errorf("could not find the Available column in df output %q", strings.TrimSpace(string(output)))
}

// ensureBuildxBuilderFreeSpace verifies the filesystem that backs BuildKit,
// not merely the host filesystem. Sparse Docker Desktop disks and small Colima
// profiles can have very different free space from the host.
func ensureBuildxBuilderFreeSpace(ctx context.Context, builderName string, w io.Writer) error {
	containerName := "buildx_buildkit_" + builderName + "0"
	out, err := exec.CommandContext(ctx, "docker", "exec", containerName,
		"df", "-Pk", "/var/lib/buildkit").CombinedOutput()
	if err != nil {
		return diagnoseDockerBuilderError(ctx, "checking BuildKit free space", out, err)
	}

	available, err := parsePOSIXDFAvailableBytes(out)
	if err != nil {
		// A future BuildKit image may format df differently. Do not break all
		// builds over an advisory probe when the command itself succeeded.
		fmt.Fprintf(w, "[buildx] warning: could not parse free space for builder %q: %v\n", builderName, err)
		return nil
	}
	if available < minimumBuildkitFreeBytes {
		return fmt.Errorf(
			"BuildKit builder %q on %s has only %s free; Wendy requires at least %s before starting a build. Free unused builder data or increase the Docker/Colima disk allocation",
			builderName, dockerBackendDescription(ctx), formatBytes(available), formatBytes(minimumBuildkitFreeBytes),
		)
	}
	if available < warnBuildkitFreeBytes {
		fmt.Fprintf(w, "[buildx] warning: builder %q on %s has only %s free; large CUDA or Isaac images may exhaust it\n",
			builderName, dockerBackendDescription(ctx), formatBytes(available))
	}
	return nil
}
