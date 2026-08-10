package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// buildProgressInteractive and buildProgressOut are indirection points so tests
// can force non-interactive rendering and capture output. In production they
// resolve to isInteractiveTerminal() and os.Stdout.
var (
	buildProgressInteractive           = func() bool { return isInteractiveTerminal() }
	buildProgressOut         io.Writer = os.Stdout
)

func forceBuildProgressInteractive(v bool) func() {
	prev := buildProgressInteractive
	buildProgressInteractive = func() bool { return v }
	return func() { buildProgressInteractive = prev }
}

func setBuildProgressOut(w io.Writer) func() {
	prev := buildProgressOut
	buildProgressOut = w
	return func() { buildProgressOut = prev }
}

// maxRawBuildCapture bounds the raw buildx log retained for failure replay.
const maxRawBuildCapture = 256 << 10

// buildxRawJSONMinor is the buildx minor version that introduced
// --progress=rawjson (v0.13.0).
const buildxRawJSONMinor = 13

var (
	buildxProgressModeOnce sync.Once
	buildxProgressModeVal  string
	buildxVersionRe        = regexp.MustCompile(`\bv(\d+)\.(\d+)\.`)
)

// buildxProgressMode picks the progress format to ask buildx for.
//
// rawjson (buildx >= 0.13) reports exact per-vertex byte counters and identifies
// vertices by digest, which is what makes download rate and percentage honest
// rather than re-parsed from formatted text. plain is the fallback, and is all
// the Apple Container CLI speaks. tui.BuildParser accepts either format and
// detects it per line, so a wrong guess here costs detail, not correctness.
//
// WENDY_BUILD_PROGRESS overrides the probe for debugging.
func buildxProgressMode(ctx context.Context) string {
	buildxProgressModeOnce.Do(func() {
		buildxProgressModeVal = detectBuildxProgressMode(ctx)
	})
	return buildxProgressModeVal
}

func detectBuildxProgressMode(ctx context.Context) string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WENDY_BUILD_PROGRESS"))) {
	case "plain":
		return "plain"
	case "rawjson":
		return "rawjson"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "docker", "buildx", "version").Output()
	if err != nil {
		return "plain"
	}
	m := buildxVersionRe.FindSubmatch(out)
	if m == nil {
		return "plain"
	}
	major, err1 := strconv.Atoi(string(m[1]))
	minor, err2 := strconv.Atoi(string(m[2]))
	if err1 != nil || err2 != nil {
		return "plain"
	}
	if major > 0 || minor >= buildxRawJSONMinor {
		return "rawjson"
	}
	return "plain"
}

// runAppleContainerBuildWithProgress builds a local image with the Apple
// Container CLI, rendering its --progress=plain output through the shared build
// progress UI (the same renderer used by the buildx/buildctl paths).
func runAppleContainerBuildWithProgress(ctx context.Context, dir, imageName, platform, dockerfile string) error {
	title := fmt.Sprintf("Building Apple Container image %s for %s...", tui.Value(imageName), tui.Value(platform))
	return runBuildWithProgress(ctx, title, dumpRawAlways, func(stream, logw io.Writer) error {
		return buildImageWithAppleContainer(ctx, dir, imageName, platform, dockerfile, nil, stream, logw)
	})
}

// dumpRawAlways is the dumpRawOnFailure predicate for build paths whose
// failures are always surfaced to the user (no fallback rebuild follows).
func dumpRawAlways(error) bool { return true }

// dumpRawUnlessRegistryUnavailable suppresses the raw buildx replay when the
// failure was converted to the friendly "no registry on the Mac agent" error —
// the retried-EOF spam would bury the actionable message.
func dumpRawUnlessRegistryUnavailable(err error) bool { return !isRegistryUnavailable(err) }

// runBuildWithProgress runs build, rendering its buildx output as a clean live
// step list (interactive) or concise per-step lines (non-interactive). The raw
// buildx output is retained and printed if the build fails AND
// dumpRawOnFailure(err) is true (but never on cancellation). Setup-log chatter
// written to logw is buffered and surfaced under the same condition. Callers
// whose failures can trigger a fallback rebuild (which would replay the same
// output) use the predicate to stay quiet in exactly those cases.
func runBuildWithProgress(ctx context.Context, title string, dumpRawOnFailure func(error) bool, build func(stream, logw io.Writer) error) error {
	start := time.Now()
	raw := &boundedBuffer{max: maxRawBuildCapture}
	var setupLog bytes.Buffer

	if !buildProgressInteractive() {
		emit, tally, stopHeartbeat := tui.NewBuildPlainRenderer(buildProgressOut)
		parser := tui.NewBuildParser(emit)
		stream := io.MultiWriter(parser, raw)
		fmt.Fprintf(buildProgressOut, "%s\n", title)
		err := build(stream, &setupLog)
		stopHeartbeat()
		if err != nil {
			if ctx.Err() == nil && dumpRawOnFailure(err) {
				buildProgressOut.Write(raw.Bytes())
				buildProgressOut.Write(setupLog.Bytes())
			}
			return err
		}
		printBuildSummary(buildProgressOut, tally(), time.Since(start))
		return nil
	}

	// Interactive: run the steps model while the build streams events to it.
	m := tui.NewBuildStepsModel(title)
	prog := tui.NewProgressProgram(m)
	parser := tui.NewBuildParser(func(e tui.BuildStepEvent) {
		prog.Send(tui.BuildStepMsg(e))
	})
	stream := io.MultiWriter(parser, raw)

	buildErrC := make(chan error, 1)
	go func() {
		err := build(stream, &setupLog)
		prog.Send(tui.BuildAllDoneMsg{Err: err})
		buildErrC <- err
	}()

	final, runErr := prog.Run()
	if runErr != nil {
		return fmt.Errorf("build progress UI: %w", runErr)
	}
	fm, ok := final.(tui.BuildStepsModel)
	if !ok {
		return fmt.Errorf("build progress UI: unexpected final model %T", final)
	}
	if cancelErr := fm.Err(); cancelErr == tui.ErrCancelled {
		return cancelErr
	}
	buildErr := <-buildErrC
	if buildErr != nil {
		if ctx.Err() == nil && dumpRawOnFailure(buildErr) {
			buildProgressOut.Write(raw.Bytes())
			buildProgressOut.Write(setupLog.Bytes())
		}
		return buildErr
	}
	elapsed := time.Since(start)
	printBuildSummary(buildProgressOut, fm.Tally(), elapsed)
	maybeSuggestOptimizeAfterBuild(fm.Tally(), elapsed)
	return nil
}

func printBuildSummary(w io.Writer, t tui.BuildTally, d time.Duration) {
	fmt.Fprintf(w, "✓ Built & pushed (%d cached, %d rebuilt) in %s\n",
		t.Cached, t.Rebuilt, d.Round(time.Millisecond))
}

// boundedBuffer keeps only the last max bytes written to it.
type boundedBuffer struct {
	max int
	buf []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf }
