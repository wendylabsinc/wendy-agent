package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
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

// runBuildWithProgress runs build, rendering its buildx output as a clean live
// step list (interactive) or concise per-step lines (non-interactive). The raw
// buildx output is retained and printed if the build fails AND dumpRawOnFailure
// is true (but never on cancellation). Setup-log chatter written to logw is
// buffered and surfaced only on failure (when dumpRawOnFailure is true).
func runBuildWithProgress(ctx context.Context, title string, dumpRawOnFailure bool, build func(stream, logw io.Writer) error) error {
	start := time.Now()
	raw := &boundedBuffer{max: maxRawBuildCapture}
	var setupLog bytes.Buffer

	if !buildProgressInteractive() {
		emit, tally := tui.NewBuildPlainRenderer(buildProgressOut)
		parser := tui.NewBuildParser(emit)
		stream := io.MultiWriter(parser, raw)
		fmt.Fprintf(buildProgressOut, "%s\n", title)
		err := build(stream, &setupLog)
		if err != nil {
			if dumpRawOnFailure && ctx.Err() == nil {
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
	fm := final.(tui.BuildStepsModel)
	if cancelErr := fm.Err(); cancelErr == tui.ErrCancelled {
		return cancelErr
	}
	buildErr := <-buildErrC
	if buildErr != nil {
		if dumpRawOnFailure && ctx.Err() == nil {
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
