# Cleaner Build Progress View — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the raw `docker buildx` dump in `wendy run` / `wendy cloud run` with a live, collapsing step list (current step, cache hits, per-step + total timings) in both single- and multi-service build views, keeping raw output on failure and a concise non-TTY fallback.

**Architecture:** A pure `tui.BuildParser` consumes buildx `--progress=plain` output and emits `BuildStepEvent`s. A single-service Bubble Tea model (`tui.BuildStepsModel`) renders the live list; a plain renderer handles non-TTY. A `commands` helper (`runBuildWithProgress`) wires the parser to the right renderer, owns raw-on-failure replay, and quiets setup-log chatter. The multi-service path feeds the parser's events into the existing `tui.MultiSpinnerModel` (its `MultiSpinnerDetailMsg` is currently unused).

**Tech Stack:** Go, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/bubbles` (already in use). Test framework: standard `testing`.

## Global Constraints

- All new code lives under `go/internal/cli/`. Import direction is `commands → tui` only; `tui` must NOT import `commands` (avoid an import cycle).
- Build the buildx command with `--progress=plain` so the parsed format is deterministic (it is universally supported, unlike `rawjson`).
- No new CLI flag. Raw output appears on build failure and via the existing `--verbose` (watch) path.
- Interactive vs non-interactive is decided by the existing `isInteractiveTerminal()` (`go/internal/cli/commands/helpers.go:514`).
- Theme colors come from `go/internal/cli/tui/theme.go`: `ColorPrimary` (titles/spinner), `ColorAccent` (success/✓), `ColorDim` (muted), `ColorError` (✗). Cache hits use `ColorPrimary` with a `⚡` glyph.
- Match existing TUI model patterns (`tui.MultiSpinnerModel`, `tui.SpinnerModel`): `tea.Model` with `Init/Update/View`, `ctrl+c` sets `ErrCancelled` and quits.
- Run `gofmt`/`goimports` before every commit. Every task ends green: `go build ./internal/cli/...` and `go test ./internal/cli/...` from the `go/` directory.

---

### Task 1: `BuildParser` — pure buildx-plain parser

**Files:**
- Create: `go/internal/cli/tui/buildparser.go`
- Test: `go/internal/cli/tui/buildparser_test.go`

**Interfaces:**
- Consumes: nothing (pure).
- Produces:
  - `type BuildVertexKind int` with `BuildVertexHidden, BuildVertexSetup, BuildVertexStep, BuildVertexExport`.
  - `type BuildStepStatus int` with `BuildStepRunning, BuildStepCached, BuildStepDone, BuildStepFailed`.
  - `const BuildExportVertexID = -1`.
  - `type BuildStepEvent struct { ID int; Kind BuildVertexKind; Display string; Status BuildStepStatus; Dur time.Duration }`.
  - `func NewBuildParser(emit func(BuildStepEvent)) *BuildParser`.
  - `(*BuildParser).Write([]byte) (int, error)` — implements `io.Writer`; always returns `len(p), nil`.
  - `func classifyBuildVertex(name string) (BuildVertexKind, string)`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/tui/buildparser_test.go`:

```go
package tui

import (
	"testing"
	"time"
)

// collect runs the given plain-progress text through the parser and returns the
// emitted events.
func collect(t *testing.T, text string) []BuildStepEvent {
	t.Helper()
	var got []BuildStepEvent
	p := NewBuildParser(func(e BuildStepEvent) { got = append(got, e) })
	if _, err := p.Write([]byte(text)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return got
}

func TestParserEmitsStepStartAndDoneWithDuration(t *testing.T) {
	text := "#9 [4/6] RUN pip install -r requirements.txt\n" +
		"#9 1.563 Collecting debugpy\n" +
		"#9 DONE 4.3s\n"
	got := collect(t, text)
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Status != BuildStepRunning || got[0].Kind != BuildVertexStep ||
		got[0].Display != "[4/6] RUN pip install -r requirements.txt" {
		t.Errorf("start event wrong: %+v", got[0])
	}
	if got[1].Status != BuildStepDone || got[1].Dur != 4300*time.Millisecond {
		t.Errorf("done event wrong: %+v (dur=%v)", got[1], got[1].Dur)
	}
}

func TestParserMarksCachedStep(t *testing.T) {
	text := "#6 [1/6] FROM docker.io/library/python:3.11-slim\n#6 CACHED\n"
	got := collect(t, text)
	if len(got) != 2 || got[1].Status != BuildStepCached {
		t.Fatalf("want a cached event, got %+v", got)
	}
}

func TestParserHidesInternalNoise(t *testing.T) {
	// .dockerignore / build context / cache manifest / resolve are noise.
	text := "#3 [internal] load .dockerignore\n#3 DONE 0.0s\n" +
		"#4 importing cache manifest from local:123\n#4 DONE 0.0s\n" +
		"#5 [internal] load build context\n#5 DONE 0.0s\n"
	if got := collect(t, text); len(got) != 0 {
		t.Fatalf("want no events for internal noise, got %+v", got)
	}
}

func TestParserShowsSetupSteps(t *testing.T) {
	text := "#1 [internal] load build definition from Dockerfile\n#1 DONE 0.0s\n" +
		"#2 [internal] load metadata for docker.io/library/python:3.11-slim\n#2 DONE 2.0s\n"
	got := collect(t, text)
	if len(got) != 4 {
		t.Fatalf("want 4 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != BuildVertexSetup || got[0].Display != "load build definition" {
		t.Errorf("event 0 wrong: %+v", got[0])
	}
	if got[2].Display != "load metadata" {
		t.Errorf("event 2 wrong: %+v", got[2])
	}
}

func TestParserCollapsesExportAndPushIntoOnePhase(t *testing.T) {
	text := "#12 exporting to image\n#12 exporting layers\n#12 DONE 0.5s\n" +
		"#13 exporting cache to client directory\n#13 DONE 0.3s\n" +
		"#12 pushing layers\n#12 DONE 12.6s\n"
	got := collect(t, text)
	// Exactly one Running export event, no per-vertex done/cached events.
	if len(got) != 1 {
		t.Fatalf("want 1 collapsed export event, got %d: %+v", len(got), got)
	}
	if got[0].ID != BuildExportVertexID || got[0].Kind != BuildVertexExport ||
		got[0].Status != BuildStepRunning || got[0].Display != "exporting + pushing layers" {
		t.Errorf("export event wrong: %+v", got[0])
	}
}

func TestParserMarksFailedStep(t *testing.T) {
	text := "#9 [4/6] RUN pip install -r requirements.txt\n" +
		"#9 ERROR: process \"/bin/sh -c pip install\" did not complete successfully\n"
	got := collect(t, text)
	if len(got) != 2 || got[1].Status != BuildStepFailed {
		t.Fatalf("want a failed event, got %+v", got)
	}
}

func TestParserHandlesSplitWrites(t *testing.T) {
	var got []BuildStepEvent
	p := NewBuildParser(func(e BuildStepEvent) { got = append(got, e) })
	p.Write([]byte("#9 [4/6] RUN pip insta"))
	p.Write([]byte("ll\n#9 DON"))
	p.Write([]byte("E 4.3s\n"))
	if len(got) != 2 || got[0].Status != BuildStepRunning || got[1].Status != BuildStepDone {
		t.Fatalf("split writes mis-parsed: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/tui/ -run TestParser -v`
