# `wendy project optimize` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `wendy project optimize` subcommand that statically analyzes a Wendy project's build config (Dockerfile, requirements.txt, wendy.json, native build files) for missed optimizations, reports findings, applies safe `--fix`es, and can emit an `--agentic` context bundle.

**Architecture:** A standalone, Cobra-free engine package `go/internal/cli/optimize/` holds parsers, the `Analyzer` registry, four analyzers, the reporter, the fix applier, and the agentic bundle. A thin command in `go/internal/cli/commands/optimize.go` wires flags and exit codes and registers under `newProjectCmd()`. Reporter and bundle both consume the same `[]Finding`.

**Tech Stack:** Go 1.26.4, Cobra (spf13/cobra), lipgloss (charmbracelet/lipgloss) via the existing `tui` package. Stdlib `testing` + table tests only (no testify, no golden libs). No new third-party dependencies — Dockerfile parsing is an internal line-based parser.

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy`. All internal imports use this prefix.
- Go version: `go 1.26.4`.
- No new third-party dependencies. Dockerfile/requirements parsing is internal stdlib code.
- Engine package `optimize` must NOT import `spf13/cobra` (testable in isolation).
- All analyzer files live in `package optimize` (single package, multiple files) — NOT a `checks/` sub-package — to avoid an import cycle between the registry and the analyzers. (Deviation from the spec's directory sketch; same package, same behavior.)
- Tests use stdlib `testing` with table-driven subtests and `t.Fatalf`/`t.Errorf`. Use `t.TempDir()` for filesystem fixtures.
- Persistent `--json` flag is read via the package-global `jsonOutput bool` in `package commands` (defined in `root.go`); do not add a second json flag.
- Default target arch is `"arm64"`. `--arch` overrides. Device-based arch inference is out of scope for this plan.
- Entitlement GPU constant: `appconfig.EntitlementGPU` (== `"gpu"`).
- Exit codes: `0` no findings ≥ threshold; `1` findings ≥ threshold; `2` execution error.
- Run `cd go && gofmt -w <files>` and `cd go && go test ./internal/cli/optimize/...` from the `go/` module dir (the module root is `go/`, module `github.com/wendylabsinc/wendy`).

---

## File Structure

Engine package `go/internal/cli/optimize/`:
- `finding.go` — `Severity`, `Loc`, `FixOp`, `Fix`, `Finding` types + helpers.
- `dockerfile.go` — internal Dockerfile parser → `Dockerfile`, `Instruction`.
- `requirements.go` — requirements.txt parser → `Requirements`, `Requirement`.
- `target.go` — `Target`, `TargetKind`, `DiscoverTargets`, `resolveArch`.
- `analyzer.go` — `Analyzer` interface, `DefaultAnalyzers()`, `Analyze()` engine driver.
- `buildcache.go` — `buildCacheAnalyzer`.
- `releasedebug.go` — `releaseDebugAnalyzer`.
- `cudaml.go` — `cudaMLAnalyzer`.
- `archimage.go` — `archImageAnalyzer`.
- `fix.go` — `ApplyFixes`.
- `report.go` — `RenderHuman`, `MarshalJSON` report shape.
- `bundle.go` — `BuildBundle`.

Command wiring:
- `go/internal/cli/commands/optimize.go` — `newOptimizeCmd()`.
- `go/internal/cli/commands/project.go:34-42` — register `newOptimizeCmd()`.

---

## Task 1: Core types (Severity, Finding, Fix)

**Files:**
- Create: `go/internal/cli/optimize/finding.go`
- Test: `go/internal/cli/optimize/finding_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Severity int` with `const (SeverityInfo Severity = iota; SeverityWarning; SeverityError)`.
  - `func (s Severity) String() string` → `"info"|"warning"|"error"`.
  - `func ParseSeverity(string) (Severity, error)`.
  - `type Loc struct { File string; Line int }`.
  - `type FixOp int` with `const (FixCreateFile FixOp = iota; FixReplaceLine)`.
  - `type Fix struct { Description string; Op FixOp; File string; Line int; Old string; New string }`.
  - `type Finding struct { Analyzer string; Severity Severity; Title string; Detail string; Location *Loc; Fix *Fix }`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/finding_test.go
package optimize

import "testing"

func TestSeverityString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SeverityInfo, "info"},
		{SeverityWarning, "warning"},
		{SeverityError, "error"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.sev.String(); got != c.want {
				t.Fatalf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseSeverity(t *testing.T) {
	got, err := ParseSeverity("warning")
	if err != nil {
		t.Fatalf("ParseSeverity returned error: %v", err)
	}
	if got != SeverityWarning {
		t.Fatalf("ParseSeverity = %v, want SeverityWarning", got)
	}
	if _, err := ParseSeverity("bogus"); err == nil {
		t.Fatalf("ParseSeverity(\"bogus\") expected error, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestSeverity -v`
Expected: FAIL — build error, `undefined: Severity`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/finding.go
package optimize

import "fmt"

// Severity ranks how strongly a finding should be acted on.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	default:
		return "unknown"
	}
}

// ParseSeverity maps a CLI string to a Severity.
func ParseSeverity(s string) (Severity, error) {
	switch s {
	case "info":
		return SeverityInfo, nil
	case "warning":
		return SeverityWarning, nil
	case "error":
		return SeverityError, nil
	default:
		return SeverityInfo, fmt.Errorf("invalid severity %q (want info, warning, or error)", s)
	}
}

// Loc points at a source location for a finding.
type Loc struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// FixOp is the kind of edit a Fix performs.
type FixOp int

const (
	// FixCreateFile creates File with New as its full contents (skipped if File exists).
	FixCreateFile FixOp = iota
	// FixReplaceLine replaces line Line of File: it must currently equal Old, and becomes New.
	FixReplaceLine
)

// Fix is a deterministic, safe edit attached to a Finding. Nil means report-only.
type Fix struct {
	Description string `json:"description"`
	Op          FixOp  `json:"op"`
	File        string `json:"file"`
	Line        int    `json:"line,omitempty"`
	Old         string `json:"old,omitempty"`
	New         string `json:"new"`
}

