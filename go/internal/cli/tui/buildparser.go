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
	buildLogLineRe    = regexp.MustCompile(`^\d+\.\d+ `)          // "1.563 Collecting ..."
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
	case strings.HasPrefix(name, "[internal"):
		return BuildVertexHidden, name
	}
	if buildStepLabelRe.MatchString(name) {
		return BuildVertexStep, name
	}
	return BuildVertexHidden, name
}
