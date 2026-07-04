# Cleaner build progress for `wendy run` / `cloud run`

Date: 2026-06-27
Branch: `jo/improve-cli`
Status: Approved (design)

## Problem

`wendy run` / `wendy cloud run` pipes the raw `docker buildx` stream straight to
stdout. A single-service deploy produces dozens of lines of low-signal noise
(`#6 sha256:… 2.10MB / 14.36MB`, `[buildx] bootstrapping builder …`,
`exporting layers … pushing layers …`), and the user cannot quickly see which
build step is running, whether layers were cached, or how long the build took.

We want a quiet, live view that still surfaces the essentials:

- the current build step / phase (which Dockerfile layer),
- caching (which steps were cache hits vs rebuilt),
- timing (per-step and total build duration),

and the same information in the multi-service build view. We must not hide real
errors.

## Chosen presentation

A **live, collapsing step list** for single-service builds:

```
Building image for linux/amd64...
  ✓ load build definition          0.0s
  ✓ load metadata python:3.11      2.0s
  ⚡ [1/6] FROM python:3.11-slim   cached
  ⚡ [2/6] WORKDIR /app            cached
  ⚡ [3/6] COPY requirements.txt   cached
  ⠙ [4/6] RUN pip install ...      8s
  · [5/6] COPY app.py
  · [6/6] RUN useradd ...
  exporting + pushing layers
```

On success it collapses to a one-line summary:

```
✓ Built & pushed (4 cached, 2 rebuilt) in 21.3s
```

## Architecture

Three pieces, with a pure, testable parser at the core.

### 1. `buildprogress.Parser` — pure core

An `io.Writer` that the buildx `stdout`/`stderr` is piped into. The build is
invoked with `--progress=plain` so the format is deterministic. `plain` is
chosen over `rawjson` because it is universally supported (rawjson needs
buildx ≥ 0.13 and errors out on older clients), and it is already the format
buildx emits when its output is not a TTY — so we have a real sample to test
against.

The parser recognises the buildkit plain grammar:

- `#N <name>` → step started. The name carries the meaningful label:
  `[4/6] RUN …`, `[internal] load build definition`, `[1/6] FROM …`,
  `exporting to image`, etc.
- `#N CACHED` → step was a cache hit.
- `#N DONE 4.3s` → step finished, with duration.
- `#N ERROR …` and the trailing `------` … `------` error block → step failed.
- export / push vertices (`exporting layers`, `pushing layers`,
  `pushing manifest …`) → collapsed into a single "exporting + pushing" phase.

It maintains:

- ordered list of steps with status (pending / running / cached / done / failed)
  and duration,
- the currently-active step,
- cached vs rebuilt tallies,
- total elapsed time,
- a capped raw buffer (same idea as the existing `capturingWriter`) for failure
  replay and transient-push-error classification.

`Write([]byte)` parses newly-arrived lines and invokes an `onEvent(StepEvent)`
callback. No rendering, no terminal access → unit-testable as
`bytes-in → events-out`.

### 2. Single-service renderer — `BuildStepsModel` (Bubble Tea)

Mirrors the lifecycle of the existing `tui.MultiSpinnerModel`:

- the build runs in a goroutine, writing to the parser;
- the parser `prog.Send()`s step events to the Bubble Tea program;
- the main goroutine runs `prog.Run()`, which quits on an all-done message.

It renders the collapsing step list above (spinner on the active step, `✓` for
built, `⚡` for cached, `·` for pending), and on success replaces the block with
the one-line summary. On failure it prints the captured raw buildx log so the
underlying error (e.g. a failed `pip install`) is fully visible.

### 3. Multi-service adapter

The multi-service path already runs a `tui.MultiSpinnerModel` with a per-row
detail string and an **unused** `MultiSpinnerDetailMsg`. Each concurrent
service build gets its own `buildprogress.Parser`; its events map to:

- `MultiSpinnerDetailMsg{Name, Detail}` for the active step
  (e.g. `[4/6] RUN pip install`), and
- the cached/rebuilt tallies, surfaced in the done row:
  `✓ api  built (4 cached, 2 rebuilt) 21.3s`.

No change to the MultiSpinner lifecycle — we only start sending the detail
messages that the model already understands, plus extend the done message with
the tallies.

## Integration points

Least-invasive: wrap the existing `streamOutput` `io.Writer` at each call site.
A helper owns TUI setup/teardown and the summary line so call sites stay a
one-liner:

```go
err := buildprogress.RunSingle(ctx, title, interactive, func(w io.Writer) error {
    return buildAndPushImageForAgent(ctx, conn, regPort, builder, cwd, repo,
        platform, dockerfile, buildArgs, "", w, logSink)
})
```

Call sites:

- `deployByChunkDiff` → `buildImageToOCILayout` (run.go ~1878, OCI fast path)
- single-service registry push path (run.go ~1504)
- multi-service `buildServiceImage` (multibuild.go ~490) — via the adapter
- compose build path (compose.go)

## Fallbacks

- **Non-TTY / CI** (`!isInteractiveTerminal()`): no Bubble Tea program. The
  parser drives one concise line per completed step
  (`✓ [4/6] RUN … 8s` / `⚡ [1/6] FROM … cached`) so logs stay readable and
  greppable.
- **Failure**: the full raw buildx log is printed (never hidden), exactly as
  today, after the TUI tears down cleanly.
- **Quiet setup logs**: `[buildx] bootstrapping / inject / restart` chatter and
  the `Fast layer-diff deploy failed … falling back to registry push` /
  `Apple Container unavailable … falling back to Docker` messages route to a
  buffer flushed only on error. The fallback chain collapses to a single short
  status line rather than multi-line bootstrap noise. These already go to the
  separate `logOutput` writer, distinct from the build `streamOutput`, so this
  is a routing change, not a parsing one.
- **Apple Container builder** (different output format): best-effort. If the
  parser sees no recognisable buildx steps it falls back to streaming the raw
  output rather than mis-parsing.

No new CLI flag. Raw output is available via the existing `--verbose` flag
(watch mode) and is always shown on failure.

## Testing

- **Parser**: unit tests against the real `cloud run` log captured in the task
  as a fixture — cached path, full-rebuild path, failure path (`pip install`
  error), and export/push phase. Assert the emitted `StepEvent` sequence and the
  final tallies/durations.
- **Plain (non-TTY) renderer**: golden-output tests.
- **TUI models**: lightweight `Update`-message tests in the style of the
  existing `multispinner_test` / `spinner_test`.

## Out of scope

- Switching the build data source to `--progress=rawjson` (considered; rejected
  for compatibility).
- Reworking the device-side "Unpacking image" progress bar, which is already
  clean.
- Changing build concurrency, caching, or the push/retry logic.