// Finding is a single optimization issue.
type Finding struct {
	Analyzer string `json:"analyzer"`
	Severity Severity
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Location *Loc   `json:"location,omitempty"`
	Fix      *Fix   `json:"fix,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestSeverity -v && go test ./internal/cli/optimize/ -run TestParseSeverity -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/finding.go internal/cli/optimize/finding_test.go
git add go/internal/cli/optimize/finding.go go/internal/cli/optimize/finding_test.go
git commit -m "feat(optimize): add core finding/severity/fix types"
```

---

## Task 2: Dockerfile parser

**Files:**
- Create: `go/internal/cli/optimize/dockerfile.go`
- Test: `go/internal/cli/optimize/dockerfile_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Instruction struct { Cmd string; Args string; Flags []string; Line int }` — `Cmd` is upper-cased (e.g. `RUN`); `Args` is the full argument text with continuations joined by spaces; `Flags` holds instruction flags like `--mount=type=cache,...` and `--platform=...`; `Line` is the 1-based line where the instruction starts.
  - `type Dockerfile struct { Path string; Lines []string; Instructions []Instruction }` — `Lines` is the raw file split by `\n` (no trailing newline element), used later by the fix applier.
  - `func ParseDockerfile(path string, data []byte) *Dockerfile`.

Parser rules: skip blank lines and `#`-comment lines; a line ending in `\` (after trimming trailing spaces) continues onto the next line; the first token of a logical line is the command (upper-cased); tokens of the form `--xxx` immediately following the command are flags; the remainder is `Args`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/dockerfile_test.go
package optimize

import "testing"

func TestParseDockerfile(t *testing.T) {
	src := "# comment\n" +
		"FROM --platform=linux/amd64 python:3.12-slim\n" +
		"ARG WENDY_DEBUG=0\n" +
		"RUN --mount=type=cache,target=/root/.cargo \\\n" +
		"    cargo build --release\n" +
		"COPY . .\n"

	df := ParseDockerfile("Dockerfile", []byte(src))

	if len(df.Instructions) != 4 {
		t.Fatalf("got %d instructions, want 4: %+v", len(df.Instructions), df.Instructions)
	}

	from := df.Instructions[0]
	if from.Cmd != "FROM" || from.Line != 2 {
		t.Fatalf("FROM = %+v, want Cmd=FROM Line=2", from)
	}
	if len(from.Flags) != 1 || from.Flags[0] != "--platform=linux/amd64" {
		t.Fatalf("FROM flags = %v, want [--platform=linux/amd64]", from.Flags)
	}
	if from.Args != "python:3.12-slim" {
		t.Fatalf("FROM args = %q, want python:3.12-slim", from.Args)
	}

	run := df.Instructions[2]
	if run.Cmd != "RUN" || run.Line != 4 {
		t.Fatalf("RUN = %+v, want Cmd=RUN Line=4", run)
	}
	if len(run.Flags) != 1 || run.Flags[0] != "--mount=type=cache,target=/root/.cargo" {
		t.Fatalf("RUN flags = %v", run.Flags)
	}
	if run.Args != "cargo build --release" {
		t.Fatalf("RUN args = %q, want 'cargo build --release'", run.Args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestParseDockerfile -v`
Expected: FAIL — `undefined: ParseDockerfile`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/dockerfile.go
package optimize

import "strings"

// Instruction is one parsed Dockerfile instruction.
type Instruction struct {
	Cmd   string   // upper-cased command, e.g. "RUN"
	Args  string   // argument text with line continuations joined by single spaces
	Flags []string // instruction flags, e.g. "--mount=type=cache,target=/x", "--platform=linux/amd64"
	Line  int      // 1-based line where the instruction starts
}

// Dockerfile is a parsed Dockerfile.
type Dockerfile struct {
	Path         string
	Lines        []string // raw lines, no trailing empty element
	Instructions []Instruction
}

// ParseDockerfile parses Dockerfile bytes. It is lenient: malformed lines are
// best-effort, never fatal.
func ParseDockerfile(path string, data []byte) *Dockerfile {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rawLines := strings.Split(text, "\n")
	// Drop a single trailing empty element from a final newline.
	if n := len(rawLines); n > 0 && rawLines[n-1] == "" {
		rawLines = rawLines[:n-1]
	}

	df := &Dockerfile{Path: path, Lines: rawLines}

	i := 0
	for i < len(rawLines) {
		startLine := i + 1 // 1-based
		line := rawLines[i]
		trimmed := strings.TrimSpace(line)
		i++

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Join continuation lines (current line ends with backslash).
		joined := strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\"))
		for strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") && i < len(rawLines) {
			next := rawLines[i]
			i++
			joined += " " + strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(next, " \t"), "\\"))
			line = next
		}
		joined = strings.TrimSpace(joined)
		if joined == "" {
			continue
		}

		fields := strings.Fields(joined)
		inst := Instruction{Cmd: strings.ToUpper(fields[0]), Line: startLine}
		rest := fields[1:]
		// Leading --flags belong to the instruction.
		j := 0
		for j < len(rest) && strings.HasPrefix(rest[j], "--") {
			inst.Flags = append(inst.Flags, rest[j])
			j++
		}
		inst.Args = strings.Join(rest[j:], " ")
		df.Instructions = append(df.Instructions, inst)
	}
	return df
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestParseDockerfile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/dockerfile.go internal/cli/optimize/dockerfile_test.go
git add go/internal/cli/optimize/dockerfile.go go/internal/cli/optimize/dockerfile_test.go
git commit -m "feat(optimize): add internal Dockerfile parser"
```

---

## Task 3: requirements.txt parser

**Files:**
- Create: `go/internal/cli/optimize/requirements.go`
- Test: `go/internal/cli/optimize/requirements_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Requirement struct { Name string; VersionSpec string; LocalLabel string; Line int }` — `Name` lower-cased package name; `VersionSpec` the version constraint text (e.g. `==2.3.0`); `LocalLabel` the PEP 440 local label after `+` (e.g. `cpu`, `cu118`), empty if none; `Line` 1-based.
  - `type Requirements struct { Path string; Packages []Requirement; IndexURLs []string }` — `IndexURLs` collects values from `--index-url`/`--extra-index-url`/`-i` lines.
  - `func ParseRequirements(path string, data []byte) *Requirements`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/requirements_test.go
package optimize

import "testing"

func TestParseRequirements(t *testing.T) {
	src := "# deps\n" +
		"--extra-index-url https://download.pytorch.org/whl/cpu\n" +
		"torch==2.3.0+cpu\n" +
		"numpy>=1.26\n" +
		"onnxruntime-gpu\n"

	r := ParseRequirements("requirements.txt", []byte(src))

	if len(r.Packages) != 3 {
		t.Fatalf("got %d packages, want 3: %+v", len(r.Packages), r.Packages)
	}
	if len(r.IndexURLs) != 1 || r.IndexURLs[0] != "https://download.pytorch.org/whl/cpu" {
		t.Fatalf("IndexURLs = %v", r.IndexURLs)
	}

	torch := r.Packages[0]
	if torch.Name != "torch" || torch.VersionSpec != "==2.3.0" || torch.LocalLabel != "cpu" || torch.Line != 3 {
		t.Fatalf("torch = %+v", torch)
	}
	if r.Packages[2].Name != "onnxruntime-gpu" {
		t.Fatalf("third pkg = %+v, want onnxruntime-gpu", r.Packages[2])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestParseRequirements -v`
Expected: FAIL — `undefined: ParseRequirements`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/requirements.go
package optimize

import (
	"regexp"
	"strings"
)

// Requirement is one parsed line of requirements.txt.
type Requirement struct {
	Name        string // lower-cased package name
	VersionSpec string // e.g. "==2.3.0", ">=1.26", "" if none
	LocalLabel  string // PEP 440 local label after '+', e.g. "cpu", "cu118"
	Line        int
}

// Requirements is a parsed requirements.txt.
type Requirements struct {
	Path      string
	Packages  []Requirement
	IndexURLs []string
}

// namePattern matches the leading distribution name of a requirement line.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// ParseRequirements parses requirements.txt bytes leniently.
func ParseRequirements(path string, data []byte) *Requirements {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	r := &Requirements{Path: path}

	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip inline comments.
		if h := strings.Index(line, " #"); h >= 0 {
			line = strings.TrimSpace(line[:h])
		}

		if strings.HasPrefix(line, "--index-url") || strings.HasPrefix(line, "--extra-index-url") || strings.HasPrefix(line, "-i ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				r.IndexURLs = append(r.IndexURLs, fields[len(fields)-1])
			}
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue // other pip options
		}

		name := namePattern.FindString(line)
		if name == "" {
			continue
		}
		rest := line[len(name):]
		req := Requirement{Name: strings.ToLower(name), Line: idx + 1}

		// Split off extras "[...]" if present.
		if strings.HasPrefix(rest, "[") {
			if end := strings.Index(rest, "]"); end >= 0 {
				rest = rest[end+1:]
			}
		}
		// Local label: "+label" appears within the version spec.
		spec := rest
		if plus := strings.Index(spec, "+"); plus >= 0 {
			label := spec[plus+1:]
			// label ends at first non [A-Za-z0-9.] char
			labelEnd := len(label)
			for k, c := range label {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.') {
					labelEnd = k
					break
				}
			}
			req.LocalLabel = label[:labelEnd]
			spec = spec[:plus]
		}
		req.VersionSpec = strings.TrimSpace(spec)
		r.Packages = append(r.Packages, req)
	}
	return r
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestParseRequirements -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/requirements.go internal/cli/optimize/requirements_test.go
git add go/internal/cli/optimize/requirements.go go/internal/cli/optimize/requirements_test.go
git commit -m "feat(optimize): add requirements.txt parser"
```

---

## Task 4: Target discovery + arch resolution

**Files:**
- Create: `go/internal/cli/optimize/target.go`
- Test: `go/internal/cli/optimize/target_test.go`

**Interfaces:**
- Consumes: `appconfig.AppConfig` (`github.com/wendylabsinc/wendy/go/internal/shared/appconfig`), `ParseDockerfile`, `ParseRequirements`.
- Produces:
  - `type TargetKind int` with `const (KindDockerfile TargetKind = iota; KindComposeService; KindNativeSwift; KindNativeBrew)`.
  - `func (k TargetKind) String() string` → `"dockerfile"|"compose-service"|"native-swift"|"native-brew"`.
  - `type Target struct { Name string; Kind TargetKind; Dir string; Dockerfile *Dockerfile; Requirements *Requirements; Config *appconfig.AppConfig; Arch string }`.
  - `func resolveArch(override string) string` — returns `override` if non-empty, else `"arm64"`.
  - `func DiscoverTargets(dir string, cfg *appconfig.AppConfig, arch string) ([]Target, error)` — `arch` is the already-resolved arch string; never errors on a missing Dockerfile (returns native or empty targets); errors only on unreadable dirs.

Discovery order: if `cfg != nil && len(cfg.Services) > 0`, one `KindComposeService` target per service (sorted by service name), each resolving `<dir>/<service.Context or ".">` for a Dockerfile; else if a Dockerfile exists in `dir`, one `KindDockerfile`; else if `Package.swift` exists, one `KindNativeSwift`; else if `Brewfile` exists, one `KindNativeBrew`; else empty slice. A `requirements.txt` found in a target's `Dir` is attached. The Dockerfile filename is detected with the existing `isContainerBuildFileName` rules — for this task, match `Dockerfile` or `Containerfile` exactly (variants handled by reusing the helper is a follow-up).

NOTE on `ServiceConfig`: the `appconfig.ServiceConfig` type has a context/dockerfile field. Read its actual field names from `go/internal/shared/appconfig/appconfig.go` during implementation; the test below uses only single-Dockerfile and native cases to stay independent of that struct's exact shape. Compose discovery is covered by the dedicated subtest using a synthesized `Services` map.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/target_test.go
package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestResolveArch(t *testing.T) {
	if got := resolveArch(""); got != "arm64" {
		t.Fatalf("resolveArch(\"\") = %q, want arm64", got)
	}
	if got := resolveArch("amd64"); got != "amd64" {
		t.Fatalf("resolveArch(\"amd64\") = %q, want amd64", got)
	}
}

func TestDiscoverSingleDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM python:3.12-slim\n")
	writeFile(t, dir, "requirements.txt", "torch==2.3.0+cpu\n")

	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	tg := targets[0]
	if tg.Kind != KindDockerfile {
		t.Fatalf("kind = %v, want KindDockerfile", tg.Kind)
	}
	if tg.Dockerfile == nil || len(tg.Dockerfile.Instructions) == 0 {
		t.Fatalf("Dockerfile not parsed")
	}
	if tg.Requirements == nil || len(tg.Requirements.Packages) != 1 {
		t.Fatalf("requirements not attached")
	}
	if tg.Arch != "arm64" {
		t.Fatalf("arch = %q, want arm64", tg.Arch)
	}
}

func TestDiscoverNativeSwift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Package.swift", "// swift-tools-version:6.0\n")
	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Kind != KindNativeSwift {
		t.Fatalf("targets = %+v, want one KindNativeSwift", targets)
	}
}

func TestDiscoverNothing(t *testing.T) {
	dir := t.TempDir()
	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("got %d targets, want 0", len(targets))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestDiscover -v`