Expected: FAIL — `undefined: NewBuildParser` (and the other new symbols).

- [ ] **Step 3: Write the implementation**

Create `go/internal/cli/tui/buildparser.go`:

```go
package tui

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BuildVertexKind classifies a buildkit vertex for display.
type BuildVertexKind int

const (
	// BuildVertexHidden is internal noise we never surface (load .dockerignore,
	// load build context, importing cache manifest, image resolve).
	BuildVertexHidden BuildVertexKind = iota
	// BuildVertexSetup is a small set of setup vertices worth showing
	// (load build definition, load metadata).
	BuildVertexSetup
	// BuildVertexStep is a numbered Dockerfile step ("[4/6] RUN ...").
	BuildVertexStep
	// BuildVertexExport is any exporting/pushing vertex; all of them collapse
	// into one synthetic "exporting + pushing layers" phase.
	BuildVertexExport
)

// BuildStepStatus is the lifecycle state of a displayed step.
type BuildStepStatus int

const (
	BuildStepRunning BuildStepStatus = iota
	BuildStepCached
	BuildStepDone
	BuildStepFailed
)

// BuildExportVertexID is the synthetic vertex ID for the collapsed
// exporting/pushing phase.
const BuildExportVertexID = -1

// BuildStepEvent is emitted by BuildParser for each meaningful transition.
type BuildStepEvent struct {
	ID      int
	Kind    BuildVertexKind
	Display string
	Status  BuildStepStatus
	Dur     time.Duration // set when Status == BuildStepDone
}

var (
	buildVertexLineRe = regexp.MustCompile(`^#(\d+) (.*)$`)
	buildDoneRe       = regexp.MustCompile(`^DONE (\d+(?:\.\d+)?)s$`)
	buildLogLineRe    = regexp.MustCompile(`^\d+\.\d+ `)        // "1.563 Collecting ..."
	buildStepLabelRe  = regexp.MustCompile(`^\[[^\]]*\d+/\d+\] `) // "[4/6] ", "[stage 2/3] "
)

type buildVertexState struct {
	named   bool
	kind    BuildVertexKind
	display string
}

// BuildParser consumes `docker buildx --progress=plain` output and calls Emit
// for each meaningful step transition. It implements io.Writer and is intended
// for sequential writes from a single goroutine (os/exec's output copier).
type BuildParser struct {
	emit          func(BuildStepEvent)
	line          []byte
	vertex        map[int]*buildVertexState
	exportStarted bool
}

// NewBuildParser returns a parser that calls emit for each event. emit must be
// safe to call from the goroutine that writes to the parser.
func NewBuildParser(emit func(BuildStepEvent)) *BuildParser {
	return &BuildParser{emit: emit, vertex: map[int]*buildVertexState{}}
}

// Write implements io.Writer. It buffers partial lines and parses complete ones.
func (p *BuildParser) Write(b []byte) (int, error) {
	p.line = append(p.line, b...)
	for {
		i := bytes.IndexByte(p.line, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(p.line[:i]), "\r")
		p.line = p.line[i+1:]
		p.parseLine(line)
	}
	return len(b), nil
}

func (p *BuildParser) parseLine(line string) {
	m := buildVertexLineRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	id, _ := strconv.Atoi(m[1])
	rest := m[2]

	v := p.vertex[id]
	if v == nil {
		v = &buildVertexState{}
		p.vertex[id] = v
	}

	switch {
	case rest == "CACHED":
		p.emitStatus(id, v, BuildStepCached, 0)
		return
	case strings.HasPrefix(rest, "ERROR"):
		p.emitStatus(id, v, BuildStepFailed, 0)
		return
	case buildLogLineRe.MatchString(rest):
		return // build log line, ignore
	}
	if dm := buildDoneRe.FindStringSubmatch(rest); dm != nil {
		secs, _ := strconv.ParseFloat(dm[1], 64)
		p.emitStatus(id, v, BuildStepDone, time.Duration(secs*float64(time.Second)))
		return
	}

	// A non-status line. If the vertex is already named, this is a sub-status
	// ("resolve ... done", "sha256:... done", "extracting ...") — ignore it.
	if v.named {
		return
	}
	// First occurrence of this vertex: its remainder is the vertex name.
	v.named = true
	v.kind, v.display = classifyBuildVertex(rest)
	switch v.kind {
	case BuildVertexHidden:
		return
	case BuildVertexExport:
		if p.exportStarted {
			return
		}
		p.exportStarted = true
		p.emit(BuildStepEvent{
			ID: BuildExportVertexID, Kind: BuildVertexExport,
			Display: "exporting + pushing layers", Status: BuildStepRunning,
		})
	default:
		p.emit(BuildStepEvent{ID: id, Kind: v.kind, Display: v.display, Status: BuildStepRunning})
	}
}

