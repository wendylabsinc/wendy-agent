package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const wendyBuildkitHostEnv = "WENDY_BUILDKIT_HOST"

var (
	wendyRuntimeCacheDir         = config.CacheDir
	managedBuildkitLookPath      = exec.LookPath
	managedBuildkitSocketPresent = func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}
)

// shouldAutoUseManagedBuildkit reports whether an omitted --builder can use
// Wendy's running local image store. A socket alone is not enough: buildctl is
// the deliberately small host-side client used to carry the BuildKit session.
// Explicit builder choices never consult this function.
func shouldAutoUseManagedBuildkit() bool {
	if managedBuildkitAddress() == "" {
		return false
	}
	_, err := managedBuildkitLookPath("buildctl")
	return err == nil
}

// localBuildkitCommandContext is a seam for the local image-store build. It is
// deliberately separate from the agent's buildctl seam: this command runs on
// the developer host and will eventually target the Wendy-managed build VM.
var localBuildkitCommandContext = exec.CommandContext

// buildkitImageStoreArgs asks BuildKit's image exporter to commit the result
// directly to its worker's image store and unpack it. With the Wendy runtime,
// buildkitd uses a containerd worker, so the same content and snapshots are
// immediately available to local container execution without docker load,
// container image save, an OCI tar, or a registry round-trip.
func buildkitImageStoreArgs(contextDir, dockerfileDir, dockerfileName, platform, imageName string, buildArgs map[string]string) []string {
	args := buildkitFrontendArgs(contextDir, dockerfileDir, dockerfileName, platform, buildArgs)
	return append(args,
		"--output",
		"type=image,name="+imageName+",store=true,unpack=true,oci-mediatypes=true",
	)
}

// managedBuildkitAddress discovers the socket exposed by Wendy's persistent
// local runtime. The runtime owns lifecycle; the build path only consumes its
// stable endpoint, keeping VM startup and BuildKit solve semantics separate.
func managedBuildkitAddress() string {
	cacheDir, err := wendyRuntimeCacheDir()
	if err != nil {
		return ""
	}
	socket := filepath.Join(cacheDir, "runtime", "buildkitd.sock")
	if !managedBuildkitSocketPresent(socket) {
		return ""
	}
	return "unix://" + socket
}

// buildkitCommandArgs prepends an explicit or auto-discovered Wendy endpoint.
// WENDY_BUILDKIT_HOST wins; BUILDKIT_HOST remains supported natively by
// buildctl; otherwise a running Wendy runtime is discovered from its stable
// cache socket without requiring per-shell configuration.
func buildkitCommandArgs(args []string) ([]string, error) {
	addr := strings.TrimSpace(os.Getenv(wendyBuildkitHostEnv))
	buildkitHost := strings.TrimSpace(os.Getenv("BUILDKIT_HOST"))
	if addr == "" && buildkitHost == "" {
		addr = managedBuildkitAddress()
	}
	if addr == "" {
		if buildkitHost != "" {
			// buildctl consumes BUILDKIT_HOST itself.
			return args, nil
		}
		cacheDir, err := wendyRuntimeCacheDir()
		if err != nil {
			return nil, fmt.Errorf(
				"Wendy's managed BuildKit runtime is not running and its socket could not be located: %w; start Wendy Agent for Mac or set %s/BUILDKIT_HOST",
				err,
				wendyBuildkitHostEnv,
			)
		}
		return nil, fmt.Errorf(
			"Wendy's managed BuildKit runtime is not running (expected %s); start Wendy Agent for Mac and wait for the Linux runtime, or set %s/BUILDKIT_HOST",
			filepath.Join(cacheDir, "runtime", "buildkitd.sock"),
			wendyBuildkitHostEnv,
		)
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--addr", addr)
	return append(out, args...), nil
}

// buildDockerProjectWithBuildkit builds directly into the image store exposed
// by a containerd-backed BuildKit worker. It is the first host-facing seam of
// the Wendy build runtime; VM bootstrap and lifecycle management can sit in
// front of the endpoint without changing build semantics.
func buildDockerProjectWithBuildkit(ctx context.Context, dir, imageName, platform, dockerfile string, buildArgs map[string]string, streamOutput, logOutput io.Writer) error {
	dockerfileDir := dir
	dockerfileName := ""
	if dockerfile != "" {
		resolved, err := confinedDockerfilePath(dir, dockerfile)
		if err != nil {
			return err
		}
		dockerfileDir = filepath.Dir(resolved)
		dockerfileName = filepath.Base(resolved)
	}
	if _, err := sortedValidatedBuildArgKeys(buildArgs); err != nil {
		return err
	}

	args, err := buildkitCommandArgs(buildkitImageStoreArgs(
		dir, dockerfileDir, dockerfileName, platform, imageName, buildArgs,
	))
	if err != nil {
		return err
	}
	fmt.Fprintf(logOutput, "[buildkit] building into the Wendy containerd image store: buildctl %s\n", strings.Join(redactBuildctlArgsForLog(args), " "))

	cmd := localBuildkitCommandContext(ctx, "buildctl", args...)
	cmd.Dir = dir
	// BuildKit progress is written to stderr. Use the same writer for both
	// streams so the progress parser sees one ordered stream.
	cmd.Stdout = streamOutput
	cmd.Stderr = streamOutput
	if err := cmd.Run(); err != nil {
		return &imageBuildFailedError{fmt.Errorf("buildctl build (containerd image store) failed: %w", err)}
	}
	return nil
}