Expected: FAIL — `undefined: DiscoverTargets`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/target.go
package optimize

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// TargetKind is the kind of buildable unit being analyzed.
type TargetKind int

const (
	KindDockerfile TargetKind = iota
	KindComposeService
	KindNativeSwift
	KindNativeBrew
)

func (k TargetKind) String() string {
	switch k {
	case KindDockerfile:
		return "dockerfile"
	case KindComposeService:
		return "compose-service"
	case KindNativeSwift:
		return "native-swift"
	case KindNativeBrew:
		return "native-brew"
	default:
		return "unknown"
	}
}

// Target is one buildable unit to analyze.
type Target struct {
	Name         string
	Kind         TargetKind
	Dir          string
	Dockerfile   *Dockerfile
	Requirements *Requirements
	Config       *appconfig.AppConfig
	Arch         string
}

// resolveArch returns the override if set, else the offline default.
func resolveArch(override string) string {
	if override != "" {
		return override
	}
	return "arm64"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// loadDockerfile parses Dockerfile or Containerfile in dir, returning nil if neither exists.
func loadDockerfile(dir string) *Dockerfile {
	for _, name := range []string{"Dockerfile", "Containerfile"} {
		p := filepath.Join(dir, name)
		if fileExists(p) {
			data, err := os.ReadFile(p)
			if err == nil {
				return ParseDockerfile(p, data)
			}
		}
	}
	return nil
}

// loadRequirements parses requirements.txt in dir, returning nil if absent.
func loadRequirements(dir string) *Requirements {
	p := filepath.Join(dir, "requirements.txt")
	if fileExists(p) {
		data, err := os.ReadFile(p)
		if err == nil {
			return ParseRequirements(p, data)
		}
	}
	return nil
}

// DiscoverTargets decides what to analyze in dir.
func DiscoverTargets(dir string, cfg *appconfig.AppConfig, arch string) ([]Target, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	// Multi-service / compose.
	if cfg != nil && len(cfg.Services) > 0 {
		names := make([]string, 0, len(cfg.Services))
		for name := range cfg.Services {
			names = append(names, name)
		}
		sort.Strings(names)

		targets := make([]Target, 0, len(names))
		for _, name := range names {
			svcDir := dir // service context resolution refined during impl per ServiceConfig fields
			targets = append(targets, Target{
				Name:         name,
				Kind:         KindComposeService,
				Dir:          svcDir,
				Dockerfile:   loadDockerfile(svcDir),
				Requirements: loadRequirements(svcDir),
				Config:       cfg,
				Arch:         arch,
			})
		}
		return targets, nil
	}

	// Single Dockerfile.
	if df := loadDockerfile(dir); df != nil {
		return []Target{{
			Name:         "app",
			Kind:         KindDockerfile,
			Dir:          dir,
			Dockerfile:   df,
			Requirements: loadRequirements(dir),
			Config:       cfg,
			Arch:         arch,
		}}, nil
	}

	// Native.
	if fileExists(filepath.Join(dir, "Package.swift")) {
		return []Target{{Name: "app", Kind: KindNativeSwift, Dir: dir, Requirements: loadRequirements(dir), Config: cfg, Arch: arch}}, nil
	}
	if fileExists(filepath.Join(dir, "Brewfile")) {
		return []Target{{Name: "app", Kind: KindNativeBrew, Dir: dir, Requirements: loadRequirements(dir), Config: cfg, Arch: arch}}, nil
	}

	return []Target{}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestResolveArch -v && go test ./internal/cli/optimize/ -run TestDiscover -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/target.go internal/cli/optimize/target_test.go
git add go/internal/cli/optimize/target.go go/internal/cli/optimize/target_test.go
git commit -m "feat(optimize): add target discovery and arch resolution"
```

---

## Task 5: Analyzer interface + engine driver

**Files:**
- Create: `go/internal/cli/optimize/analyzer.go`
- Test: `go/internal/cli/optimize/analyzer_test.go`

**Interfaces:**
- Consumes: `Target`, `Finding`.
- Produces:
  - `type Analyzer interface { ID() string; Analyze(t *Target) []Finding }`.
  - `func DefaultAnalyzers() []Analyzer` — returns the registered analyzers. Starts empty; each later task appends its analyzer here.
  - `func Analyze(targets []Target, analyzers []Analyzer) []Finding` — runs each analyzer over each target (analyzer takes `*Target`), concatenating results in target-then-analyzer order.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/analyzer_test.go
package optimize

import "testing"

type fakeAnalyzer struct{ id string }

func (f fakeAnalyzer) ID() string { return f.id }
func (f fakeAnalyzer) Analyze(t *Target) []Finding {
	return []Finding{{Analyzer: f.id, Severity: SeverityWarning, Title: t.Name}}
}

func TestAnalyzeRunsAllAnalyzersOverAllTargets(t *testing.T) {
	targets := []Target{{Name: "a", Kind: KindDockerfile}, {Name: "b", Kind: KindDockerfile}}
	analyzers := []Analyzer{fakeAnalyzer{"x"}, fakeAnalyzer{"y"}}

	findings := Analyze(targets, analyzers)
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4", len(findings))
	}
	// target-then-analyzer order
	if findings[0].Title != "a" || findings[0].Analyzer != "x" {
		t.Fatalf("findings[0] = %+v", findings[0])
	}
	if findings[1].Analyzer != "y" {
		t.Fatalf("findings[1] = %+v", findings[1])
	}
	if findings[2].Title != "b" {
		t.Fatalf("findings[2] = %+v", findings[2])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestAnalyzeRuns -v`
Expected: FAIL — `undefined: Analyze` / `undefined: Analyzer`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/analyzer.go
package optimize

// Analyzer inspects a Target and returns findings.
type Analyzer interface {
	ID() string
	Analyze(t *Target) []Finding
}

// DefaultAnalyzers returns all built-in analyzers. Each analyzer task appends here.
func DefaultAnalyzers() []Analyzer {
	return []Analyzer{
		// appended by later tasks: buildCacheAnalyzer{}, releaseDebugAnalyzer{}, cudaMLAnalyzer{}, archImageAnalyzer{}
	}
}

// Analyze runs every analyzer over every target, in target-then-analyzer order.
func Analyze(targets []Target, analyzers []Analyzer) []Finding {
	var out []Finding
	for i := range targets {
		t := &targets[i]
		for _, a := range analyzers {
			out = append(out, a.Analyze(t)...)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestAnalyzeRuns -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/analyzer.go internal/cli/optimize/analyzer_test.go
git add go/internal/cli/optimize/analyzer.go go/internal/cli/optimize/analyzer_test.go
git commit -m "feat(optimize): add analyzer interface and engine driver"
```

---

## Task 6: buildcache analyzer

**Files:**
- Create: `go/internal/cli/optimize/buildcache.go`
- Modify: `go/internal/cli/optimize/analyzer.go` (append `buildCacheAnalyzer{}` to `DefaultAnalyzers()`)
- Test: `go/internal/cli/optimize/buildcache_test.go`

**Interfaces:**
- Consumes: `Target`, `Instruction`, `Finding`, `Fix`.
- Produces: `type buildCacheAnalyzer struct{}` implementing `Analyzer` with `ID() == "build-cache"`.

Behavior: for each `RUN` instruction in the target's Dockerfile, if `Args` contains a known compiled-lang build/install command AND the instruction has no `--mount=type=cache` flag, emit a `SeverityWarning` finding with a `FixReplaceLine` that rewrites the whole line to add the appropriate cache mount right after `RUN`. Idempotency is enforced by the fix applier (Task 10) checking `Old`. Commands → cache targets:
- `cargo build`, `cargo fetch` → `/root/.cargo`
- `go build`, `go mod download` → `/root/.cache/go-build`
- `swift build` → `/root/.swiftpm`
- `npm install`, `npm ci`, `yarn install`, `pnpm install` → `/root/.npm`
- `pip install` → `/root/.cache/pip`

The fix's `Old` is the raw source line (`df.Lines[inst.Line-1]`); `New` inserts the mount flag after the leading `RUN` token of that raw line.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/buildcache_test.go
package optimize

import "testing"

func dockerfileTarget(t *testing.T, src string) *Target {
	t.Helper()
	df := ParseDockerfile("Dockerfile", []byte(src))
	return &Target{Name: "app", Kind: KindDockerfile, Dir: ".", Dockerfile: df, Arch: "arm64"}
}

func TestBuildCacheFlagsMissingMount(t *testing.T) {
	tg := dockerfileTarget(t, "FROM rust:1\nRUN cargo build --release\n")
	got := buildCacheAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "build-cache" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.Op != FixReplaceLine {
		t.Fatalf("expected FixReplaceLine, got %+v", f.Fix)
	}
	if f.Fix.Old != "RUN cargo build --release" {
		t.Fatalf("fix.Old = %q", f.Fix.Old)
	}
	if f.Fix.New != "RUN --mount=type=cache,target=/root/.cargo cargo build --release" {
		t.Fatalf("fix.New = %q", f.Fix.New)
	}
}

func TestBuildCacheSilentWhenMountPresent(t *testing.T) {
	tg := dockerfileTarget(t, "FROM rust:1\nRUN --mount=type=cache,target=/root/.cargo cargo build\n")
	if got := buildCacheAnalyzer{}.Analyze(tg); len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestBuildCacheIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	if got := buildCacheAnalyzer{}.Analyze(tg); len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestBuildCache -v`
Expected: FAIL — `undefined: buildCacheAnalyzer`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/buildcache.go
package optimize

import (
	"fmt"
	"strings"
)

type buildCacheAnalyzer struct{}

func (buildCacheAnalyzer) ID() string { return "build-cache" }

// cacheRule maps a substring that signals a compiled-lang build to its cache mount target.
type cacheRule struct {
	match  string
	target string
}

var cacheRules = []cacheRule{
	{"cargo build", "/root/.cargo"},
	{"cargo fetch", "/root/.cargo"},
	{"go build", "/root/.cache/go-build"},
	{"go mod download", "/root/.cache/go-build"},
	{"swift build", "/root/.swiftpm"},
	{"npm install", "/root/.npm"},
	{"npm ci", "/root/.npm"},
	{"yarn install", "/root/.npm"},
	{"pnpm install", "/root/.npm"},
	{"pip install", "/root/.cache/pip"},
}

func (a buildCacheAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" {
			continue
		}
		if hasCacheMount(inst.Flags) {
			continue
		}
		rule, ok := matchCacheRule(inst.Args)
		if !ok {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]
		newLine := insertRunFlag(raw, fmt.Sprintf("--mount=type=cache,target=%s", rule.target))
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("%q runs without a build cache mount", rule.match),
			Detail: fmt.Sprintf("Add `--mount=type=cache,target=%s` to this RUN so %s reuses its cache across builds, "+
				"cutting rebuild time substantially.", rule.target, rule.match),
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
			Fix: &Fix{
				Description: fmt.Sprintf("add cache mount for %s", rule.match),
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         newLine,
			},
		})
	}
	return out
}

func hasCacheMount(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--mount=") && strings.Contains(f, "type=cache") {
			return true
		}
	}
	return false
}