// emitStatus emits a terminal status (cached/done/failed) for a vertex, applying
// the export-collapse rule: individual export vertices never flip the collapsed
// phase to done/cached (the caller marks the whole build done), but a failure is
// still surfaced.
func (p *BuildParser) emitStatus(id int, v *buildVertexState, status BuildStepStatus, dur time.Duration) {
	if !v.named || v.kind == BuildVertexHidden {
		return
	}
	if v.kind == BuildVertexExport {
		if status == BuildStepFailed {
			p.emit(BuildStepEvent{ID: BuildExportVertexID, Kind: BuildVertexExport,
				Display: "exporting + pushing layers", Status: BuildStepFailed})
		}
		return
	}
	p.emit(BuildStepEvent{ID: id, Kind: v.kind, Display: v.display, Status: status, Dur: dur})
}

// classifyBuildVertex maps a buildkit vertex name to a display kind and a cleaned
// label. Unknown internal vertices are hidden to keep the view uncluttered.
func classifyBuildVertex(name string) (BuildVertexKind, string) {
	switch {
	case strings.HasPrefix(name, "[internal] load metadata"):
		return BuildVertexSetup, "load metadata"
	case strings.HasPrefix(name, "[internal] load build definition"):
		return BuildVertexSetup, "load build definition"
	case strings.HasPrefix(name, "exporting"), strings.HasPrefix(name, "pushing"):
		return BuildVertexExport, name
	}
	if buildStepLabelRe.MatchString(name) {
		return BuildVertexStep, name
	}
	return BuildVertexHidden, name
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/tui/ -run TestParser -v`
Expected: PASS (all `TestParser*` green).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/tui/buildparser.go internal/cli/tui/buildparser_test.go
git add internal/cli/tui/buildparser.go internal/cli/tui/buildparser_test.go
git commit -m "feat(cli): add buildx plain-progress parser"
```

---

### Task 2: Plain (non-TTY) renderer

**Files:**
- Create: `go/internal/cli/tui/buildplain.go`
- Test: `go/internal/cli/tui/buildplain_test.go`

**Interfaces:**
- Consumes: `BuildStepEvent`, `BuildStepStatus`, `BuildVertexKind` (Task 1).
- Produces:
  - `type BuildTally struct { Cached int; Rebuilt int }`.
  - `func NewBuildPlainRenderer(w io.Writer) (emit func(BuildStepEvent), tally func() BuildTally)` — returns an emit callback to hand to `NewBuildParser`, and a `tally` accessor for the final summary. Writes one concise line per completed step.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/tui/buildplain_test.go`:

```go
package tui

import (
	"strings"
	"testing"
	"time"
)

func TestPlainRendererWritesLinePerCompletedStep(t *testing.T) {
	var sb strings.Builder
	emit, tally := NewBuildPlainRenderer(&sb)
	emit(BuildStepEvent{ID: 1, Kind: BuildVertexSetup, Display: "load metadata", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 1, Kind: BuildVertexSetup, Display: "load metadata", Status: BuildStepDone, Dur: 2 * time.Second})
	emit(BuildStepEvent{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM python", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM python", Status: BuildStepCached})
	emit(BuildStepEvent{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepDone, Dur: 4300 * time.Millisecond})

	out := sb.String()
	// Running events produce no line; only terminal states do.
	for _, want := range []string{
		"load metadata", "2.0s",
		"[1/6] FROM python", "cached",
		"[4/6] RUN pip install", "4.3s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing %q\n%s", want, out)
		}
	}
	if got := tally(); got.Cached != 1 || got.Rebuilt != 1 {
		t.Errorf("tally = %+v, want {Cached:1 Rebuilt:1}", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/tui/ -run TestPlainRenderer -v`
Expected: FAIL — `undefined: NewBuildPlainRenderer`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/cli/tui/buildplain.go`:

```go
package tui

import (
	"fmt"
	"io"
	"time"
)

// BuildTally counts cached vs rebuilt Dockerfile steps for the summary line.
type BuildTally struct {
	Cached  int
	Rebuilt int
}

// NewBuildPlainRenderer returns an emit callback (to pass to NewBuildParser) that
// writes one concise line per completed step to w, plus a tally accessor for the
// final summary. It is the non-interactive (CI / piped) renderer.
func NewBuildPlainRenderer(w io.Writer) (func(BuildStepEvent), func() BuildTally) {
	var t BuildTally
	emit := func(e BuildStepEvent) {
		switch e.Status {
		case BuildStepCached:
			if e.Kind == BuildVertexStep {
				t.Cached++
			}
			fmt.Fprintf(w, "  cached  %s\n", e.Display)
		case BuildStepDone:
			if e.Kind == BuildVertexStep {
				t.Rebuilt++
			}
			fmt.Fprintf(w, "  done    %s  %s\n", e.Display, e.Dur.Round(time.Millisecond))
		case BuildStepFailed:
			fmt.Fprintf(w, "  FAILED  %s\n", e.Display)
		}
		// BuildStepRunning is intentionally silent in plain mode.
	}
	tally := func() BuildTally { return t }
	return emit, tally
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/tui/ -run TestPlainRenderer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/tui/buildplain.go internal/cli/tui/buildplain_test.go
git add internal/cli/tui/buildplain.go internal/cli/tui/buildplain_test.go
git commit -m "feat(cli): add plain (non-TTY) build progress renderer"
```

---

### Task 3: `BuildStepsModel` — single-service Bubble Tea model

**Files:**
- Create: `go/internal/cli/tui/buildsteps.go`
- Test: `go/internal/cli/tui/buildsteps_test.go`

**Interfaces:**
- Consumes: `BuildStepEvent`, statuses/kinds (Task 1), `BuildTally` (Task 2), theme colors, `hintRotator` (existing in `tui`).
- Produces:
  - `type BuildStepMsg BuildStepEvent` — wraps a parser event as a tea message.
  - `type BuildAllDoneMsg struct { Err error }`.
  - `func NewBuildStepsModel(title string) BuildStepsModel`.
  - `BuildStepsModel` implements `tea.Model` (`Init/Update/View`).
  - `(BuildStepsModel) Err() error`.
  - `(BuildStepsModel) Tally() BuildTally`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/tui/buildsteps_test.go`:

```go
package tui

import (
	"testing"
	"time"
)

func applyBuild(m BuildStepsModel, msgs ...interface{}) BuildStepsModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(BuildStepsModel)
	}
	return m
}

