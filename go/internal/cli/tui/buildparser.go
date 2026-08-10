package tui

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BuildVertexKind classifies a buildkit vertex for display.
type BuildVertexKind int

const (
	// BuildVertexHidden is internal noise we never surface (load .dockerignore,
	// importing cache manifest, image resolve).
	BuildVertexHidden BuildVertexKind = iota
	// BuildVertexSetup is a small set of setup vertices worth showing
	// (load build definition, load metadata, load build context).
	BuildVertexSetup
	// BuildVertexStep is a numbered Dockerfile step ("[4/6] RUN ...").
	BuildVertexStep
	// BuildVertexExport is any exporting/pushing vertex; all of them collapse
	// into one synthetic "exporting + pushing layers" phase.
	BuildVertexExport
	// BuildVertexPull is a base-image pull ("docker-image://..."). Pulling a
	// multi-gigabyte CUDA base is often the slowest part of a cold build, so it
	// gets its own visible row with byte progress rather than being hidden.
	BuildVertexPull
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
const BuildExportVertexID = "export"

// detailThrottle bounds how often a single vertex may push a new detail line.
// swiftc can emit hundreds of "[N/M] Compiling" lines a second; without this the
// Bubble Tea message queue becomes the bottleneck.
const detailThrottle = 80 * time.Millisecond

// structuredDetailTTL is how long a matched progress line ("[525/1027]",
// "Fetched 9940 kB") keeps precedence over raw output from the same step.
const structuredDetailTTL = 2 * time.Second

// BuildStepEvent is emitted by BuildParser for each meaningful transition.
type BuildStepEvent struct {
	// ID identifies the vertex: "#12" on the plain-text path, the vertex digest
	// on the rawjson path, BuildExportVertexID for the collapsed export phase.
	ID      string
	Kind    BuildVertexKind
	Display string
	Status  BuildStepStatus
	Dur     time.Duration // set when Status == BuildStepDone
	// Detail is the live sub-line for a running step ("[525/1027] 51% Compiling
	// WendyKit"). Empty on terminal events.
	Detail string
	// Bytes carries transfer counters when the step is moving data.
	Bytes ByteProgress
}

var (
	buildVertexLineRe      = regexp.MustCompile(`^#(\d+) (.*)$`)
	buildDoneRe            = regexp.MustCompile(`^DONE (\d+(?:\.\d+)?)s$`)
	buildLogLineRe         = regexp.MustCompile(`^(\d+\.\d+) `)        // "1.563 Collecting ..."
	buildStepLabelRe       = regexp.MustCompile(`^\[[^\]]*\d+/\d+\] `) // "[4/6] ", "[stage 2/3] "
	buildContextTransferRe = regexp.MustCompile(`\btransferring context:\s*([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?i?B|[kKmMgGtT]?B)\b`)
)

type buildVertexState struct {
	named   bool
	kind    BuildVertexKind
	display string
	// lastStructuredAt is when a sniffer last matched for this vertex. While
	// structured progress keeps arriving, unstructured chatter (a compiler
	// warning, a pip notice) must not clobber "[525/1027]". Once it goes stale
	// the raw tail takes over again — apt prints "Fetched 9940 kB" and then
	// spends a minute unpacking, and a frozen line there reads as a hang.
	lastStructuredAt time.Time
	detail           string
	bytes            ByteProgress
	lastDetailAt     time.Time
	terminal         bool

	// rawjson-only state.
	logTail  []byte
	statuses map[string]*rawStatusCounter
	started  time.Time
}

// BuildParser consumes `docker buildx` progress output and calls emit for each
// meaningful step transition. It accepts both --progress=plain and
// --progress=rawjson and detects which per line, so callers never have to agree
// with it in advance about the mode (Apple Container only speaks plain, buildx
// speaks rawjson when new enough, and warnings interleave as bare text).
//
// It implements io.Writer and is intended for sequential writes from a single
// goroutine (os/exec's output copier).
type BuildParser struct {
	emit          func(BuildStepEvent)
	line          []byte
	vertex        map[string]*buildVertexState
	exportStarted bool
	throttle      time.Duration
	now           func() time.Time
}

// NewBuildParser returns a parser that calls emit for each event. emit must be
// safe to call from the goroutine that writes to the parser.
func NewBuildParser(emit func(BuildStepEvent)) *BuildParser {
	return &BuildParser{
		emit:     emit,
		vertex:   map[string]*buildVertexState{},
		throttle: detailThrottle,
		now:      time.Now,
	}
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

// ParseBuildContextTransferBytes returns the byte count from a BuildKit plain
// progress line such as "#4 transferring context: 2B".
func ParseBuildContextTransferBytes(line string) (int64, bool) {
	if len(line) > 512 {
		return 0, false
	}
	m := buildContextTransferRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult, ok := byteUnitMultiplier(m[2])
	if !ok {
		return 0, false
	}
	return int64(math.Round(value * mult)), true
}

func (p *BuildParser) parseLine(line string) {
	// rawjson frames are one JSON object per line; anything else is plain text.
	if len(line) > 0 && line[0] == '{' {
		if p.parseRawJSONFrame(line) {
			return
		}
	}
	p.parsePlainLine(line)
}

func (p *BuildParser) parsePlainLine(line string) {
	m := buildVertexLineRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	id := "#" + m[1]
	rest := m[2]
	v := p.state(id)

	switch {
	case rest == "CACHED":
		p.emitStatus(id, v, BuildStepCached, 0)
		return
	case strings.HasPrefix(rest, "ERROR"):
		p.emitStatus(id, v, BuildStepFailed, 0)
		return
	case buildLogLineRe.MatchString(rest):
		// "1.563 [525/1027] Compiling Foo.swift" — strip BuildKit's relative
		// timestamp and feed the tool's own output to the sniffers.
		if idx := strings.IndexByte(rest, ' '); idx >= 0 {
			p.onLogLine(id, v, rest[idx+1:])
		}
		return
	}
	if dm := buildDoneRe.FindStringSubmatch(rest); dm != nil {
		secs, _ := strconv.ParseFloat(dm[1], 64)
		p.emitStatus(id, v, BuildStepDone, time.Duration(secs*float64(time.Second)))
		return
	}

	if v.named {
		// A sub-status line ("resolve ... done", "sha256:... 5.24MB / 27.09MB").
		// Byte counters are the only part worth surfacing.
		if bp, ok := sniffTransferBytes(rest); ok {
			p.onBytes(id, v, bp)
		}
		return
	}
	// First occurrence of this vertex: its remainder is the vertex name.
	v.named = true
	v.kind, v.display = classifyBuildVertex(rest)
	p.emitStart(id, v)
}

// emitStart emits the initial Running event for a newly named vertex, applying
// the export-collapse rule.
func (p *BuildParser) emitStart(id string, v *buildVertexState) {
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

// onLogLine turns one line of a step's own output into a detail update.
func (p *BuildParser) onLogLine(id string, v *buildVertexState, raw string) {
	if !v.named || v.kind == BuildVertexHidden || v.terminal {
		return
	}
	now := p.now()
	detail, structured := sniffProgressDetail(raw)
	if structured {
		v.lastStructuredAt = now
	} else {
		if !v.lastStructuredAt.IsZero() && now.Sub(v.lastStructuredAt) < structuredDetailTTL {
			// Recent structured progress outranks stray output.
			return
		}
		detail = compactLogLine(raw)
	}
	if detail == "" || detail == v.detail {
		return
	}
	if p.throttle > 0 && !v.lastDetailAt.IsZero() && now.Sub(v.lastDetailAt) < p.throttle {
		// Still record it so the next update shows the newest value.
		v.detail = detail
		return
	}
	v.detail = detail
	v.lastDetailAt = now
	p.emitProgress(id, v)
}

func (p *BuildParser) onBytes(id string, v *buildVertexState, bp ByteProgress) {
	if !v.named || v.kind == BuildVertexHidden || v.terminal {
		return
	}
	v.bytes = bp
	now := p.now()
	if p.throttle > 0 && !v.lastDetailAt.IsZero() && now.Sub(v.lastDetailAt) < p.throttle {
		return
	}
	v.lastDetailAt = now
	p.emitProgress(id, v)
}

// emitProgress republishes a running step with its current detail and bytes.
func (p *BuildParser) emitProgress(id string, v *buildVertexState) {
	if v.kind == BuildVertexExport {
		id = BuildExportVertexID
	}
	p.emit(BuildStepEvent{
		ID:      id,
		Kind:    v.kind,
		Display: v.display,
		Status:  BuildStepRunning,
		Detail:  v.detail,
		Bytes:   v.bytes,
	})
}

// emitStatus emits a terminal status (cached/done/failed) for a vertex, applying
// the export-collapse rule: individual export vertices never flip the collapsed
// phase to done/cached (the caller marks the whole build done), but a failure is
// still surfaced.
func (p *BuildParser) emitStatus(id string, v *buildVertexState, status BuildStepStatus, dur time.Duration) {
	if !v.named || v.kind == BuildVertexHidden {
		return
	}
	v.terminal = true
	if v.kind == BuildVertexExport {
		if status == BuildStepFailed {
			p.emit(BuildStepEvent{ID: BuildExportVertexID, Kind: BuildVertexExport,
				Display: "exporting + pushing layers", Status: BuildStepFailed})
		}
		return
	}
	p.emit(BuildStepEvent{ID: id, Kind: v.kind, Display: v.display, Status: status, Dur: dur})
}

func (p *BuildParser) state(id string) *buildVertexState {
	v := p.vertex[id]
	if v == nil {
		v = &buildVertexState{}
		p.vertex[id] = v
	}
	return v
}

// classifyBuildVertex maps a buildkit vertex name to a display kind and a cleaned
// label. Unknown internal vertices are hidden to keep the view uncluttered.
func classifyBuildVertex(name string) (BuildVertexKind, string) {
	switch {
	case strings.HasPrefix(name, "[internal] load metadata"):
		return BuildVertexSetup, "load metadata"
	case strings.HasPrefix(name, "[internal] load build definition"):
		return BuildVertexSetup, "load build definition"
	case strings.HasPrefix(name, "[internal] load build context"):
		return BuildVertexSetup, "load build context"
	case strings.HasPrefix(name, "exporting"), strings.HasPrefix(name, "pushing"):
		return BuildVertexExport, name
	case strings.HasPrefix(name, "[internal"):
		return BuildVertexHidden, name
	case strings.HasPrefix(name, "docker-image://"):
		return BuildVertexPull, "pull " + shortImageRef(strings.TrimPrefix(name, "docker-image://"))
	}
	if buildStepLabelRe.MatchString(name) {
		return BuildVertexStep, name
	}
	return BuildVertexHidden, name
}

// shortImageRef drops the registry host and digest so a pull row stays readable
// ("nvcr.io/nvidia/l4t-base:r36.2@sha256:ab…" -> "nvidia/l4t-base:r36.2").
func shortImageRef(ref string) string {
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i]
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 1 && strings.ContainsAny(parts[0], ".:") {
		ref = strings.Join(parts[1:], "/")
	}
	return ref
}