func matchCacheRule(args string) (cacheRule, bool) {
	for _, r := range cacheRules {
		if strings.Contains(args, r.match) {
			return r, true
		}
	}
	return cacheRule{}, false
}

// insertRunFlag inserts flag right after the leading RUN token of a raw line,
// preserving the line's leading indentation.
func insertRunFlag(raw, flag string) string {
	indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
	body := strings.TrimLeft(raw, " \t")
	const run = "RUN "
	if strings.HasPrefix(body, run) {
		return indent + run + flag + " " + strings.TrimPrefix(body, run)
	}
	return raw // not a simple RUN line; leave unchanged
}
```

Then append to `DefaultAnalyzers()` in `analyzer.go`:

```go
func DefaultAnalyzers() []Analyzer {
	return []Analyzer{
		buildCacheAnalyzer{},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestBuildCache -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/buildcache.go internal/cli/optimize/analyzer.go internal/cli/optimize/buildcache_test.go
git add go/internal/cli/optimize/buildcache.go go/internal/cli/optimize/analyzer.go go/internal/cli/optimize/buildcache_test.go
git commit -m "feat(optimize): add build-cache analyzer"
```

---

## Task 7: releasedebug analyzer

**Files:**
- Create: `go/internal/cli/optimize/releasedebug.go`
- Modify: `go/internal/cli/optimize/analyzer.go` (append `releaseDebugAnalyzer{}`)
- Test: `go/internal/cli/optimize/releasedebug_test.go`

**Interfaces:**
- Consumes: `Target`, `Instruction`, `Finding`, `Fix`.
- Produces: `type releaseDebugAnalyzer struct{}` with `ID() == "release-debug"`.

Behavior over the Dockerfile:
1. `RUN` with `swift build` lacking `-c release` (and not `--configuration release`) → `SeverityWarning` + `FixReplaceLine` appending ` -c release` to the `swift build` occurrence in the raw line.
2. `RUN` with `cargo build` lacking `--release` → `SeverityWarning` + `FixReplaceLine` appending ` --release`.
3. WENDY_DEBUG wiring: scan all instructions. If any `ARG`/`ENV` defines `WENDY_DEBUG` but no `RUN` line references `WENDY_DEBUG`, emit one `SeverityInfo` finding (no fix) recommending gating optimization on it. If `WENDY_DEBUG` is never mentioned at all, emit nothing (silence — projects need not adopt it).

For the line-rewrite fixes, `Old` = raw source line; `New` = raw line with the flag inserted immediately after the build command token.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/releasedebug_test.go
package optimize

import "testing"

func TestReleaseDebugSwiftMissingReleaseFlag(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nRUN swift build\n")
	got := releaseDebugAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].Fix == nil || got[0].Fix.New != "RUN swift build -c release" {
		t.Fatalf("fix = %+v", got[0].Fix)
	}
}

func TestReleaseDebugSwiftSilentWithReleaseFlag(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nRUN swift build -c release\n")
	if got := releaseDebugAnalyzer{}.Analyze(tg); len(got) != 0 {
		t.Fatalf("got %d, want 0: %+v", len(got), got)
	}
}

func TestReleaseDebugWendyDebugDefinedButUnused(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nARG WENDY_DEBUG=0\nRUN swift build -c release\n")
	got := releaseDebugAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityInfo {
		t.Fatalf("want 1 info finding for WENDY_DEBUG, got %+v", got)
	}
	if got[0].Fix != nil {
		t.Fatalf("WENDY_DEBUG finding should be report-only")
	}
}

func TestReleaseDebugWendyDebugUsed(t *testing.T) {
	src := "FROM swift:6\nARG WENDY_DEBUG=0\n" +
		"RUN if [ \"$WENDY_DEBUG\" = \"1\" ]; then swift build; else swift build -c release; fi\n"
	tg := dockerfileTarget(t, src)
	for _, f := range releaseDebugAnalyzer{}.Analyze(tg) {
		if f.Severity == SeverityInfo {
			t.Fatalf("did not expect WENDY_DEBUG info finding: %+v", f)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestReleaseDebug -v`
Expected: FAIL — `undefined: releaseDebugAnalyzer`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/releasedebug.go
package optimize

import "strings"

type releaseDebugAnalyzer struct{}

func (releaseDebugAnalyzer) ID() string { return "release-debug" }

func (a releaseDebugAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding

	wendyDebugDefined := false
	wendyDebugUsed := false

	for _, inst := range t.Dockerfile.Instructions {
		joined := inst.Cmd + " " + inst.Args

		if (inst.Cmd == "ARG" || inst.Cmd == "ENV") && strings.Contains(inst.Args, "WENDY_DEBUG") {
			wendyDebugDefined = true
		}
		if inst.Cmd == "RUN" && strings.Contains(inst.Args, "WENDY_DEBUG") {
			wendyDebugUsed = true
		}

		if inst.Cmd != "RUN" {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]

		if strings.Contains(inst.Args, "swift build") &&
			!strings.Contains(inst.Args, "-c release") &&
			!strings.Contains(inst.Args, "--configuration release") {
			out = append(out, lineFlagFinding(a.ID(), t.Dockerfile.Path, inst.Line, raw,
				"swift build", "swift build -c release",
				"`swift build` defaults to a debug build. Add `-c release` so the shipped binary is optimized.",
				"add -c release to swift build"))
		}
		if strings.Contains(inst.Args, "cargo build") && !strings.Contains(inst.Args, "--release") {
			out = append(out, lineFlagFinding(a.ID(), t.Dockerfile.Path, inst.Line, raw,
				"cargo build", "cargo build --release",
				"`cargo build` defaults to a debug build. Add `--release` for an optimized binary.",
				"add --release to cargo build"))
		}
		_ = joined
	}

	if wendyDebugDefined && !wendyDebugUsed {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "WENDY_DEBUG is declared but never used to toggle the build",
			Detail: "WENDY_DEBUG is defined but no RUN step branches on it. Gate the optimization level on it, " +
				"e.g. release by default and a debug build only when WENDY_DEBUG=1.",
			Location: nil,
		})
	}

	return out
}