func TestBuildStepsModelTracksTally(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	m = applyBuild(m,
		BuildStepMsg{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM", Status: BuildStepRunning},
		BuildStepMsg{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM", Status: BuildStepCached},
		BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN", Status: BuildStepRunning},
		BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN", Status: BuildStepDone, Dur: time.Second},
	)
	if got := m.Tally(); got.Cached != 1 || got.Rebuilt != 1 {
		t.Fatalf("tally = %+v, want {1 1}", got)
	}
}

func TestBuildStepsModelViewShowsActiveStep(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	m = applyBuild(m, BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepRunning})
	if v := m.View(); !contains(v, "[4/6] RUN pip install") {
		t.Fatalf("view missing active step:\n%s", v)
	}
}

func TestBuildStepsModelAllDoneQuitsAndKeepsErr(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	wantErr := errForTest("boom")
	next, cmd := m.Update(BuildAllDoneMsg{Err: wantErr})
	m = next.(BuildStepsModel)
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if m.Err() != wantErr {
		t.Fatalf("Err() = %v, want %v", m.Err(), wantErr)
	}
}

func TestBuildStepsModelCtrlCCancels(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	next, _ := m.Update(keyMsg("ctrl+c"))
	m = next.(BuildStepsModel)
	if m.Err() != ErrCancelled {
		t.Fatalf("Err() = %v, want ErrCancelled", m.Err())
	}
}
```

NOTE: this test reuses small helpers. If `contains`, `errForTest`, and `keyMsg` are not already defined in the `tui` test package, add them at the top of this file:

```go
import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }
type tErr string
func (e tErr) Error() string { return string(e) }
func errForTest(s string) error { return tErr(s) }
func keyMsg(s string) tea.KeyMsg {
	// minimal: only ctrl+c is asserted; map it explicitly.
	if s == "ctrl+c" {
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
```

(If the helpers already exist in another `_test.go` in the package, delete the duplicates to avoid "redeclared" errors.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/tui/ -run TestBuildStepsModel -v`
Expected: FAIL — `undefined: NewBuildStepsModel`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/cli/tui/buildsteps.go`:

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BuildStepMsg delivers a parser event to the model.
type BuildStepMsg BuildStepEvent

// BuildAllDoneMsg signals the build finished (Err nil on success).
type BuildAllDoneMsg struct{ Err error }

type buildRow struct {
	id      int
	kind    BuildVertexKind
	display string
	status  BuildStepStatus
	dur     time.Duration
}

// BuildStepsModel renders a live, collapsing list of buildx steps for a single
// service build.
type BuildStepsModel struct {
	title   string
	rows    []buildRow
	byID    map[int]int
	spinner spinner.Model
	hints   hintRotator
	tally   BuildTally
	done    bool
	err     error
}

// NewBuildStepsModel returns a model with the given title (e.g. the
// "Building image for linux/amd64..." line).
func NewBuildStepsModel(title string) BuildStepsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorPrimary)
	return BuildStepsModel{
		title:   title,
		byID:    map[int]int{},
		spinner: s,
		hints:   newHintRotator(),
	}
}

// Init implements tea.Model.
func (m BuildStepsModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.hints.tick())
}

// Update implements tea.Model.
func (m BuildStepsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			m.err = ErrCancelled
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case hintTickMsg:
		m.hints.next()
		return m, m.hints.tick()
	case BuildStepMsg:
		m.applyEvent(BuildStepEvent(msg))
	case BuildAllDoneMsg:
		m.done = true
		m.err = msg.Err
		return m, tea.Quit
	}
	return m, nil
}

func (m *BuildStepsModel) applyEvent(e BuildStepEvent) {
	i, ok := m.byID[e.ID]
	if !ok {
		m.rows = append(m.rows, buildRow{id: e.ID, kind: e.Kind, display: e.Display, status: e.Status})
		m.byID[e.ID] = len(m.rows) - 1
		return
	}
	m.rows[i].status = e.Status
	m.rows[i].dur = e.Dur
	switch e.Status {
	case BuildStepCached:
		if e.Kind == BuildVertexStep {
			m.tally.Cached++
		}
	case BuildStepDone:
		if e.Kind == BuildVertexStep {
			m.tally.Rebuilt++
		}
	}
}

var (
	bsCheck = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	bsCache = lipgloss.NewStyle().Foreground(ColorPrimary)
	bsCross = lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	bsDim   = lipgloss.NewStyle().Foreground(ColorDim)
	bsTitle = lipgloss.NewStyle().Foreground(ColorPrimary)
)

const buildStepLabelWidth = 34

// View implements tea.Model.
func (m BuildStepsModel) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s\n", m.spinner.View(), bsTitle.Render(m.title)))
	for _, r := range m.rows {
		label := truncateLabel(r.display, buildStepLabelWidth)
		switch r.status {
		case BuildStepRunning:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", m.spinner.View(), label, bsDim.Render(elapsedNote(r))))
		case BuildStepCached:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", bsCache.Render("⚡"), label, bsDim.Render("cached")))
		case BuildStepDone:
			sb.WriteString(fmt.Sprintf("  %s %s %s\n", bsCheck.Render("✓"), label, bsDim.Render(r.dur.Round(time.Millisecond).String())))
		case BuildStepFailed:
			sb.WriteString(fmt.Sprintf("  %s %s\n", bsCross.Render("✗"), label))
		}
	}
	if hint := m.hints.view(); hint != "" {
		sb.WriteString(hint)
		sb.WriteString("\n")
	}
	return sb.String()
}

func elapsedNote(r buildRow) string {
	return "" // running steps show only the spinner; duration is shown on DONE.
}

func truncateLabel(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// Err returns the terminal error (ErrCancelled on ctrl+c, the build error from
// BuildAllDoneMsg, or nil).
func (m BuildStepsModel) Err() error { return m.err }

// Tally returns the cached/rebuilt counts accumulated from step events.
func (m BuildStepsModel) Tally() BuildTally { return m.tally }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/tui/ -run TestBuildStepsModel -v`
Expected: PASS. Then run the whole package: `go test ./internal/cli/tui/` → PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/tui/buildsteps.go internal/cli/tui/buildsteps_test.go
git add internal/cli/tui/buildsteps.go internal/cli/tui/buildsteps_test.go
git commit -m "feat(cli): add single-service build steps TUI model"
```

---

### Task 4: Pass `--progress=plain` to both buildx invocations

**Files:**
- Modify: `go/internal/cli/commands/docker.go:1428-1432` (registry-push args in `buildAndPushImage`)
- Modify: `go/internal/cli/commands/ocilayers.go:498-502` (OCI-export args in `buildImageToOCILayout`)
- Test: `go/internal/cli/commands/buildxcache_test.go` (add an assertion)

**Interfaces:**
- Consumes: nothing new.
- Produces: deterministic plain output for the parser. No signature changes.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/cli/commands/buildxcache_test.go` (append a new test; reuse the package's existing helpers/style — if the file builds args via a helper, assert on that; otherwise this standalone string check works):

```go
func TestBuildxArgsRequestPlainProgress(t *testing.T) {
	// Both buildx arg builders must request --progress=plain so the CLI can
	// parse a deterministic format. Guard against accidental removal.
	for _, f := range []string{"docker.go", "ocilayers.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(src), `"--progress", "plain"`) {
			t.Errorf("%s: expected buildx args to include --progress plain", f)
		}
	}
}
```

(Ensure the test file imports `os` and `strings`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildxArgsRequestPlainProgress -v`
Expected: FAIL — neither file contains the flag yet.

- [ ] **Step 3: Write the implementation**

In `go/internal/cli/commands/docker.go`, change the args initializer in `buildAndPushImage`:

```go
	args := []string{
		"buildx", "build",
		"--builder", builder,
		"--platform", platform,
		"--progress", "plain",
	}
```

In `go/internal/cli/commands/ocilayers.go`, change the args initializer in `buildImageToOCILayout`:

```go
	args := []string{
		"buildx", "build",
		"--builder", buildxBuilder,
		"--platform", platform,
		"--progress", "plain",
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildxArgsRequestPlainProgress -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/docker.go internal/cli/commands/ocilayers.go internal/cli/commands/buildxcache_test.go
git add internal/cli/commands/docker.go internal/cli/commands/ocilayers.go internal/cli/commands/buildxcache_test.go
git commit -m "feat(cli): request --progress=plain for parseable buildx output"
```

---

### Task 5: `runBuildWithProgress` helper (single-service orchestration)

**Files:**
- Create: `go/internal/cli/commands/buildprogress.go`
- Test: `go/internal/cli/commands/buildprogress_test.go`

**Interfaces:**
- Consumes: `tui.NewBuildParser`, `tui.NewBuildPlainRenderer`, `tui.NewBuildStepsModel`, `tui.BuildStepMsg`, `tui.BuildAllDoneMsg`, `tui.BuildTally` (Tasks 1-3); `isInteractiveTerminal()`; `cliLogln`.
- Produces:
  - `func runBuildWithProgress(ctx context.Context, title string, build func(stream, logw io.Writer) error) error`.
    - `build` is the existing build call, given the stream writer (buildx stdout/stderr) and a log writer (setup-log chatter).
    - Interactive: runs a `tui.BuildStepsModel` program; build runs in a goroutine writing to a `tui.BuildParser`; raw bytes also captured to a capped buffer. On success prints the one-line summary `✓ Built & pushed (N cached, M rebuilt) in D`. On failure prints the captured raw log + setup log.
    - Non-interactive: parser drives `tui.NewBuildPlainRenderer`; setup log streams to stderr; on failure the raw buffer is printed.
    - Cancellation (`ctx.Err() != nil`): no raw dump (matches existing watch behavior).

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/buildprogress_test.go`. We test the non-interactive path (deterministic, no TTY) and the failure path. Drive interactivity through a package-level hook so the test does not depend on a real terminal:

```go
package commands

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunBuildWithProgressPlainSuccess(t *testing.T) {
	// Force non-interactive rendering and capture stdout via the package sink.
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	err := runBuildWithProgress(context.Background(), "Building image...", func(stream, logw io.Writer) error {
		io.WriteString(stream, "#9 [4/6] RUN pip install\n#9 DONE 4.3s\n")
		io.WriteString(stream, "#6 [1/6] FROM python\n#6 CACHED\n")
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "cached") || !strings.Contains(got, "4.3s") {
		t.Errorf("missing step lines:\n%s", got)
	}
	if !strings.Contains(got, "1 cached") || !strings.Contains(got, "1 rebuilt") {
		t.Errorf("missing summary tally:\n%s", got)
	}
}

func TestRunBuildWithProgressPrintsRawOnFailure(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	wantErr := errors.New("docker buildx build failed")
	err := runBuildWithProgress(context.Background(), "Building image...", func(stream, logw io.Writer) error {
		io.WriteString(stream, "#9 [4/6] RUN pip install\n")
		io.WriteString(stream, "#9 12.34 ERROR: could not find a version\n")
		io.WriteString(logw, "[buildx] bootstrapping builder\n")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got := out.String()
	// Raw build output AND setup log are surfaced on failure.
	if !strings.Contains(got, "could not find a version") {
		t.Errorf("raw build output not surfaced:\n%s", got)
	}
	if !strings.Contains(got, "bootstrapping builder") {
		t.Errorf("setup log not surfaced on failure:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestRunBuildWithProgress -v`
Expected: FAIL — `undefined: runBuildWithProgress` / `forceBuildProgressInteractive` / `setBuildProgressOut`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/cli/commands/buildprogress.go`:

```go
package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// buildProgressInteractive and buildProgressOut are indirection points so tests
// can force non-interactive rendering and capture output. In production they
// resolve to isInteractiveTerminal() and os.Stdout.
var (
	buildProgressInteractive = func() bool { return isInteractiveTerminal() }
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
// buildx output is retained and printed if the build fails (but not on
// cancellation). Setup-log chatter written to logw is buffered and surfaced only
// on failure.
func runBuildWithProgress(ctx context.Context, title string, build func(stream, logw io.Writer) error) error {
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
			if ctx.Err() == nil {
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
	prog := tea.NewProgram(m)
	parser := tui.NewBuildParser(func(e tui.BuildStepEvent) {
		prog.Send(tui.BuildStepMsg(e))
	})
	stream := io.MultiWriter(parser, raw)

	var buildErr error
	go func() {
		buildErr = build(stream, &setupLog)
		prog.Send(tui.BuildAllDoneMsg{Err: buildErr})
	}()

	final, runErr := prog.Run()
	if runErr != nil {
		return fmt.Errorf("build progress UI: %w", runErr)
	}
	fm := final.(tui.BuildStepsModel)
	if cancelErr := fm.Err(); cancelErr == tui.ErrCancelled {
		return cancelErr
	}
	if buildErr != nil {
		if ctx.Err() == nil {
			buildProgressOut.Write(raw.Bytes())
			buildProgressOut.Write(setupLog.Bytes())
		}
		return buildErr
	}
	printBuildSummary(buildProgressOut, fm.Tally(), time.Since(start))
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
```

NOTE on the summary wording: the OCI fast-path does not "push" to a registry; if you want path-accurate wording, pass a verb into `runBuildWithProgress`. For v1 keep "Built & pushed" — both paths ultimately land the image on the device, and the spec's mockup uses this text. Adjust later if it reads wrong on the OCI path.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestRunBuildWithProgress -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/buildprogress.go internal/cli/commands/buildprogress_test.go
git add internal/cli/commands/buildprogress.go internal/cli/commands/buildprogress_test.go
git commit -m "feat(cli): add single-service build progress orchestrator"
```

---

### Task 6: Wire single-service build paths to the new helper

**Files:**
- Modify: `go/internal/cli/commands/run.go:1483` (registry-push path in `runWithAgent`)
- Modify: `go/internal/cli/commands/run.go:1868-1883` (OCI fast path in `deployByChunkDiff`)

**Interfaces:**
- Consumes: `runBuildWithProgress` (Task 5), existing `buildAndPushImageForAgent` and `buildImageToOCILayout`.
- Produces: clean build output on the two single-service paths.

This task has no new unit test (it rewires existing calls); it is verified by `go build`, the full test suite staying green, and the manual check in Task 8.

- [ ] **Step 1: Rewire the registry-push path**

In `go/internal/cli/commands/run.go`, replace the block at ~1483:

```go
	if err := buildAndPushImageForAgent(ctx, conn, regPort, opts.builder, cwd, repo, platform, opts.dockerfile, buildArgs, "", os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("building and pushing image: %w", err)
	}
	cliLogln("Build and push completed.")
```

with:

```go
	buildTitle := fmt.Sprintf("Building and pushing image for %s...", tui.Value(platform))
	if err := runBuildWithProgress(ctx, buildTitle, func(stream, logw io.Writer) error {
		return buildAndPushImageForAgent(ctx, conn, regPort, opts.builder, cwd, repo, platform, opts.dockerfile, buildArgs, "", stream, logw)
	}); err != nil {
		return fmt.Errorf("building and pushing image: %w", err)
	}
```

(Remove the now-redundant `cliLogln("Build and push completed.")` — the summary line replaces it. The "Building and pushing image with Docker..." line emitted inside `buildAndPushImageForAgentWithBuilder` via `cliLogln` now goes to the quiet `logw`? No — that one uses `cliLogln` to stdout. Leave it; it is a single short line. If it reads as duplication next to the new title, demote it in a follow-up. Do not change `buildAndPushImageForAgentWithBuilder` in this task.)

- [ ] **Step 2: Rewire the OCI fast path**

In `go/internal/cli/commands/run.go`, in `deployByChunkDiff`, replace the build block at ~1868-1883:

```go
	cliLogln("Building image (OCI layout) for %s...", tui.Value(platform))
	var buildOut, buildErr io.Writer = os.Stdout, os.Stderr
	var buildLog *bytes.Buffer
	if opts.quietBuild {
		buildLog = &bytes.Buffer{}
		buildOut, buildErr = buildLog, buildLog
	}
	if err := buildImageToOCILayout(ctx, cwd, dockerfile, platform, buildArgs, opts.builder, ociTar, buildOut, buildErr); err != nil {
		if buildLog != nil && ctx.Err() == nil {
			_, _ = os.Stderr.Write(buildLog.Bytes())
		}
		return err
	}
	mark("build (oci export)")
```

with:

```go
	buildTitle := fmt.Sprintf("Building image (OCI layout) for %s...", tui.Value(platform))
	if opts.quietBuild {
		// wendy watch: keep the legacy quiet behavior (buffer, surface only on
		// genuine failure) rather than rendering a live UI under the watcher.
		var buildLog bytes.Buffer
		if err := buildImageToOCILayout(ctx, cwd, dockerfile, platform, buildArgs, opts.builder, ociTar, &buildLog, &buildLog); err != nil {
			if ctx.Err() == nil {
				_, _ = os.Stderr.Write(buildLog.Bytes())
			}
			return err
		}
	} else {
		if err := runBuildWithProgress(ctx, buildTitle, func(stream, logw io.Writer) error {
			return buildImageToOCILayout(ctx, cwd, dockerfile, platform, buildArgs, opts.builder, ociTar, stream, logw)
		}); err != nil {
			return err
		}
	}
	mark("build (oci export)")
```

- [ ] **Step 3: Build and run the full suite**

Run: `cd go && go build ./internal/cli/... && go test ./internal/cli/...`
Expected: builds clean; all tests pass. If `bytes` or `io` becomes unused in `run.go`, fix imports (`goimports -w internal/cli/commands/run.go`). `bytes` is still used by the `quietBuild` branch, so it should remain.

- [ ] **Step 4: Commit**

```bash
cd go && goimports -w internal/cli/commands/run.go
git add internal/cli/commands/run.go
git commit -m "feat(cli): render single-service builds with the live step view"
```

---

### Task 7: Multi-service — feed step detail + cached/rebuilt counts into MultiSpinner

**Files:**
- Modify: `go/internal/cli/tui/multispinner.go` (extend `MultiSpinnerDoneMsg` + Done row render)
- Modify: `go/internal/cli/commands/multibuild.go:472-502` (per-service parser → detail msgs + tallies)
- Test: `go/internal/cli/tui/multispinner_test.go` (Done row with counts) — create if absent.

**Interfaces:**
- Consumes: `tui.NewBuildParser`, `tui.BuildStepEvent`, `tui.MultiSpinnerDetailMsg` (existing), `tui.BuildTally`.
- Produces: `MultiSpinnerDoneMsg` gains `Cached int` and `Rebuilt int`; the Done row renders `built (N cached, M rebuilt) Dur`.

- [ ] **Step 1: Write the failing test**

Create or append `go/internal/cli/tui/multispinner_test.go`:

```go
package tui

import (
	"strings"
	"testing"
	"time"
)

func TestMultiSpinnerDoneRowShowsCacheCounts(t *testing.T) {
	m := NewMultiSpinner("Building 1 service(s)...", []string{"api"})
	next, _ := m.Update(MultiSpinnerStartMsg{Name: "api"})
	m = next.(MultiSpinnerModel)
	next, _ = m.Update(MultiSpinnerDoneMsg{Name: "api", Dur: 21300 * time.Millisecond, Cached: 4, Rebuilt: 2})
	m = next.(MultiSpinnerModel)
	v := m.View()
	if !strings.Contains(v, "4 cached") || !strings.Contains(v, "2 rebuilt") {
		t.Fatalf("done row missing cache counts:\n%s", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/tui/ -run TestMultiSpinnerDoneRowShowsCacheCounts -v`
Expected: FAIL — `unknown field 'Cached' in struct literal`.

- [ ] **Step 3: Implement the MultiSpinner change**

In `go/internal/cli/tui/multispinner.go`, extend the message and row:

```go
// MultiSpinnerDoneMsg signals that a service's build has finished.
type MultiSpinnerDoneMsg struct {
	Name    string
	Err     error
	Dur     time.Duration
	Cached  int
	Rebuilt int
}
```

Add the two fields to `multiSpinnerRow`:

```go
type multiSpinnerRow struct {
	name    string
	status  MultiSpinnerServiceStatus
	detail  string
	dur     time.Duration
	cached  int
	rebuilt int
	err     error
}
```

In `Update`, in the `MultiSpinnerDoneMsg` case, record them on success:

```go
	case MultiSpinnerDoneMsg:
		if i, ok := m.byName[msg.Name]; ok {
			m.rows[i].dur = msg.Dur
			if msg.Err != nil {
				m.rows[i].status = MultiSpinnerFailed
				m.rows[i].err = msg.Err
			} else {
				m.rows[i].status = MultiSpinnerDone
				m.rows[i].detail = ""
				m.rows[i].cached = msg.Cached
				m.rows[i].rebuilt = msg.Rebuilt
			}
		}
```

In `View`, change the `MultiSpinnerDone` case to include counts:

```go
		case MultiSpinnerDone:
			note := fmt.Sprintf("built (%d cached, %d rebuilt) %s",
				r.cached, r.rebuilt, r.dur.Round(time.Millisecond))
			sb.WriteString(fmt.Sprintf("  %s %s%s\n",
				msCheckStyle.Render("✓"),
				msNameStyle.Render(r.name),
				msDimStyle.Render(note),
			))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/tui/ -run TestMultiSpinnerDoneRowShowsCacheCounts -v`
Expected: PASS.

- [ ] **Step 5: Wire the multibuild per-service parser**

In `go/internal/cli/commands/multibuild.go`, inside the goroutine (currently ~472-502), replace the output-writer setup and the build call so each service parses its stream into detail messages and tallies. Replace the block:

```go
			var buildOut io.Writer
			var logBuf bytes.Buffer
			if prog != nil {
				buildOut = &logBuf
			} else {
				buildOut = os.Stdout
			}
			logOut := buildOut
			if prog == nil {
				logOut = os.Stderr
			}
			err := dockerfileErr
			if err == nil {
				err = buildServiceImage(ctx, conn, regPort, builder, contextDir, repo, platform, dockerfile, buildArgs, repo, buildOut, logOut)
			}
			dur := time.Since(start)

			if prog != nil {
				prog.Send(tui.MultiSpinnerDoneMsg{Name: name, Err: err, Dur: dur})
			} else if err != nil {
				cliLogln("Service %s build failed: %v", name, err)
			} else {
				cliLogln("Service %s built (%s).", name, dur.Round(time.Millisecond))
			}

			results <- result{name: name, err: err, dur: dur, log: logBuf.String()}
```

with:

```go
			var buildOut io.Writer
			var logBuf bytes.Buffer
			var tally func() tui.BuildTally = func() tui.BuildTally { return tui.BuildTally{} }
			if prog != nil {
				// Parse this service's stream into per-row detail updates and
				// cache/rebuild tallies. Raw output is still buffered for the
				// on-failure dump.
				emit, getTally := newServiceProgressEmitter(prog, name)
				tally = getTally
				parser := tui.NewBuildParser(emit)
				buildOut = io.MultiWriter(parser, &logBuf)
			} else {
				buildOut = os.Stdout
			}
			logOut := &logBuf
			var logOutW io.Writer = logOut
			if prog == nil {
				logOutW = os.Stderr
			}
			err := dockerfileErr
			if err == nil {
				err = buildServiceImage(ctx, conn, regPort, builder, contextDir, repo, platform, dockerfile, buildArgs, repo, buildOut, logOutW)
			}
			dur := time.Since(start)

			if prog != nil {
				t := tally()
				prog.Send(tui.MultiSpinnerDoneMsg{Name: name, Err: err, Dur: dur, Cached: t.Cached, Rebuilt: t.Rebuilt})
			} else if err != nil {
				cliLogln("Service %s build failed: %v", name, err)
			} else {
				cliLogln("Service %s built (%s).", name, dur.Round(time.Millisecond))
			}

			results <- result{name: name, err: err, dur: dur, log: logBuf.String()}
```

Add this helper near the bottom of `multibuild.go`:

```go
// newServiceProgressEmitter returns an emit callback for tui.NewBuildParser that
// forwards the active step as a MultiSpinner detail line and accumulates the
// cached/rebuilt tally for the service's done row.
func newServiceProgressEmitter(prog *tea.Program, name string) (func(tui.BuildStepEvent), func() tui.BuildTally) {
	var t tui.BuildTally
	emit := func(e tui.BuildStepEvent) {
		switch e.Status {
		case tui.BuildStepRunning:
			prog.Send(tui.MultiSpinnerDetailMsg{Name: name, Detail: e.Display})
		case tui.BuildStepCached:
			if e.Kind == tui.BuildVertexStep {
				t.Cached++
			}
		case tui.BuildStepDone:
			if e.Kind == tui.BuildVertexStep {
				t.Rebuilt++
			}
		}
	}
	return emit, func() tui.BuildTally { return t }
}
```

Ensure `multibuild.go` imports `tea "github.com/charmbracelet/bubbletea"` (it uses `*tea.Program` already, so this import exists).

- [ ] **Step 6: Build and run the full suite**

Run: `cd go && go build ./internal/cli/... && go test ./internal/cli/...`
Expected: clean build, all green.

- [ ] **Step 7: Commit**

```bash
cd go && gofmt -w internal/cli/tui/multispinner.go internal/cli/tui/multispinner_test.go internal/cli/commands/multibuild.go
git add internal/cli/tui/multispinner.go internal/cli/tui/multispinner_test.go internal/cli/commands/multibuild.go
git commit -m "feat(cli): show current step + cache counts in multi-service build view"
```

---

### Task 8: Quiet the fallback chatter + manual verification

**Files:**
- Modify: `go/internal/cli/commands/run.go:1469` (`Fast layer-diff deploy failed ... falling back`)
- Modify: `go/internal/cli/commands/docker.go` `logAppleContainerFallback` (find with grep) — demote to the build's quiet log.

**Interfaces:** none new.

The fallback messages are currently `cliLogln`'d to stdout and read as noise on the happy path. Collapse the layer-diff fallback to a single short line, and route the Apple Container fallback note through the quiet log path.

- [ ] **Step 1: Shorten the layer-diff fallback line**

In `go/internal/cli/commands/run.go` at ~1469, change:

```go
			cliLogln("Fast layer-diff deploy failed (%v); falling back to registry push.", err)
```

to a shorter, single line (keep it — it explains why the slower path runs, which is useful, but trim the verbose error to one clause):

```go
			cliLogln("Fast deploy unavailable; using registry push.")
```

(Rationale: the detailed error is still surfaced if the registry push then also fails. On the happy path this is one short line instead of a multi-line error echo.)

- [ ] **Step 2: Verify the Apple Container fallback routing**

Run: `cd go && grep -n "func logAppleContainerFallback" internal/cli/commands/docker.go`

Read the function. It currently writes to the `logOutput` it is given. Confirm the single-service call sites now pass the quiet `logw` (they do, after Task 6), so this message is already buffered and only shown on failure. No code change needed if it writes to its writer arg; if it writes to `os.Stderr` directly, change it to write to its passed writer. Make that change only if needed.

- [ ] **Step 3: Build + full suite + vet**

Run: `cd go && go build ./... && go vet ./internal/cli/... && go test ./internal/cli/...`
Expected: clean.

- [ ] **Step 4: Manual verification (the real `cloud run`)**

From an example app directory (e.g. `Examples/HelloPython`), with a device/cloud target configured:

```bash
# interactive single-service: expect the live collapsing step list + summary line
wendy cloud run

# non-TTY: expect concise one-line-per-step output, no redraw
wendy cloud run 2>&1 | cat

# failure path: break the Dockerfile (e.g. a bad pip package) and confirm the
# full raw buildx log is printed after the UI tears down.
```

Confirm:
1. Active step shows a spinner; cached steps show `⚡ ... cached`; done steps show `✓ ... <dur>`.
2. Final line: `✓ Built & pushed (N cached, M rebuilt) in <dur>`.
3. No `[buildx] bootstrapping/inject/restart` noise on success.
4. On failure, the raw `pip` error is visible.

For multi-service, run a Compose-based example and confirm each row shows the current step and a `built (N cached, M rebuilt) <dur>` summary.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/commands/run.go internal/cli/commands/docker.go
git add internal/cli/commands/run.go internal/cli/commands/docker.go
git commit -m "feat(cli): quiet build fallback chatter on the happy path"
```

---

## Notes & known limitations (carry into review)

- **Compose path:** `go/internal/cli/commands/compose.go` deploys via the same multi-service builder (`runMultiServiceWithAgent` / `buildServiceImage`), so Task 7 covers it. If `compose.go` has a separate single-service build call, wire it to `runBuildWithProgress` the same way as Task 6 (verify with `grep -n "buildAndPushImage\|buildImageToOCILayout\|os.Stdout" internal/cli/commands/compose.go`).
- **Retry path:** `buildAndPushImage` retries transient push failures; on a retry the parser sees a second pass (vertex numbers repeat → treated as sub-status). Durations/tallies reflect the last pass; the live list does not reset. Acceptable for v1 (retries are rare). Note it in the PR.
- **Apple Container builder:** emits a different progress format; `classifyBuildVertex` will mark most of it hidden, so the step list may be sparse. The build still works and raw output appears on failure. Out of scope to fully parse.
- **`--progress=plain` and TTY:** even interactive runs use plain (we parse it ourselves and render our own UI), so buildx's own TTY animation is intentionally not used.
