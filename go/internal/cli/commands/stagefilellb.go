package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/stagefile"
	stagefilesolve "github.com/wendylabsinc/wendy/go/internal/stagefile/solve"
)

type stagefileLLBPlan struct {
	source  string
	options []stagefile.Option
}

var stagefileLLBPlans sync.Map

func stagefileLLBPlanKey(dir, generated string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	return abs + "\x00" + generated
}

// rememberStagefileLLBPlan preserves the exact compiler inputs that produced a
// generated Dockerfile. The lower build layers historically receive only that
// filename; retaining the plan lets the direct backend honor variants, GPU
// targets, --debug, ROS 2 framework options, and download progress unchanged.
func rememberStagefileLLBPlan(dir, generated, source string, opts []stagefile.Option) {
	stagefileLLBPlans.Store(stagefileLLBPlanKey(dir, generated), stagefileLLBPlan{
		source: source, options: append([]stagefile.Option(nil), opts...),
	})
}

func directStagefileLLBPlan(ctx context.Context, dir, dockerfile, builder string) (stagefileLLBPlan, bool, error) {
	source, stagefileGenerated := stagefileSourceForGenerated(dockerfile)
	if !stagefileGenerated {
		return stagefileLLBPlan{}, false, nil
	}
	normalized, err := normalizeImageBuilder(builder)
	if err != nil {
		return stagefileLLBPlan{}, false, err
	}
	useLLB, err := stagefileBackendLLB(stagefileBackendFromContext(ctx), normalized)
	if err != nil || !useLLB {
		return stagefileLLBPlan{}, false, err
	}
	if value, ok := stagefileLLBPlans.Load(stagefileLLBPlanKey(dir, dockerfile)); ok {
		return value.(stagefileLLBPlan), true, nil
	}
	// Normal CLI preparation records a plan on every invocation. The fallback
	// keeps direct internal callers and generated files from older runs useful;
	// they compile the identified variant with its declared defaults.
	return stagefileLLBPlan{source: source, options: []stagefile.Option{stagefile.WithSource(source)}}, true, nil
}

func directStagefileLLBAddress(ctx context.Context, builder string, progress io.Writer) (string, error) {
	normalized, err := resolveOCIExportBuilder(builder)
	if err != nil {
		return "", err
	}
	if normalized == imageBuilderBuildkit || strings.TrimSpace(os.Getenv("BUILDKIT_HOST")) != "" {
		return stagefilesolve.Address(ctx)
	}
	if normalized != imageBuilderDocker {
		return "", fmt.Errorf("direct Stagefile LLB builds require docker or buildkit, got %q", normalized)
	}
	// Match the Dockerfile OCI path's cross-process setup serialization. The
	// lock is released before solving, so independent builds still overlap once
	// the shared daemon is known to be running.
	releaseLock, err := buildLock.acquire(ctx, progress)
	if err != nil {
		return "", err
	}
	builderName, err := ensureOCIExportBuilder(ctx, progress)
	releaseLock()
	if err != nil {
		return "", err
	}
	return stagefilesolve.AddressForBuildxBuilder(ctx, builderName)
}

func solveStagefileLLB(ctx context.Context, dir, platform, builder string, plan stagefileLLBPlan, output stagefilesolve.Output, progress io.Writer) error {
	compiled, err := stagefile.CompileToLLB(dir, platform, plan.options...)
	if err != nil {
		return fmt.Errorf("compiling %s to LLB: %w", plan.source, err)
	}
	addr, err := directStagefileLLBAddress(ctx, builder, progress)
	if err != nil {
		return err
	}
	if err := stagefilesolve.Run(ctx, addr, stagefilesolve.Request{
		Def: compiled.Definition, Config: compiled.Config, BaseConfig: compiled.BaseConfig,
		Platform: platform, ContextDir: dir, Output: output, Progress: progress,
	}); err != nil {
		return err
	}
	return nil
}

func maybeBuildStagefileLLBToOCI(ctx context.Context, dir, dockerfile, platform, builder, tarPath, layoutDir string, progress io.Writer) (bool, error) {
	plan, ok, err := directStagefileLLBPlan(ctx, dir, dockerfile, builder)
	if err != nil || !ok {
		return ok, err
	}
	output := stagefilesolve.Output{OCILayoutPath: tarPath, OCILayoutDir: layoutDir}
	if err := solveStagefileLLB(ctx, dir, platform, builder, plan, output, progress); err != nil {
		return true, &imageBuildFailedError{err}
	}
	return true, nil
}

func maybeBuildStagefileLLBToDocker(ctx context.Context, dir, dockerfile, imageName, platform, builder string) (bool, error) {
	plan, ok, err := directStagefileLLBPlan(ctx, dir, dockerfile, builder)
	if err != nil || !ok {
		return ok, err
	}
	if builder == imageBuilderBuildkit {
		return true, fmt.Errorf("`wendy build --stagefile-backend=llb` needs Docker to load the completed image; use --builder=docker, or use `wendy run` for an on-device BuildKit build")
	}
	tmp, err := os.CreateTemp("", "wendy-stagefile-*.docker.tar")
	if err != nil {
		return true, err
	}
	tarPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tarPath)
		return true, err
	}
	_ = os.Remove(tarPath)
	defer os.Remove(tarPath)

	err = runBuildWithProgress(ctx, "Building Stagefile with direct LLB...", dumpRawAlways, func(buildCtx context.Context, stream, _ io.Writer) error {
		return solveStagefileLLB(buildCtx, dir, platform, builder, plan, stagefilesolve.Output{
			DockerTarPath: tarPath, ImageRef: imageName,
		}, stream)
	})
	if err != nil {
		return true, err
	}
	load := exec.CommandContext(ctx, "docker", "load", "--input", tarPath)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return true, fmt.Errorf("loading direct LLB image into Docker: %w", err)
	}
	cliSuccess("Build completed successfully.")
	return true, nil
}