// lineFlagFinding builds a warning whose fix appends a flag to a build command on a raw line.
func lineFlagFinding(analyzer, file string, line int, raw, oldCmd, newCmd, detail, fixDesc string) Finding {
	newLine := strings.Replace(raw, oldCmd, newCmd, 1)
	return Finding{
		Analyzer: analyzer,
		Severity: SeverityWarning,
		Title:    oldCmd + " is a debug build",
		Detail:   detail,
		Location: &Loc{File: file, Line: line},
		Fix: &Fix{
			Description: fixDesc,
			Op:          FixReplaceLine,
			File:        file,
			Line:        line,
			Old:         raw,
			New:         newLine,
		},
	}
}
```

Then append `releaseDebugAnalyzer{}` to `DefaultAnalyzers()`:

```go
func DefaultAnalyzers() []Analyzer {
	return []Analyzer{
		buildCacheAnalyzer{},
		releaseDebugAnalyzer{},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestReleaseDebug -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/releasedebug.go internal/cli/optimize/analyzer.go internal/cli/optimize/releasedebug_test.go
git add go/internal/cli/optimize/releasedebug.go go/internal/cli/optimize/analyzer.go go/internal/cli/optimize/releasedebug_test.go
git commit -m "feat(optimize): add release-debug analyzer"
```

---

## Task 8: cudaml analyzer

**Files:**
- Create: `go/internal/cli/optimize/cudaml.go`
- Modify: `go/internal/cli/optimize/analyzer.go` (append `cudaMLAnalyzer{}`)
- Test: `go/internal/cli/optimize/cudaml_test.go`

**Interfaces:**
- Consumes: `Target`, `Requirements`, `Instruction`, `appconfig.AppConfig`, `appconfig.EntitlementGPU`.
- Produces: `type cudaMLAnalyzer struct{}` with `ID() == "cuda-ml"`. All findings report-only (`Fix == nil`).

Behavior:
1. `hasGPUEntitlement(cfg)` = true if any `cfg.Entitlements[i].Type == appconfig.EntitlementGPU`.
2. If `requirements.txt` lists a known ML package (`torch`, `tensorflow`, `onnxruntime`, `onnxruntime-gpu`) AND the wheel is CPU-only (package `LocalLabel == "cpu"`, OR any IndexURL contains `/whl/cpu`) AND `hasGPUEntitlement` → `SeverityWarning`: "GPU entitlement set but a CPU-only ML wheel is pinned."
3. If a known GPU ML package (`onnxruntime-gpu`, or torch with `LocalLabel` starting `cu`) is present AND NOT `hasGPUEntitlement` → `SeverityWarning`: "CUDA wheel pinned but no gpu entitlement declared in wendy.json."
4. Dockerfile `FROM` whose image arg contains `nvidia/cuda` while `t.Arch == "arm64"` → `SeverityWarning`: "x86 CUDA base image on an arm64 target; Jetson needs an L4T base (e.g. nvcr.io/nvidia/l4t-*)."

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/cudaml_test.go
package optimize

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func gpuCfg() *appconfig.AppConfig {
	return &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}}
}

func TestCudaCPUWheelWithGPUEntitlement(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       gpuCfg(),
		Requirements: ParseRequirements("requirements.txt", []byte("torch==2.3.0+cpu\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning, got %+v", got)
	}
	if got[0].Fix != nil {
		t.Fatalf("cuda findings are report-only")
	}
}

func TestCudaGPUWheelWithoutEntitlement(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       &appconfig.AppConfig{},
		Requirements: ParseRequirements("requirements.txt", []byte("onnxruntime-gpu\n")),
	}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning, got %+v", got)
	}
}

func TestCudaX86BaseImageOnArm(t *testing.T) {
	tg := dockerfileTarget(t, "FROM nvidia/cuda:12.4.0-runtime-ubuntu22.04\n")
	tg.Config = &appconfig.AppConfig{}
	got := cudaMLAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("want 1 warning for x86 cuda base, got %+v", got)
	}
}

func TestCudaSilentWhenAligned(t *testing.T) {
	tg := &Target{
		Name:         "app",
		Kind:         KindDockerfile,
		Arch:         "arm64",
		Config:       gpuCfg(),
		Requirements: ParseRequirements("requirements.txt", []byte("numpy>=1.26\n")),
	}
	if got := cudaMLAnalyzer{}.Analyze(tg); len(got) != 0 {
		t.Fatalf("want 0, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestCuda -v`
Expected: FAIL — `undefined: cudaMLAnalyzer`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/cudaml.go
package optimize

import (
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

type cudaMLAnalyzer struct{}

func (cudaMLAnalyzer) ID() string { return "cuda-ml" }

var mlPackages = map[string]bool{
	"torch":            true,
	"tensorflow":       true,
	"onnxruntime":      true,
	"onnxruntime-gpu":  true,
	"tensorflow-gpu":   true,
}

func hasGPUEntitlement(cfg *appconfig.AppConfig) bool {
	if cfg == nil {
		return false
	}
	for _, e := range cfg.Entitlements {
		if e.Type == appconfig.EntitlementGPU {
			return true
		}
	}
	return false
}

func (a cudaMLAnalyzer) Analyze(t *Target) []Finding {
	var out []Finding
	gpu := hasGPUEntitlement(t.Config)

	if t.Requirements != nil {
		cpuIndex := false
		for _, u := range t.Requirements.IndexURLs {
			if strings.Contains(u, "/whl/cpu") {
				cpuIndex = true
			}
		}
		for _, p := range t.Requirements.Packages {
			if !mlPackages[p.Name] {
				continue
			}
			isCPU := p.LocalLabel == "cpu" || cpuIndex
			isGPU := p.Name == "onnxruntime-gpu" || p.Name == "tensorflow-gpu" || strings.HasPrefix(p.LocalLabel, "cu")

			if isCPU && gpu {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "GPU entitlement set but a CPU-only ML wheel is pinned",
					Detail: "wendy.json declares the gpu entitlement, but " + p.Name + " resolves to a CPU-only build. " +
						"Pin a CUDA wheel matching the device's JetPack/CUDA version to actually use the GPU.",
					Location: &Loc{File: t.Requirements.Path, Line: p.Line},
				})
			}
			if isGPU && !gpu {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "CUDA ML wheel pinned but no gpu entitlement declared",
					Detail: p.Name + " is a CUDA build, but wendy.json does not declare the gpu entitlement, " +
						"so the container will not get GPU access on the device.",
					Location: &Loc{File: t.Requirements.Path, Line: p.Line},
				})
			}
		}
	}

	if t.Dockerfile != nil && t.Arch == "arm64" {
		for _, inst := range t.Dockerfile.Instructions {
			if inst.Cmd == "FROM" && strings.Contains(inst.Args, "nvidia/cuda") {
				out = append(out, Finding{
					Analyzer: a.ID(),
					Severity: SeverityWarning,
					Title:    "x86 CUDA base image on an arm64 target",
					Detail: "nvidia/cuda images are x86-first. On a Jetson (arm64) use an L4T base such as " +
						"nvcr.io/nvidia/l4t-base or an l4t-* runtime image that matches the device JetPack.",
					Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
				})
			}
		}
	}

	return out
}
```

Append `cudaMLAnalyzer{}` to `DefaultAnalyzers()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestCuda -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/cudaml.go internal/cli/optimize/analyzer.go internal/cli/optimize/cudaml_test.go
git add go/internal/cli/optimize/cudaml.go go/internal/cli/optimize/analyzer.go go/internal/cli/optimize/cudaml_test.go
git commit -m "feat(optimize): add cuda-ml analyzer"
```

---

## Task 9: archimage analyzer

**Files:**
- Create: `go/internal/cli/optimize/archimage.go`
- Modify: `go/internal/cli/optimize/analyzer.go` (append `archImageAnalyzer{}`)
- Test: `go/internal/cli/optimize/archimage_test.go`

**Interfaces:**
- Consumes: `Target`, `Instruction`, `Finding`, `Fix`.
- Produces: `type archImageAnalyzer struct{}` with `ID() == "arch-image"`.

Behavior:
1. Any `FROM` with `--platform=linux/amd64` (or `--platform=linux/x86_64`) while `t.Arch == "arm64"` → `SeverityError`, report-only: "amd64 base image on arm64 target — runs under QEMU or fails."
2. Missing `.dockerignore` in `t.Dir` (only for Dockerfile/compose targets) → `SeverityWarning` + `FixCreateFile` writing a default `.dockerignore`. Default contents constant `defaultDockerignore`.
3. Single-stage build (exactly one `FROM` and no `AS` stage names) that also installs a build toolchain (a `RUN` whose Args contains `apt-get install` with a `-dev` package, or `build-essential`, or `cargo`/`go build`/`swift build`) → `SeverityInfo`, report-only: suggest multi-stage.

`.dockerignore` existence is checked via `fileExists(filepath.Join(t.Dir, ".dockerignore"))`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/archimage_test.go
package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchAmd64OnArm(t *testing.T) {
	tg := dockerfileTarget(t, "FROM --platform=linux/amd64 python:3.12-slim\n")
	tg.Dir = t.TempDir()
	writeFile(t, tg.Dir, ".dockerignore", ".git\n") // present, so no .dockerignore finding
	got := archImageAnalyzer{}.Analyze(tg)
	var sawErr bool
	for _, f := range got {
		if f.Severity == SeverityError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("expected an error finding for amd64-on-arm, got %+v", got)
	}
}

func TestArchMissingDockerignoreFixable(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12-slim\n")
	tg.Dir = t.TempDir() // no .dockerignore
	got := archImageAnalyzer{}.Analyze(tg)
	var fix *Fix
	for i := range got {
		if got[i].Title == "No .dockerignore" {
			fix = got[i].Fix
		}
	}
	if fix == nil || fix.Op != FixCreateFile {
		t.Fatalf("expected FixCreateFile for missing .dockerignore, got %+v", got)
	}
	if fix.File != filepath.Join(tg.Dir, ".dockerignore") {
		t.Fatalf("fix.File = %q", fix.File)
	}
}

func TestArchDockerignorePresentNoFinding(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12-slim\n")
	tg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(tg.Dir, ".dockerignore"), []byte(".git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range archImageAnalyzer{}.Analyze(tg) {
		if f.Title == "No .dockerignore" {
			t.Fatalf("did not expect a .dockerignore finding")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestArch -v`
Expected: FAIL — `undefined: archImageAnalyzer`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/archimage.go
package optimize

import (
	"path/filepath"
	"strings"
)

type archImageAnalyzer struct{}

func (archImageAnalyzer) ID() string { return "arch-image" }

const defaultDockerignore = `.git
**/.build
**/.swiftpm
node_modules
target
__pycache__
*.pyc
.venv
dist
build
`

func (a archImageAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding

	fromCount := 0
	hasStageName := false
	installsToolchain := false

	for _, inst := range t.Dockerfile.Instructions {
		switch inst.Cmd {
		case "FROM":
			fromCount++
			if strings.Contains(strings.ToUpper(" "+inst.Args+" "), " AS ") {
				hasStageName = true
			}
			if t.Arch == "arm64" {
				for _, fl := range inst.Flags {
					if fl == "--platform=linux/amd64" || fl == "--platform=linux/x86_64" {
						out = append(out, Finding{
							Analyzer: a.ID(),
							Severity: SeverityError,
							Title:    "amd64 base image on arm64 target",
							Detail: "This FROM forces linux/amd64 but the target device is arm64. It will run under " +
								"QEMU emulation (slow) or fail to start. Use an arm64/multi-arch base image.",
							Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
						})
					}
				}
			}
		case "RUN":
			if strings.Contains(inst.Args, "build-essential") ||
				(strings.Contains(inst.Args, "apt-get install") && strings.Contains(inst.Args, "-dev")) ||
				strings.Contains(inst.Args, "cargo build") ||
				strings.Contains(inst.Args, "go build") ||
				strings.Contains(inst.Args, "swift build") {
				installsToolchain = true
			}
		}
	}

	if !fileExists(filepath.Join(t.Dir, ".dockerignore")) {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    "No .dockerignore",
			Detail: "Without a .dockerignore the whole context (including .git and build artifacts) is sent to the " +
				"builder, slowing builds and bloating layers.",
			Location: nil,
			Fix: &Fix{
				Description: "create a default .dockerignore",
				Op:          FixCreateFile,
				File:        filepath.Join(t.Dir, ".dockerignore"),
				New:         defaultDockerignore,
			},
		})
	}

	if fromCount == 1 && !hasStageName && installsToolchain {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "Single-stage build ships its build toolchain",
			Detail: "This image builds and runs in one stage, leaving compilers and -dev packages in the deployed " +
				"image. A multi-stage build that copies only the final artifact into a slim runtime stage is much smaller.",
			Location: nil,
		})
	}

	return out
}
```

Append `archImageAnalyzer{}` to `DefaultAnalyzers()`. After this task `DefaultAnalyzers()` returns all four: `buildCacheAnalyzer{}, releaseDebugAnalyzer{}, cudaMLAnalyzer{}, archImageAnalyzer{}`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestArch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/archimage.go internal/cli/optimize/analyzer.go internal/cli/optimize/archimage_test.go
git add go/internal/cli/optimize/archimage.go go/internal/cli/optimize/analyzer.go go/internal/cli/optimize/archimage_test.go
git commit -m "feat(optimize): add arch-image analyzer"
```

---

## Task 10: Fix applier (idempotent)

**Files:**
- Create: `go/internal/cli/optimize/fix.go`
- Test: `go/internal/cli/optimize/fix_test.go`

**Interfaces:**
- Consumes: `Finding`, `Fix`, `FixOp`.
- Produces:
  - `type AppliedFix struct { Fix Fix; Applied bool; Reason string }` — `Applied=false` with a `Reason` when skipped (already applied / line mismatch / file exists).
  - `func ApplyFixes(findings []Finding) ([]AppliedFix, error)` — applies every finding's non-nil `Fix`. `FixCreateFile`: write `New` if file does not exist (else skip "file exists"). `FixReplaceLine`: read file, if line `Line` equals `Old` replace with `New` and write back (else skip "already applied or line changed"). Idempotent: re-running applies nothing.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/fix_test.go
package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyReplaceLineIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM rust:1\nRUN cargo build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Finding{Fix: &Fix{
		Op: FixReplaceLine, File: p, Line: 2,
		Old: "RUN cargo build",
		New: "RUN --mount=type=cache,target=/root/.cargo cargo build",
	}}

	applied, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !applied[0].Applied {
		t.Fatalf("first apply = %+v", applied)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "FROM rust:1\nRUN --mount=type=cache,target=/root/.cargo cargo build\n" {
		t.Fatalf("file after fix = %q", string(data))
	}

	// Re-running must not apply again.
	applied2, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if applied2[0].Applied {
		t.Fatalf("second apply should be skipped, got %+v", applied2[0])
	}
}

func TestApplyCreateFileSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".dockerignore")
	f := Finding{Fix: &Fix{Op: FixCreateFile, File: p, New: ".git\n"}}

	if _, err := ApplyFixes([]Finding{f}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(p) {
		t.Fatalf("file not created")
	}
	applied, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if applied[0].Applied {
		t.Fatalf("second create should skip existing file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestApply -v`
Expected: FAIL — `undefined: ApplyFixes`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/fix.go
package optimize

import (
	"fmt"
	"os"
	"strings"
)

// AppliedFix records the outcome of applying one Fix.
type AppliedFix struct {
	Fix     Fix
	Applied bool
	Reason  string // populated when Applied is false
}

// ApplyFixes applies every finding's non-nil Fix. It is idempotent.
func ApplyFixes(findings []Finding) ([]AppliedFix, error) {
	var results []AppliedFix
	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
		fx := *f.Fix
		switch fx.Op {
		case FixCreateFile:
			if fileExists(fx.File) {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "file already exists"})
				continue
			}
			if err := os.WriteFile(fx.File, []byte(fx.New), 0o644); err != nil {
				return results, fmt.Errorf("creating %s: %w", fx.File, err)
			}
			results = append(results, AppliedFix{Fix: fx, Applied: true})

		case FixReplaceLine:
			data, err := os.ReadFile(fx.File)
			if err != nil {
				return results, fmt.Errorf("reading %s: %w", fx.File, err)
			}
			text := strings.ReplaceAll(string(data), "\r\n", "\n")
			trailingNL := strings.HasSuffix(text, "\n")
			body := text
			if trailingNL {
				body = strings.TrimSuffix(text, "\n")
			}
			lines := strings.Split(body, "\n")
			idx := fx.Line - 1
			if idx < 0 || idx >= len(lines) || lines[idx] != fx.Old {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "already applied or line changed"})
				continue
			}
			lines[idx] = fx.New
			out := strings.Join(lines, "\n")
			if trailingNL {
				out += "\n"
			}
			if err := os.WriteFile(fx.File, []byte(out), 0o644); err != nil {
				return results, fmt.Errorf("writing %s: %w", fx.File, err)
			}
			results = append(results, AppliedFix{Fix: fx, Applied: true})

		default:
			results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "unknown fix op"})
		}
	}
	return results, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestApply -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/fix.go internal/cli/optimize/fix_test.go
git add go/internal/cli/optimize/fix.go go/internal/cli/optimize/fix_test.go
git commit -m "feat(optimize): add idempotent fix applier"
```

---

## Task 11: Reporter (human + JSON shape)

**Files:**
- Create: `go/internal/cli/optimize/report.go`
- Test: `go/internal/cli/optimize/report_test.go`

**Interfaces:**
- Consumes: `Target`, `Finding`, `Severity`.
- Produces:
  - `type Report struct { Targets []ReportTarget; Findings []Finding }` where `type ReportTarget struct { Name string; Kind string }`.
  - `func BuildReport(targets []Target, findings []Finding) Report`.
  - `func (r Report) Counts() (info, warning, errc, fixable int)`.
  - `func (r Report) MaxSeverity() Severity` — highest severity present (defaults to `SeverityInfo` when empty).
  - `func RenderHuman(r Report) string` — plain-text report grouped by target (no lipgloss color codes in the returned string, so it is testable; the command layer wraps lines in color). Each finding line: `  <sev>  <analyzer>:<line>  <title>[  (fixable)]`. Findings with no `Location` omit `:<line>`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/report_test.go
package optimize

import (
	"strings"
	"testing"
)

func sampleReport() Report {
	targets := []Target{{Name: "app", Kind: KindDockerfile}}
	findings := []Finding{
		{Analyzer: "arch-image", Severity: SeverityError, Title: "amd64 base", Location: &Loc{File: "Dockerfile", Line: 1}},
		{Analyzer: "build-cache", Severity: SeverityWarning, Title: "no cache", Location: &Loc{File: "Dockerfile", Line: 4}, Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "arch-image", Severity: SeverityWarning, Title: "No .dockerignore", Fix: &Fix{Op: FixCreateFile}},
	}
	return BuildReport(targets, findings)
}

func TestCountsAndMaxSeverity(t *testing.T) {
	r := sampleReport()
	info, warn, errc, fixable := r.Counts()
	if info != 0 || warn != 2 || errc != 1 || fixable != 2 {
		t.Fatalf("counts = info:%d warn:%d err:%d fixable:%d", info, warn, errc, fixable)
	}
	if r.MaxSeverity() != SeverityError {
		t.Fatalf("MaxSeverity = %v, want error", r.MaxSeverity())
	}
}

func TestRenderHuman(t *testing.T) {
	out := RenderHuman(sampleReport())
	if !strings.Contains(out, "app (dockerfile)") {
		t.Fatalf("missing target header:\n%s", out)
	}
	if !strings.Contains(out, "build-cache:4") || !strings.Contains(out, "(fixable)") {
		t.Fatalf("missing fixable build-cache line:\n%s", out)
	}
	if strings.Contains(out, "arch-image:0") {
		t.Fatalf("location-less finding should not print :0\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run 'TestCounts|TestRenderHuman' -v`
Expected: FAIL — `undefined: BuildReport`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/report.go
package optimize

import (
	"fmt"
	"strings"
)

// ReportTarget is the lightweight target view embedded in a report.
type ReportTarget struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Report is the rendered/serialized output of an optimize run.
type Report struct {
	Targets  []ReportTarget `json:"targets"`
	Findings []Finding      `json:"findings"`
}

// BuildReport assembles a Report from targets and findings.
func BuildReport(targets []Target, findings []Finding) Report {
	rt := make([]ReportTarget, 0, len(targets))
	for _, t := range targets {
		rt = append(rt, ReportTarget{Name: t.Name, Kind: t.Kind.String()})
	}
	return Report{Targets: rt, Findings: findings}
}

// Counts returns finding counts by severity plus the number of fixable findings.
func (r Report) Counts() (info, warning, errc, fixable int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityInfo:
			info++
		case SeverityWarning:
			warning++
		case SeverityError:
			errc++
		}
		if f.Fix != nil {
			fixable++
		}
	}
	return
}

// MaxSeverity returns the highest severity in the report (SeverityInfo if empty).
func (r Report) MaxSeverity() Severity {
	max := SeverityInfo
	for _, f := range r.Findings {
		if f.Severity > max {
			max = f.Severity
		}
	}
	return max
}

// RenderHuman renders a plain-text report grouped by target Name.
func RenderHuman(r Report) string {
	var b strings.Builder

	// Group findings by the target each was produced under. Findings carry no
	// back-pointer, so render per target header then all findings (single-target
	// is the common case; multi-target groups by Location file when present).
	for _, t := range r.Targets {
		fmt.Fprintf(&b, "%s (%s)\n", t.Name, t.Kind)
	}
	for _, f := range r.Findings {
		loc := ""
		if f.Location != nil {
			loc = fmt.Sprintf(":%d", f.Location.Line)
		}
		fixable := ""
		if f.Fix != nil {
			fixable = "  (fixable)"
		}
		fmt.Fprintf(&b, "  %-7s  %s%s  %s%s\n", f.Severity.String(), f.Analyzer, loc, f.Title, fixable)
	}

	info, warn, errc, fixable := r.Counts()
	total := info + warn + errc
	fmt.Fprintf(&b, "\n%d findings (%d errors, %d warnings, %d info)", total, errc, warn, info)
	if fixable > 0 {
		fmt.Fprintf(&b, "  ·  %d fixable — run with --fix", fixable)
	}
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run 'TestCounts|TestRenderHuman' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/report.go internal/cli/optimize/report_test.go
git add go/internal/cli/optimize/report.go go/internal/cli/optimize/report_test.go
git commit -m "feat(optimize): add report builder and human renderer"
```

---

## Task 12: Agentic bundle

**Files:**
- Create: `go/internal/cli/optimize/bundle.go`
- Test: `go/internal/cli/optimize/bundle_test.go`

**Interfaces:**
- Consumes: `Target`, `Finding`, `Report` (for findings reuse).
- Produces:
  - `type BundleTarget struct { Name string; Kind string; Dockerfile string; RequirementsTxt *string }`.
  - `type BundleProject struct { Dir string; AppID string; Platform string; Arch string }`.
  - `type Bundle struct { Schema int; Project BundleProject; Targets []BundleTarget; WendyJSON string; StaticFindings []Finding; Instructions string }`.
  - `func BuildBundle(dir, wendyJSON string, targets []Target, findings []Finding) Bundle` — `Schema = 1`; `WendyJSON` is the verbatim wendy.json text (empty if none); each target's `Dockerfile` is the raw source (joined `Lines`), `RequirementsTxt` is a pointer to the raw text or nil; `Project.AppID/Platform` come from the first target's `Config` if present; `Arch` from the first target; `Instructions` is the constant prompt template `bundleInstructions`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/optimize/bundle_test.go
package optimize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestBuildBundle(t *testing.T) {
	df := ParseDockerfile("Dockerfile", []byte("FROM rust:1\nRUN cargo build\n"))
	targets := []Target{{
		Name:       "app",
		Kind:       KindDockerfile,
		Dir:        ".",
		Dockerfile: df,
		Arch:       "arm64",
		Config:     &appconfig.AppConfig{AppID: "demo", Platform: "wendyos"},
	}}
	findings := []Finding{{Analyzer: "build-cache", Severity: SeverityWarning, Title: "no cache"}}

	b := BuildBundle(".", "{\"appId\":\"demo\"}", targets, findings)

	if b.Schema != 1 {
		t.Fatalf("schema = %d, want 1", b.Schema)
	}
	if b.Project.AppID != "demo" || b.Project.Arch != "arm64" {
		t.Fatalf("project = %+v", b.Project)
	}
	if len(b.Targets) != 1 || !strings.Contains(b.Targets[0].Dockerfile, "cargo build") {
		t.Fatalf("targets = %+v", b.Targets)
	}
	if b.Targets[0].RequirementsTxt != nil {
		t.Fatalf("expected nil RequirementsTxt")
	}
	if len(b.StaticFindings) != 1 || b.Instructions == "" {
		t.Fatalf("findings/instructions missing")
	}

	// Must round-trip through JSON.
	if _, err := json.Marshal(b); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/optimize/ -run TestBuildBundle -v`
Expected: FAIL — `undefined: BuildBundle`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/optimize/bundle.go
package optimize

import "strings"

// BundleTarget is one target's verbatim inputs for the agent.
type BundleTarget struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Dockerfile      string  `json:"dockerfile"`
	RequirementsTxt *string `json:"requirements_txt"`
}

// BundleProject is project-level context for the agent.
type BundleProject struct {
	Dir      string `json:"dir"`
	AppID    string `json:"app_id"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

// Bundle is the --agentic output: static findings plus verbatim context.
type Bundle struct {
	Schema         int           `json:"schema"`
	Project        BundleProject `json:"project"`
	Targets        []BundleTarget `json:"targets"`
	WendyJSON      string        `json:"wendy_json"`
	StaticFindings []Finding     `json:"static_findings"`
	Instructions   string        `json:"instructions"`
}

const bundleInstructions = "You are optimizing a Wendy edge-device app build. " +
	"The static_findings below were already detected by deterministic rules — do not just repeat them. " +
	"Using the verbatim Dockerfile, requirements.txt, and wendy.json, look for the contextual optimizations rules cannot catch: " +
	"converting to multi-stage builds, choosing the correct CUDA/PyTorch wheel for the device's JetPack/CUDA version, " +
	"swapping to a slimmer or arch-correct base image, consolidating layers, and removing build-only deps from the runtime image. " +
	"Propose concrete unified diffs against the files provided. The target architecture is given in project.arch."

// BuildBundle assembles the agentic context bundle.
func BuildBundle(dir, wendyJSON string, targets []Target, findings []Finding) Bundle {
	b := Bundle{
		Schema:         1,
		Project:        BundleProject{Dir: dir},
		WendyJSON:      wendyJSON,
		StaticFindings: findings,
		Instructions:   bundleInstructions,
	}
	if len(targets) > 0 {
		b.Project.Arch = targets[0].Arch
		if cfg := targets[0].Config; cfg != nil {
			b.Project.AppID = cfg.AppID
			b.Project.Platform = cfg.Platform
		}
	}
	for _, t := range targets {
		bt := BundleTarget{Name: t.Name, Kind: t.Kind.String()}
		if t.Dockerfile != nil {
			bt.Dockerfile = strings.Join(t.Dockerfile.Lines, "\n")
		}
		if t.Requirements != nil {
			raw := strings.Join(requirementLinesPlaceholder(t.Requirements), "\n")
			_ = raw
		}
		b.Targets = append(b.Targets, bt)
	}
	return b
}

// requirementLinesPlaceholder exists only so the bundle can attach raw
// requirements text. Requirements does not retain raw lines, so the command
// layer passes raw file bytes via the target loader; here we return nil.
func requirementLinesPlaceholder(_ *Requirements) []string { return nil }
```

NOTE during implementation: to populate `RequirementsTxt` with verbatim text, add a `Raw string` field to `Requirements` in Task 3's struct (set in `ParseRequirements` to the original text) and use it here instead of the placeholder helper. The placeholder keeps Task 12 self-contained; fold the `Raw` field addition into this task and delete `requirementLinesPlaceholder`. Updated `BuildBundle` requirements branch:

```go
		if t.Requirements != nil {
			raw := t.Requirements.Raw
			bt.RequirementsTxt = &raw
		}
```

And in `requirements.go`, add `Raw string` to the `Requirements` struct and set `r.Raw = string(data)` at the top of `ParseRequirements`. Update the Task 3 test only if it asserts struct equality (it does not).

- [ ] **Step 2b: Add the `Raw` field**

Edit `go/internal/cli/optimize/requirements.go`: add `Raw string` field to `Requirements`, set `r := &Requirements{Path: path, Raw: string(data)}`. Remove `requirementLinesPlaceholder` and use the `Raw`-based branch above in `bundle.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/optimize/ -run TestBuildBundle -v && go test ./internal/cli/optimize/ -v`
Expected: PASS (all engine tests green).

- [ ] **Step 5: Commit**

```bash
cd go && gofmt -w internal/cli/optimize/bundle.go internal/cli/optimize/requirements.go internal/cli/optimize/bundle_test.go
git add go/internal/cli/optimize/bundle.go go/internal/cli/optimize/requirements.go go/internal/cli/optimize/bundle_test.go
git commit -m "feat(optimize): add agentic context bundle"
```

---

## Task 13: Command wiring + exit codes (integration)

**Files:**
- Create: `go/internal/cli/commands/optimize.go`
- Modify: `go/internal/cli/commands/project.go:34-42` (register `newOptimizeCmd()`)
- Test: `go/internal/cli/commands/optimize_test.go`

**Interfaces:**
- Consumes: `optimize` package (`DiscoverTargets`, `DefaultAnalyzers`, `Analyze`, `BuildReport`, `RenderHuman`, `ApplyFixes`, `BuildBundle`, `ParseSeverity`, `resolveArch` is unexported — instead the command passes the `--arch` flag straight to `DiscoverTargets` after defaulting it inline), `appconfig.LoadFromFile`, global `jsonOutput`, `tui` colors, `cliSuccess`.
- Produces: `func newOptimizeCmd() *cobra.Command`. Behavior: load wendy.json if present (nil if absent — optimize must work without it for native/dockerfile-only projects), resolve arch (`--arch` else `"arm64"`), discover targets, run analyzers, then branch on flags:
  - `--agentic`: print `BuildBundle(...)` as indented JSON; exit 0.
  - else build report; if `--json` print report JSON; else print `RenderHuman`.
  - `--fix`: call `ApplyFixes(findings)`, print applied summary, then recompute findings by re-discovering + re-analyzing so the exit code reflects the post-fix state.
  - Exit code: compare `report.MaxSeverity()` against the `--severity` threshold (default `warning`). At/above → return a non-zero-exit error via `newExitError(1)` style. Use Cobra's `SilenceUsage`/`SilenceErrors` already set on root; to force exit code 1 without a noisy error, set `cmd.SilenceErrors = true` and return a sentinel error whose message is empty, OR call `os.Exit`. Implementation detail: define `errFindings = errors.New("")` is ugly — instead the command sets a package var and `main.go` already maps errors to exit 1. Simplest correct approach: return a typed error `&exitCodeError{code: 1}` and check `main.go`. Since modifying main.go is out of scope, the command instead prints the report and calls `os.Exit(1)` directly after flushing output when findings ≥ threshold and not in `--json`/`--agentic` machine modes... 

  DECISION (lock this in): the command returns `nil` always for output, and signals CI failure via `os.Exit`. Concretely: after rendering, if `!jsonOutput` and findings ≥ threshold, call `os.Exit(1)`. For `--json`/`--agentic`, still `os.Exit(1)` when findings ≥ threshold so CI works uniformly. Execution errors return a normal `error` (Cobra → exit 1 via main's handler, which is acceptable; reserve `os.Exit(2)` for them by calling `os.Exit(2)` on load/discovery failure). Because `os.Exit` bypasses test assertions, the testable core is extracted into `runOptimize(opts) (Report, []AppliedFix, error)` and the Cobra `RunE` is a thin wrapper that calls it and then decides exit code. Tests target `runOptimize`.

So produce ALSO:
  - `type optimizeOptions struct { Dir string; Arch string; Fix bool; Agentic bool }`.
  - `func runOptimize(opts optimizeOptions) (optimize.Report, []optimize.AppliedFix, error)` — the testable core (no os.Exit, no printing). Loads config, discovers, analyzes; if `opts.Fix`, applies fixes then re-discovers+re-analyzes and returns the post-fix report plus the applied list.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/commands/optimize_test.go
package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOptFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestRunOptimizeFindsAndFixes(t *testing.T) {
	dir := t.TempDir()
	writeOptFile(t, dir, "Dockerfile", "FROM rust:1\nRUN cargo build\n")

	rep, _, err := runOptimize(optimizeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("runOptimize: %v", err)
	}
	_, warn, _, fixable := rep.Counts()
	if warn == 0 || fixable == 0 {
		t.Fatalf("expected warnings and fixable findings, got %+v", rep.Counts)
	}

	// With --fix, the cache-mount + .dockerignore fixes apply, and a re-run drops fixable count.
	repFixed, applied, err := runOptimize(optimizeOptions{Dir: dir, Fix: true})
	if err != nil {
		t.Fatalf("runOptimize fix: %v", err)
	}
	var anyApplied bool
	for _, a := range applied {
		if a.Applied {
			anyApplied = true
		}
	}
	if !anyApplied {
		t.Fatalf("expected at least one applied fix")
	}
	// Re-running clean should report fewer fixable findings than the first pass.
	_, _, _, fixableAfter := repFixed.Counts()
	if fixableAfter >= fixable {
		t.Fatalf("fixable did not decrease after --fix: before=%d after=%d", fixable, fixableAfter)
	}
}

func TestRunOptimizeNoProject(t *testing.T) {
	dir := t.TempDir()
	rep, _, err := runOptimize(optimizeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("runOptimize: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("expected no findings for empty dir, got %+v", rep.Findings)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestRunOptimize -v`
Expected: FAIL — `undefined: runOptimize`.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/commands/optimize.go
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/optimize"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

type optimizeOptions struct {
	Dir     string
	Arch    string
	Fix     bool
	Agentic bool
}

// loadOptConfig loads wendy.json from dir, returning (nil, "") when absent.
func loadOptConfig(dir string) (*appconfig.AppConfig, string) {
	p := filepath.Join(dir, "wendy.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, ""
	}
	cfg, err := appconfig.LoadFromBytes(data)
	if err != nil {
		return nil, string(data)
	}
	return cfg, string(data)
}

// runOptimize is the testable core: no printing, no os.Exit.
func runOptimize(opts optimizeOptions) (optimize.Report, []optimize.AppliedFix, error) {
	arch := opts.Arch
	if arch == "" {
		arch = "arm64"
	}
	cfg, _ := loadOptConfig(opts.Dir)

	targets, err := optimize.DiscoverTargets(opts.Dir, cfg, arch)
	if err != nil {
		return optimize.Report{}, nil, err
	}
	findings := optimize.Analyze(targets, optimize.DefaultAnalyzers())

	var applied []optimize.AppliedFix
	if opts.Fix {
		applied, err = optimize.ApplyFixes(findings)
		if err != nil {
			return optimize.Report{}, applied, err
		}
		// Recompute post-fix so callers see the residual state.
		targets, err = optimize.DiscoverTargets(opts.Dir, cfg, arch)
		if err != nil {
			return optimize.Report{}, applied, err
		}
		findings = optimize.Analyze(targets, optimize.DefaultAnalyzers())
	}

	return optimize.BuildReport(targets, findings), applied, nil
}

func newOptimizeCmd() *cobra.Command {
	var (
		archFlag     string
		fixFlag      bool
		agenticFlag  bool
		severityFlag string
	)

	cmd := &cobra.Command{
		Use:   "optimize",
		Short: "Analyze the project's build config for missed optimizations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			threshold, err := optimize.ParseSeverity(severityFlag)
			if err != nil {
				return err
			}
			opts := optimizeOptions{Dir: cwd, Arch: archFlag, Fix: fixFlag, Agentic: agenticFlag}

			if agenticFlag {
				arch := archFlag
				if arch == "" {
					arch = "arm64"
				}
				cfg, raw := loadOptConfig(cwd)
				targets, derr := optimize.DiscoverTargets(cwd, cfg, arch)
				if derr != nil {
					os.Exit(2)
				}
				findings := optimize.Analyze(targets, optimize.DefaultAnalyzers())
				bundle := optimize.BuildBundle(cwd, raw, targets, findings)
				data, merr := json.MarshalIndent(bundle, "", "  ")
				if merr != nil {
					return merr
				}
				cmd.Println(string(data))
				return nil
			}

			rep, applied, rerr := runOptimize(opts)
			if rerr != nil {
				cmd.PrintErrln(rerr.Error())
				os.Exit(2)
			}

			if fixFlag {
				cliSuccess("Applied fixes:")
				for _, a := range applied {
					if a.Applied {
						cmd.Printf("  %s — %s\n", a.Fix.File, a.Fix.Description)
					} else {
						cmd.Printf("  %s — skipped (%s)\n", a.Fix.File, a.Reason)
					}
				}
			}

			if jsonOutput {
				data, merr := json.MarshalIndent(rep, "", "  ")
				if merr != nil {
					return merr
				}
				cmd.Println(string(data))
			} else {
				cmd.Print(optimize.RenderHuman(rep))
			}

			if rep.MaxSeverity() >= threshold && len(rep.Findings) > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&archFlag, "arch", "", "Target architecture (default arm64)")
	cmd.Flags().BoolVar(&fixFlag, "fix", false, "Apply safe, deterministic fixes")
	cmd.Flags().BoolVar(&agenticFlag, "agentic", false, "Emit an agent context bundle instead of a report")
	cmd.Flags().StringVar(&severityFlag, "severity", "warning", "Minimum severity that triggers a non-zero exit (info|warning|error)")
	return cmd
}
```

Then register in `project.go`:

```go
func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage Wendy project configuration",
	}

	cmd.AddCommand(newEntitlementsCmd())
	cmd.AddCommand(newOptimizeCmd())
	return cmd
}
```

NOTE: confirm `appconfig.LoadFromBytes` is exported (the explorer reported `LoadFromFile` calls `LoadFromBytes`). If it is unexported, use `appconfig.LoadFromFile(filepath.Join(dir, "wendy.json"))` inside `loadOptConfig` and read the raw bytes separately with `os.ReadFile`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run TestRunOptimize -v && go test ./internal/cli/optimize/... && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 5: Manual smoke (optional but recommended)**

```bash
cd go && go run ./cmd/wendy project optimize --help
# In a project dir with a Dockerfile that runs `cargo build`:
#   wendy project optimize            -> warnings + (fixable)
#   wendy project optimize --fix      -> applies cache mount + .dockerignore
#   wendy project optimize --agentic  -> JSON bundle
```

- [ ] **Step 6: Commit**

```bash
cd go && gofmt -w internal/cli/commands/optimize.go internal/cli/commands/project.go internal/cli/commands/optimize_test.go
git add go/internal/cli/commands/optimize.go go/internal/cli/commands/project.go go/internal/cli/commands/optimize_test.go
git commit -m "feat(optimize): wire wendy project optimize command"
```

---

## Self-Review Notes

**Spec coverage:**
- Static-by-default + `--agentic` bundle → Tasks 12, 13. ✓
- Four analyzers (buildcache, releasedebug, cudaml, archimage) → Tasks 6–9. ✓
- Three project shapes (Dockerfile, compose, native) → Task 4 discovery. ✓ (compose service-context resolution is flagged for refinement against the real `ServiceConfig` fields during Task 4.)
- Report + `--json` + exit codes → Tasks 11, 13. ✓
- Safe `--fix` (cache mount, release flag, .dockerignore), idempotent → Tasks 6, 7, 9, 10. ✓
- Arch auto-infer + `--arch` override (offline default arm64; device inference deferred) → Tasks 4, 13. ✓

**Known follow-ups (documented, not gaps):**
- Compose `ServiceConfig` context/dockerfile field wiring (Task 4 NOTE).
- Dockerfile-name variants (`Dockerfile.prod`) beyond `Dockerfile`/`Containerfile` — reuse `isContainerBuildFileName` later.
- Device-based arch inference.
- Exit-code mechanism uses `os.Exit` from `RunE` after flushing output; core logic is tested via `runOptimize` to keep it assertable.

**Type consistency:** `Finding`/`Fix`/`Severity`/`FixOp`/`Loc` defined in Task 1 and used unchanged through Tasks 6–13. `Requirements.Raw` field added in Task 12 (Step 2b) and back-referenced to Task 3. `DefaultAnalyzers()` grows by one entry per analyzer task (6→9).
