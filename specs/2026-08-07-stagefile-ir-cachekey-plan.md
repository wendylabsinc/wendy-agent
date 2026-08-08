# Stagefile IR + cache keys (stage 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a typed intermediate representation (`ir`) and a stable,
versioned cache-key function (`cachekey`) for Stagefile, and retarget `codegen`
to consume the IR — with byte-identical Dockerfile output as the acceptance test.

**Architecture:** `spec` (YAML) → `ir.Lower` (typed DAG, no rendered shell) →
`codegen.Generate` (Dockerfile text). `cachekey.Key` hashes an IR node over its
semantic dependency closure. This stage adds no BuildKit LLB, no CAS, and no
behavior change; it exists so stage 2 has a graph to emit from and stage 3 has a
key to look up.

**Tech Stack:** Go, `gopkg.in/yaml.v3`, stdlib `crypto/sha256`. No new
dependencies.

## Global Constraints

- **Branch base: `jo/stagefile-parallel-compile` (PR #1607), NOT `jo/try-stagefile`.**
  #1607 already modified `codegen.go` (the `cacheRun` helper), `lock/resolve.go`,
  and `stagefile.go`. Basing on #1585 guarantees a three-way conflict in exactly
  the files this stage rewrites.
- **Zero behavior change.** Every existing test in
  `go/internal/stagefile/...` and `go/internal/cli/commands/...` must pass
  unmodified. If a test needs changing to go green, that is a defect in this
  work, not in the test.
- **No rendered shell strings in `ir`.** The IR carries typed recipe parameters;
  `codegen` owns all quoting and rendering. Putting a rendered string in the IR
  would reintroduce, at the key layer, exactly the opacity this design removes.
- **`ir` must not reuse `spec` types in node payloads.** `spec` will grow fields
  as DSL coverage gaps close (`specs/stagefile-dsl-gaps.md`). If `ir` aliased
  those types, adding a spec field would silently change cache keys fleet-wide.
  Decoupling forces every new field through `Lower`, where the version bump is a
  visible decision.
- **Recipe versions are frozen constants.** All start at `1`. Changing one is a
  reviewed act, never a refactoring side effect.
- Test command throughout: `go test ./go/internal/stagefile/...`
- Go module path prefix: `github.com/wendylabsinc/wendy/go/internal/stagefile`
- Commit style matches the repo: `feat:`, `refactor:`, `test:` prefixes.

---

## File Structure

| File | Responsibility |
|---|---|
| `go/internal/stagefile/ir/ir.go` **create** | Node/Graph types, recipe identities. Types only, no logic. |
| `go/internal/stagefile/ir/lower.go` **create** | `Lower(*spec.File) (*Graph, error)`. The sole place mapping spec → nodes. |
| `go/internal/stagefile/ir/lower_test.go` **create** | Lowering tests, one per spec construct. |
| `go/internal/stagefile/cachekey/canonical.go` **create** | Deterministic byte encoding. Length-prefixed, no maps, no `%v`. |
| `go/internal/stagefile/cachekey/cachekey.go` **create** | `Key(...)` over the encoding. |
| `go/internal/stagefile/cachekey/cachekey_test.go` **create** | Key semantics: closure sensitivity, sibling insensitivity. |
| `go/internal/stagefile/cachekey/golden_keys_test.go` **create** | Frozen key corpus. Guards fleet-wide invalidation. |
| `go/internal/stagefile/codegen/codegen.go` **modify** | `Generate` takes `*ir.Graph`; `GenerateSpec` shim retained. |
| `go/internal/stagefile/codegen/golden_test.go` **modify** | Routes through `ir.Lower`; expected output unchanged. |

---

### Task 1: The `ir` types and `Lower`

**Files:**
- Create: `go/internal/stagefile/ir/ir.go`
- Create: `go/internal/stagefile/ir/lower.go`
- Test: `go/internal/stagefile/ir/lower_test.go`

**Interfaces:**
- Consumes: `spec.File`, `spec.Stage`, `spec.NpmLockfile` (from `spec/spec.go`).
- Produces: `ir.Graph`, `ir.Node`, `ir.OpKind`, `ir.Recipe`, and
  `ir.Lower(f *spec.File) (*Graph, error)`. Task 2 hashes `ir.Node`; Task 3
  renders `ir.Graph`.

**Design note for the implementer.** A `Graph` holds a flat, topologically
ordered `[]Node`. Edges are integer indices into that slice, never pointers —
indices serialize deterministically and pointers do not, which matters in Task 2.
`Stages` records the file's stage names and the index of each stage's final node,
so `codegen` can rebuild `FROM ... AS <name>` blocks in original order.

- [ ] **Step 1: Write the failing test**

Create `go/internal/stagefile/ir/lower_test.go`:

```go
package ir

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestLowerSingleStageImageOnly(t *testing.T) {
	f := &spec.File{
		Version: 1,
		Stages:  []spec.Stage{{Name: "app", From: "debian:12"}},
	}
	g, err := Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(g.Nodes))
	}
	if g.Nodes[0].Kind != OpImage {
		t.Fatalf("got kind %q, want %q", g.Nodes[0].Kind, OpImage)
	}
	if g.Nodes[0].Image.Ref != "debian:12" {
		t.Fatalf("got ref %q, want debian:12", g.Nodes[0].Image.Ref)
	}
	if len(g.Stages) != 1 || g.Stages[0].Name != "app" || g.Stages[0].Final != 0 {
		t.Fatalf("got stages %+v, want one stage app→0", g.Stages)
	}
}

func TestLowerAptThenPipChainsInputs(t *testing.T) {
	f := &spec.File{
		Version: 1,
		Stages: []spec.Stage{{
			Name: "deps",
			From: "python:3.12-slim",
			Install: &spec.Install{
				Apt: &spec.AptInstall{Packages: []string{"build-essential"}},
				Pip: &spec.PipInstall{Requirements: "requirements.txt"},
			},
		}},
	}
	g, err := Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (image, apt, pip)", len(g.Nodes))
	}
	if g.Nodes[1].Exec.Recipe != RecipeApt {
		t.Fatalf("node 1 recipe = %+v, want apt", g.Nodes[1].Exec.Recipe)
	}
	if g.Nodes[2].Exec.Recipe != RecipePip {
		t.Fatalf("node 2 recipe = %+v, want pip", g.Nodes[2].Exec.Recipe)
	}
	// The chain must be explicit: pip runs on apt's result.
	if len(g.Nodes[2].Inputs) != 1 || g.Nodes[2].Inputs[0] != 1 {
		t.Fatalf("pip inputs = %v, want [1]", g.Nodes[2].Inputs)
	}
	if g.Nodes[2].Exec.Pip.Requirements != "requirements.txt" {
		t.Fatalf("pip requirements = %q", g.Nodes[2].Exec.Pip.Requirements)
	}
	if g.Stages[0].Final != 2 {
		t.Fatalf("stage final = %d, want 2", g.Stages[0].Final)
	}
}

func TestLowerCopyFromPriorStageReferencesItsFinalNode(t *testing.T) {
	f := &spec.File{
		Version: 1,
		Stages: []spec.Stage{
			{Name: "deps", From: "python:3.12-slim",
				Install: &spec.Install{Apt: &spec.AptInstall{Packages: []string{"gcc"}}}},
			{Name: "app", From: "python:3.12-slim",
				Copy: []spec.CopyEntry{{From: "deps", Paths: []string{"/usr/lib"}}}},
		},
	}
	g, err := Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	depsFinal := g.Stages[0].Final
	var copyNode *Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == OpCopy {
			copyNode = &g.Nodes[i]
		}
	}
	if copyNode == nil {
		t.Fatal("no copy node lowered")
	}
	// Inputs[0] is the stage this copy lands on; Inputs[1] is the source stage.
	if len(copyNode.Inputs) != 2 || copyNode.Inputs[1] != depsFinal {
		t.Fatalf("copy inputs = %v, want [_, %d]", copyNode.Inputs, depsFinal)
	}
	if copyNode.Copy.FromLocal {
		t.Fatal("copy from a named stage must not be marked FromLocal")
	}
}

func TestLowerCopyFromLocalHasNoStageInput(t *testing.T) {
	f := &spec.File{
		Version: 1,
		Stages: []spec.Stage{{
			Name: "app", From: "debian:12",
			Copy: []spec.CopyEntry{{From: "local", Paths: []string{"app.py"}}},
		}},
	}
	g, err := Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	copyNode := g.Nodes[g.Stages[0].Final]
	if copyNode.Kind != OpCopy || !copyNode.Copy.FromLocal {
		t.Fatalf("final node = %+v, want a local copy", copyNode)
	}
	if len(copyNode.Inputs) != 1 {
		t.Fatalf("local copy inputs = %v, want exactly the base", copyNode.Inputs)
	}
}

func TestLowerRejectsUnknownBuildLang(t *testing.T) {
	f := &spec.File{
		Version: 1,
		Stages: []spec.Stage{{
			Name: "app", From: "debian:12",
			Build: &spec.Build{Lang: "cobol"},
		}},
	}
	if _, err := Lower(f); err == nil {
		t.Fatal("Lower accepted an unsupported build.lang")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/stagefile/ir/...`
Expected: FAIL — the package does not exist (`no required module provides package .../ir`).

- [ ] **Step 3: Write the IR types**

Create `go/internal/stagefile/ir/ir.go`:

```go
// Package ir is the typed intermediate representation between a parsed
// Stagefile and any backend that renders it. A Graph is a flat,
// topologically ordered list of Nodes whose edges are indices into that
// list — never pointers, because Node must serialize deterministically for
// cache-key computation (see package cachekey).
//
// Two rules hold everywhere in this package:
//
//  1. No node carries a rendered shell string. Nodes carry typed recipe
//     parameters; rendering and quoting belong to a backend.
//  2. No node payload aliases a spec type. spec grows fields as DSL coverage
//     gaps close; if ir aliased those types, a new spec field would silently
//     change cache keys fleet-wide. Every field crosses the boundary through
//     Lower, by hand, on purpose.
package ir

// OpKind is the closed set of node operations. Adding a kind is a
// deliberate extension of the model, not a convenience.
type OpKind string

const (
	OpImage OpKind = "image"
	OpExec  OpKind = "exec"
	OpCopy  OpKind = "copy"
)

// Recipe identifies a typed exec operation and the version of its
// compilation rules. Version participates in the cache key: bumping it
// invalidates every node using that recipe, everywhere. Treat a bump as a
// migration, not a refactor.
type Recipe struct {
	Name    string
	Version int
}

var (
	RecipeApt   = Recipe{Name: "apt", Version: 1}
	RecipeApk   = Recipe{Name: "apk", Version: 1}
	RecipePip   = Recipe{Name: "pip", Version: 1}
	RecipeNpm   = Recipe{Name: "npm", Version: 1}
	RecipeBuild = Recipe{Name: "build", Version: 1}
)

// Graph is a lowered Stagefile.
type Graph struct {
	Nodes  []Node
	Stages []Stage
}

// Stage names a build stage and points at the node holding its final state.
type Stage struct {
	Name  string
	Final int
	// Entrypoint and User are stage-level image config rather than
	// filesystem operations, so they hang off the stage, not off a node.
	// They never enter a cache key: changing an entrypoint does not
	// invalidate a built filesystem.
	Entrypoint []string
	User       string
}

// Node is one operation. Exactly one of Image/Exec/Copy is non-nil,
// matching Kind.
type Node struct {
	Kind   OpKind
	Inputs []int
	Image  *ImageOp
	Exec   *ExecOp
	Copy   *CopyOp
}

// ImageOp is a base image by its original reference. The resolved digest is
// supplied separately at key time and at render time, so the graph itself
// stays independent of any particular lockfile state.
type ImageOp struct {
	Ref string
}

// ExecOp is a typed operation. Exactly one params pointer is non-nil, and
// it must correspond to Recipe.
type ExecOp struct {
	Recipe Recipe
	Apt    *AptParams
	Apk    *AptParams
	Pip    *PipParams
	Npm    *NpmParams
	Build  *BuildParams
}

// AptParams covers both apt and apk; the declared shape is identical and
// only the rendered command differs.
type AptParams struct {
	Packages   []string
	Recommends bool
}

// PipParams installs from a requirements file, an explicit list, or both.
type PipParams struct {
	Requirements string
	Packages     []string
}

// NpmParams records the resolved manager and its lockfile, both resolved at
// lower time so no backend re-derives them and drifts.
type NpmParams struct {
	Manager  string
	Lockfile string
}

// BuildParams is a language compile step with its profile resolved.
type BuildParams struct {
	Lang    string
	Profile string
}

// CopyOp promotes paths into the stage this node belongs to. When FromLocal
// is set the source is the build context and Inputs holds only the base;
// otherwise Inputs is [base, sourceStageFinalNode].
type CopyOp struct {
	FromLocal bool
	Paths     []string
	Dest      string
}
```

- [ ] **Step 4: Write `Lower`**

Create `go/internal/stagefile/ir/lower.go`:

```go
package ir

import (
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// supportedLangs mirrors the languages codegen can render. Lower rejects
// anything else here so an unsupported language fails at lowering rather
// than surfacing from a backend deep in the pipeline.
var supportedLangs = map[string]bool{"rust": true, "go": true, "swift": true}

// Lower converts a validated spec.File into a Graph. Node order within a
// stage matches the order codegen must emit: install, then copy, then
// build. That ordering is load-bearing — a copy that lands source files
// must happen after the installs the build depends on, and reversing it was
// a real defect once already (see codegen/golden_test.go).
func Lower(f *spec.File) (*Graph, error) {
	g := &Graph{}
	stageFinal := map[string]int{}

	for _, s := range f.Stages {
		cur := g.add(Node{Kind: OpImage, Image: &ImageOp{Ref: s.From}})

		if s.Install != nil {
			if s.Install.Apt != nil {
				cur = g.add(execNode(cur, &ExecOp{
					Recipe: RecipeApt,
					Apt:    &AptParams{Packages: s.Install.Apt.Packages, Recommends: s.Install.Apt.Recommends},
				}))
			}
			if s.Install.Apk != nil {
				cur = g.add(execNode(cur, &ExecOp{
					Recipe: RecipeApk,
					Apk:    &AptParams{Packages: s.Install.Apk.Packages, Recommends: s.Install.Apk.Recommends},
				}))
			}
			if s.Install.Pip != nil {
				cur = g.add(execNode(cur, &ExecOp{
					Recipe: RecipePip,
					Pip:    &PipParams{Requirements: s.Install.Pip.Requirements, Packages: s.Install.Pip.Packages},
				}))
			}
			if s.Install.Npm != nil {
				manager := s.Install.Npm.Manager
				if manager == "" {
					manager = "npm"
				}
				cur = g.add(execNode(cur, &ExecOp{
					Recipe: RecipeNpm,
					Npm:    &NpmParams{Manager: manager, Lockfile: spec.NpmLockfile(s.Install.Npm.Manager)},
				}))
			}
		}

		for _, c := range s.Copy {
			n := Node{Kind: OpCopy, Inputs: []int{cur}, Copy: &CopyOp{
				Paths: c.Paths,
				Dest:  c.Dest,
			}}
			if c.From == "local" {
				n.Copy.FromLocal = true
			} else {
				src, ok := stageFinal[c.From]
				if !ok {
					return nil, fmt.Errorf("stage %q: copy from unknown stage %q", s.Name, c.From)
				}
				n.Inputs = append(n.Inputs, src)
			}
			cur = g.add(n)
		}

		if s.Build != nil {
			if !supportedLangs[s.Build.Lang] {
				return nil, fmt.Errorf("stage %q: unsupported build.lang %q (supported: go, rust, swift)", s.Name, s.Build.Lang)
			}
			profile := s.Build.Profile
			if profile == "" {
				profile = "release"
			}
			cur = g.add(execNode(cur, &ExecOp{
				Recipe: RecipeBuild,
				Build:  &BuildParams{Lang: s.Build.Lang, Profile: profile},
			}))
		}

		st := Stage{Name: s.Name, Final: cur, User: s.User}
		if s.Entrypoint != nil {
			st.Entrypoint = s.Entrypoint.Exec
		}
		g.Stages = append(g.Stages, st)
		stageFinal[s.Name] = cur
	}
	return g, nil
}

func execNode(base int, e *ExecOp) Node {
	return Node{Kind: OpExec, Inputs: []int{base}, Exec: e}
}

func (g *Graph) add(n Node) int {
	g.Nodes = append(g.Nodes, n)
	return len(g.Nodes) - 1
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./go/internal/stagefile/ir/... -v`
Expected: all five tests PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/stagefile/ir/
git commit -m "feat(stagefile): add typed IR and spec lowering"
```

---

### Task 2: `cachekey` — deterministic encoding and `Key`

**Files:**
- Create: `go/internal/stagefile/cachekey/canonical.go`
- Create: `go/internal/stagefile/cachekey/cachekey.go`
- Test: `go/internal/stagefile/cachekey/cachekey_test.go`

**Interfaces:**
- Consumes: `ir.Graph`, `ir.Node`, `ir.Recipe` from Task 1.
- Produces: `cachekey.Inputs` and
  `cachekey.Key(g *ir.Graph, idx int, in Inputs) (string, error)`, returning
  `"sha256:<hex>"`. Stage 3's CAS calls this.

**Design note for the implementer.** Determinism is the whole product here. Never
hash a Go map directly, never use `fmt.Sprintf("%v", ...)`, and never hash a
struct via `encoding/gob` or `encoding/json` — all three are either
order-unstable or silently sensitive to field reordering during a refactor. The
encoder below writes explicit tags and length prefixes, so a field added without
a version bump changes the bytes loudly rather than quietly.

Keys are computed recursively over a node's inputs. That is deliberate: a node
that runs on another node's result genuinely depends on it. What the key omits is
everything Docker's layer ID carries beyond that — file position, instruction
text, and unrelated sibling stages.

- [ ] **Step 1: Write the failing test**

Create `go/internal/stagefile/cachekey/cachekey_test.go`:

```go
package cachekey

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func mustLower(t *testing.T, f *spec.File) *ir.Graph {
	t.Helper()
	g, err := ir.Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	return g
}

func pipStage(name, from, reqs string, aptPkgs []string) spec.Stage {
	s := spec.Stage{Name: name, From: from, Install: &spec.Install{
		Pip: &spec.PipInstall{Requirements: reqs},
	}}
	if aptPkgs != nil {
		s.Install.Apt = &spec.AptInstall{Packages: aptPkgs}
	}
	return s
}

func testInputs() Inputs {
	return Inputs{
		Images: map[string]string{"python:3.12-slim": "sha256:aaa", "debian:12": "sha256:bbb"},
		Files:  map[string]string{"requirements.txt": "sha256:reqs1"},
	}
}

// The core property: the same pip install on the same base yields the same
// key across two unrelated Stagefiles.
func TestKeyIsStableAcrossUnrelatedFiles(t *testing.T) {
	a := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "requirements.txt", nil),
	}})
	b := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "unrelated", From: "debian:12"},
		pipStage("otherName", "python:3.12-slim", "requirements.txt", nil),
	}})

	ka, err := Key(a, a.Stages[0].Final, testInputs())
	if err != nil {
		t.Fatalf("Key(a): %v", err)
	}
	kb, err := Key(b, b.Stages[1].Final, testInputs())
	if err != nil {
		t.Fatalf("Key(b): %v", err)
	}
	if ka != kb {
		t.Fatalf("keys differ across unrelated files:\n a=%s\n b=%s", ka, kb)
	}
	if !strings.HasPrefix(ka, "sha256:") {
		t.Fatalf("key %q lacks sha256: prefix", ka)
	}
}

// The soundness property: a different dependency closure must key
// differently, even with identical pip params.
func TestKeyDiffersWhenClosureDiffers(t *testing.T) {
	a := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "requirements.txt", []string{"foo"}),
	}})
	b := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "requirements.txt", []string{"bar"}),
	}})

	ka, _ := Key(a, a.Stages[0].Final, testInputs())
	kb, _ := Key(b, b.Stages[0].Final, testInputs())
	if ka == kb {
		t.Fatal("pip keyed identically over different apt closures — cache is unsound")
	}
}

func TestKeyDiffersWhenInputFileChanges(t *testing.T) {
	g := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "requirements.txt", nil),
	}})

	in1 := testInputs()
	k1, _ := Key(g, g.Stages[0].Final, in1)

	in2 := testInputs()
	in2.Files["requirements.txt"] = "sha256:reqs2"
	k2, _ := Key(g, g.Stages[0].Final, in2)

	if k1 == k2 {
		t.Fatal("key ignored requirements.txt content")
	}
}

func TestKeyDiffersWhenBaseDigestChanges(t *testing.T) {
	g := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "requirements.txt", nil),
	}})

	k1, _ := Key(g, g.Stages[0].Final, testInputs())
	in2 := testInputs()
	in2.Images["python:3.12-slim"] = "sha256:ccc"
	k2, _ := Key(g, g.Stages[0].Final, in2)

	if k1 == k2 {
		t.Fatal("key ignored the base image digest")
	}
}

func TestKeyErrorsOnMissingImageDigest(t *testing.T) {
	g := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "alpine:3.20"},
	}})
	if _, err := Key(g, 0, testInputs()); err == nil {
		t.Fatal("Key succeeded with no resolved digest for alpine:3.20")
	}
}

func TestKeyErrorsOnMissingFileDigest(t *testing.T) {
	g := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{
		pipStage("deps", "python:3.12-slim", "absent.txt", nil),
	}})
	if _, err := Key(g, g.Stages[0].Final, testInputs()); err == nil {
		t.Fatal("Key succeeded with no digest for absent.txt")
	}
}

// Entrypoint and user are image config, not filesystem content; changing
// them must not invalidate a built node.
func TestKeyIgnoresEntrypointAndUser(t *testing.T) {
	base := pipStage("deps", "python:3.12-slim", "requirements.txt", nil)
	a := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{base}})

	withCfg := base
	withCfg.Entrypoint = &spec.Entrypoint{Exec: []string{"python3", "x.py"}}
	withCfg.User = "1000"
	b := mustLower(t, &spec.File{Version: 1, Stages: []spec.Stage{withCfg}})

	ka, _ := Key(a, a.Stages[0].Final, testInputs())
	kb, _ := Key(b, b.Stages[0].Final, testInputs())
	if ka != kb {
		t.Fatal("entrypoint/user changed the filesystem key")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/stagefile/cachekey/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the canonical encoder**

Create `go/internal/stagefile/cachekey/canonical.go`:

```go
package cachekey

import (
	"encoding/binary"
	"hash"
)

// enc writes a self-delimiting, order-explicit byte stream into a hash.
//
// Everything is length-prefixed and every field is preceded by a literal
// tag. This is deliberately more verbose than gob or JSON: both of those
// can change their output when a struct's fields are reordered or a field
// is added, which would silently invalidate every cached node in the fleet
// during an ordinary refactor. Here, any change to the encoding is a
// visible edit to this file.
type enc struct{ h hash.Hash }

func (e enc) tag(name string) {
	e.str(name)
}

func (e enc) str(s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	e.h.Write(n[:])
	e.h.Write([]byte(s))
}

func (e enc) int(i int) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(i))
	e.h.Write(n[:])
}

func (e enc) bool(b bool) {
	if b {
		e.int(1)
		return
	}
	e.int(0)
}

// strs encodes a slice in its given order. Order is significant for package
// lists: apt resolves differently depending on order in rare cases, and
// pretending otherwise would key two genuinely different rootfs alike.
func (e enc) strs(ss []string) {
	e.int(len(ss))
	for _, s := range ss {
		e.str(s)
	}
}
```

- [ ] **Step 4: Write `Key`**

Create `go/internal/stagefile/cachekey/cachekey.go`:

```go
// Package cachekey computes stable content-addressed keys for ir nodes.
//
// A key covers a node's semantic dependency closure: its recipe and
// version, its declared parameters, the digests of any input files it
// reads, and — recursively — the keys of the nodes it runs on. It does not
// cover the node's position in the source file, the text of any rendered
// command, or any unrelated sibling stage. That is the difference between
// this and a Docker layer ID, and it is why two projects that reach the
// same rootfs by different routes converge on the same key.
package cachekey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
)

// keyFormatVersion prefixes every key. Bumping it invalidates every cached
// node everywhere; it exists for changes to the keying scheme itself, as
// distinct from ir.Recipe.Version which scopes an invalidation to one
// recipe.
const keyFormatVersion = 1

// Inputs supplies the externally-resolved facts a key depends on: base
// image digests (from the lockfile) and build-context path digests.
//
// Files maps a context-relative path to a digest of its content. For a
// directory that must be a digest over the whole tree — names, modes, and
// contents — because a copy of a directory depends on all of it. Computing
// these belongs to the caller; stage 3 supplies them from the build
// context, and stage 1 only ever receives them from tests. Key treats a
// missing entry as an error rather than hashing an empty string, so a
// caller that forgets a path cannot silently produce a key that collides
// across different content.
type Inputs struct {
	Images map[string]string
	Files  map[string]string
}

// Key returns the "sha256:<hex>" key of node idx in g.
func Key(g *ir.Graph, idx int, in Inputs) (string, error) {
	h := sha256.New()
	e := enc{h: h}
	e.tag("stagefile-key")
	e.int(keyFormatVersion)
	if err := write(e, g, idx, in); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func write(e enc, g *ir.Graph, idx int, in Inputs) error {
	if idx < 0 || idx >= len(g.Nodes) {
		return fmt.Errorf("cachekey: node index %d out of range", idx)
	}
	n := g.Nodes[idx]

	// Inputs are folded in by key, not by index — an index is a position in
	// this particular file and must never reach the hash.
	e.tag("inputs")
	e.int(len(n.Inputs))
	for _, dep := range n.Inputs {
		sub, err := Key(g, dep, in)
		if err != nil {
			return err
		}
		e.str(sub)
	}

	e.tag(string(n.Kind))
	switch n.Kind {
	case ir.OpImage:
		digest, ok := in.Images[n.Image.Ref]
		if !ok {
			return fmt.Errorf("cachekey: no resolved digest for image %q", n.Image.Ref)
		}
		// The ref itself is excluded on purpose: two tags pointing at the
		// same digest are the same rootfs and must share a key.
		e.str(digest)
	case ir.OpCopy:
		e.bool(n.Copy.FromLocal)
		e.strs(n.Copy.Paths)
		e.str(n.Copy.Dest)
		if n.Copy.FromLocal {
			for _, p := range n.Copy.Paths {
				d, ok := in.Files[p]
				if !ok {
					return fmt.Errorf("cachekey: no digest for context path %q", p)
				}
				e.str(d)
			}
		}
	case ir.OpExec:
		if err := writeExec(e, n.Exec, in); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cachekey: unhandled node kind %q", n.Kind)
	}
	return nil
}

func writeExec(e enc, x *ir.ExecOp, in Inputs) error {
	e.str(x.Recipe.Name)
	e.int(x.Recipe.Version)
	switch {
	case x.Apt != nil:
		e.strs(x.Apt.Packages)
		e.bool(x.Apt.Recommends)
	case x.Apk != nil:
		e.strs(x.Apk.Packages)
		e.bool(x.Apk.Recommends)
	case x.Pip != nil:
		e.str(x.Pip.Requirements)
		e.strs(x.Pip.Packages)
		if x.Pip.Requirements != "" {
			d, ok := in.Files[x.Pip.Requirements]
			if !ok {
				return fmt.Errorf("cachekey: no digest for %q", x.Pip.Requirements)
			}
			e.str(d)
		}
	case x.Npm != nil:
		e.str(x.Npm.Manager)
		e.str(x.Npm.Lockfile)
		d, ok := in.Files[x.Npm.Lockfile]
		if !ok {
			return fmt.Errorf("cachekey: no digest for %q", x.Npm.Lockfile)
		}
		e.str(d)
	case x.Build != nil:
		e.str(x.Build.Lang)
		e.str(x.Build.Profile)
	default:
		return fmt.Errorf("cachekey: exec node has no params")
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./go/internal/stagefile/cachekey/... -v`
Expected: all seven tests PASS.

Note: `TestKeyErrorsOnMissingFileDigest` and the npm path both require a file
digest. If `TestKeyIsStableAcrossUnrelatedFiles` fails, the likely cause is that
`ir.Lower` recorded something file-position-dependent in a node — check that no
node carries a stage name or index.

- [ ] **Step 6: Commit**

```bash
git add go/internal/stagefile/cachekey/
git commit -m "feat(stagefile): add deterministic content-addressed cache keys"
```

---

### Task 3: Retarget `codegen` to the IR

**Files:**
- Modify: `go/internal/stagefile/codegen/codegen.go` (`Generate`, lines 26–80)
- Modify: `go/internal/stagefile/codegen/golden_test.go`
- Test: existing `go/internal/stagefile/codegen/codegen_test.go` (unchanged)

**Interfaces:**
- Consumes: `ir.Graph` from Task 1.
- Produces: `codegen.Generate(g *ir.Graph, images map[string]string, platform string) (string, error)`
  and the compatibility shim
  `codegen.GenerateSpec(f *spec.File, images map[string]string, platform string) (string, error)`,
  which lowers then generates. `stagefile.compileFile` keeps calling `GenerateSpec`,
  so no caller outside this package changes in stage 1.

**Design note for the implementer.** This is the risky task, and its acceptance
test is severe on purpose: **the generated Dockerfile must be byte-identical.**
The existing `codegen_test.go` and `golden_test.go` are the specification. Do not
edit expected strings. If output differs, the lowering or the rendering is wrong.

Watch the ordering trap called out in `golden_test.go`'s comment: install must
render before copy within a stage. `ir.Lower` already fixes that order, and
rendering nodes in graph order preserves it — but only if you iterate nodes, not
re-read the spec.

- [ ] **Step 1: Run the existing tests to capture the baseline**

Run: `go test ./go/internal/stagefile/... -v 2>&1 | tail -40`
Expected: all PASS. Note the count; it must not drop.

- [ ] **Step 2: Rewrite `Generate` over the IR**

In `go/internal/stagefile/codegen/codegen.go`, replace the existing `Generate`
(currently `spec`-typed) with the pair below. Leave `shellQuote`, `cacheRun`,
`aptInstallLines`, `apkInstallLines`, `pipInstallLines`, `npmInstallLines`,
`copyLines`, `buildLines`, and `entrypointLine` in place — only their parameter
types change from `spec.*` to `ir.*`, since the two now carry identical fields.

```go
// Generate compiles a lowered graph into Dockerfile text. images maps every
// base-image ref in g to its resolved "sha256:..." digest. platform, if
// non-empty, is applied to every FROM via --platform.
func Generate(g *ir.Graph, images map[string]string, platform string) (string, error) {
	var blocks []string
	lastIdx := len(g.Stages) - 1

	// Nodes belong to the stage whose range they fall in; walking stages and
	// slicing the node list preserves both stage order and within-stage
	// operation order without consulting the spec again.
	start := 0
	for si, st := range g.Stages {
		var lines []string
		for i := start; i <= st.Final; i++ {
			n := g.Nodes[i]
			switch n.Kind {
			case ir.OpImage:
				digest, ok := images[n.Image.Ref]
				if !ok {
					return "", fmt.Errorf("stage %q: no resolved digest for %q; run `stagefile lock`", st.Name, n.Image.Ref)
				}
				lines = append(lines, fromLine(n.Image.Ref, digest, st.Name, platform))
			case ir.OpExec:
				el, err := execLines(n.Exec)
				if err != nil {
					return "", fmt.Errorf("stage %q: %w", st.Name, err)
				}
				lines = append(lines, el...)
			case ir.OpCopy:
				lines = append(lines, copyLine(g, n))
			default:
				return "", fmt.Errorf("stage %q: unhandled node kind %q", st.Name, n.Kind)
			}
		}
		if si == lastIdx {
			if len(st.Entrypoint) > 0 {
				lines = append(lines, entrypointLine(st.Entrypoint))
			}
			user := st.User
			if user == "" {
				user = defaultUser
			}
			lines = append(lines, "USER "+user)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
		start = st.Final + 1
	}
	return strings.Join(blocks, "\n\n") + "\n", nil
}

// GenerateSpec lowers f and generates from the result. It exists so callers
// outside this package are untouched while the IR lands; stage 2 removes it
// once every caller holds a graph of its own.
func GenerateSpec(f *spec.File, images map[string]string, platform string) (string, error) {
	g, err := ir.Lower(f)
	if err != nil {
		return "", err
	}
	return Generate(g, images, platform)
}

func execLines(x *ir.ExecOp) ([]string, error) {
	switch {
	case x.Apt != nil:
		return aptInstallLines(x.Apt), nil
	case x.Apk != nil:
		return apkInstallLines(x.Apk), nil
	case x.Pip != nil:
		return pipInstallLines(x.Pip), nil
	case x.Npm != nil:
		return npmInstallLines(x.Npm), nil
	case x.Build != nil:
		return buildLines(x.Build)
	default:
		return nil, fmt.Errorf("exec node has no params")
	}
}

// copyLine renders one copy node. The source stage's name comes from the
// graph rather than the node, so a copy node itself stays free of any
// file-local naming.
func copyLine(g *ir.Graph, n ir.Node) string {
	dest := n.Copy.Dest
	if dest == "" {
		dest = n.Copy.Paths[0]
	}
	// BuildKit requires a multi-source COPY's destination to end with "/";
	// a dest without one validates here but hard-fails at docker build.
	if len(n.Copy.Paths) > 1 && !strings.HasSuffix(dest, "/") {
		dest += "/"
	}
	from := ""
	if !n.Copy.FromLocal {
		for _, st := range g.Stages {
			if st.Final == n.Inputs[1] {
				from = "--from=" + st.Name + " "
				break
			}
		}
	}
	return fmt.Sprintf("COPY %s%s %s", from, strings.Join(n.Copy.Paths, " "), dest)
}

func entrypointLine(exec []string) string {
	quoted := make([]string, len(exec))
	for i, s := range exec {
		quoted[i] = strconv.Quote(s)
	}
	return "ENTRYPOINT [" + strings.Join(quoted, ", ") + "]"
}
```

Then change the remaining helper signatures from `*spec.AptInstall` →
`*ir.AptParams`, `*spec.PipInstall` → `*ir.PipParams`, `*spec.NpmInstall` →
`*ir.NpmParams`, `*spec.Build` → `*ir.BuildParams`. In `npmInstallLines`, replace
the `spec.NpmLockfile(n.Manager)` call and the `manager == ""` defaulting with
`n.Lockfile` and `n.Manager` — `ir.Lower` already resolved both.

In `buildLines`, drop the `profile == ""` defaulting for the same reason, and
keep the `default:` error arm even though `ir.Lower` now rejects unknown
languages first; a backend that trusts its input silently is how a future caller
constructing a graph directly gets a wrong Dockerfile instead of an error.

- [ ] **Step 3: Point `stagefile.go` at the shim**

In `go/internal/stagefile/stagefile.go`, change the single call in
`compileFile` from `codegen.Generate(f, updated.Images, platform)` to
`codegen.GenerateSpec(f, updated.Images, platform)`. Nothing else in that file
changes.

- [ ] **Step 4: Update the golden test to route through the IR**

In `go/internal/stagefile/codegen/golden_test.go`, replace the `Generate` call
with a lowering plus generate. **Leave the `want` string exactly as it is.**

```go
	g, err := ir.Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	out, err := Generate(g, images, "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
```

Add `"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"` to that file's
imports. Apply the same two-line change to every `Generate(` call site in
`codegen_test.go`, again without touching any expected output.

- [ ] **Step 5: Run the full package suite**

Run: `go test ./go/internal/stagefile/... -v 2>&1 | tail -40`
Expected: every test PASS, including `TestGenerateGoldenExampleFixture` with its
`want` string untouched. A diff there means the lowering reordered operations.

- [ ] **Step 6: Run the CLI suite for regressions**

Run: `go test ./go/internal/cli/commands/... ./go/internal/cli/optimize/...`
Expected: PASS. These exercise `stagefile.CompileFile` end to end via
`TestResolveDockerfile_CompilesStagefile`.

- [ ] **Step 7: Commit**

```bash
git add go/internal/stagefile/
git commit -m "refactor(stagefile): generate Dockerfiles from the IR"
```

---

### Task 4: Freeze the key corpus

**Files:**
- Create: `go/internal/stagefile/cachekey/golden_keys_test.go`

**Interfaces:**
- Consumes: `cachekey.Key`, `ir.Lower`, and the shipped fixture
  `go/internal/stagefile/testdata/example.stagefile.yaml`.
- Produces: nothing consumed by later code. This is a tripwire.

**Design note for the implementer.** A silent key change invalidates every cached
node in the fleet — at stage 7 that is a global cache flush triggered by an
innocuous refactor. This test makes that impossible to do by accident: the
literal key strings are checked in, so any change to lowering, encoding, or
recipe versions fails CI with a diff naming the node.

- [ ] **Step 1: Write the test with deliberately wrong expected values**

Create `go/internal/stagefile/cachekey/golden_keys_test.go`:

```go
package cachekey

import (
	"os"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// TestGoldenKeys pins the key of every node in the shipped example
// Stagefile. These strings are a wire format, not an implementation detail:
// changing one invalidates that node for every cache tier, in every org,
// permanently. A diff here is only ever correct alongside a bump to
// ir.Recipe.Version (scoped) or keyFormatVersion (global), and both belong
// in their own reviewed commit.
func TestGoldenKeys(t *testing.T) {
	data, err := os.ReadFile("../testdata/example.stagefile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, err := ir.Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	in := Inputs{
		Images: map[string]string{"python:3.12-slim": "sha256:abc123"},
		Files: map[string]string{
			"requirements.txt": "sha256:fixedreqs",
			"app.py":           "sha256:fixedapp",
		},
	}

	want := []string{
		"REPLACE_ME_NODE_0",
		"REPLACE_ME_NODE_1",
		"REPLACE_ME_NODE_2",
		"REPLACE_ME_NODE_3",
		"REPLACE_ME_NODE_4",
		"REPLACE_ME_NODE_5",
	}
	if len(g.Nodes) != len(want) {
		t.Fatalf("fixture lowered to %d nodes, corpus pins %d — update both together", len(g.Nodes), len(want))
	}
	for i := range g.Nodes {
		got, err := Key(g, i, in)
		if err != nil {
			t.Fatalf("Key(node %d): %v", i, err)
		}
		if got != want[i] {
			t.Errorf("node %d (%s) key drift:\n got  %s\n want %s", i, g.Nodes[i].Kind, got, want[i])
		}
	}
}
```

- [ ] **Step 2: Run it to harvest the real keys**

Run: `go test ./go/internal/stagefile/cachekey/ -run TestGoldenKeys -v`
Expected: FAIL, printing a `got` value for each node (and possibly a node-count
mismatch first — if so, resize `want` to the reported count and rerun).

- [ ] **Step 3: Paste the harvested keys into `want`**

Replace each `REPLACE_ME_NODE_n` with the corresponding `got` value from the
failure output, in order. Do not hand-edit or reformat them.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./go/internal/stagefile/cachekey/ -run TestGoldenKeys -v`
Expected: PASS.

- [ ] **Step 5: Verify the tripwire actually trips**

Temporarily change `RecipePip` in `go/internal/stagefile/ir/ir.go` to
`Version: 2`, then run:

Run: `go test ./go/internal/stagefile/cachekey/ -run TestGoldenKeys`
Expected: FAIL, naming the pip node and every node downstream of it.

Revert the version back to `1` and rerun to confirm PASS. A test that cannot fail
is not protecting anything, and this one is the whole safeguard against an
accidental fleet-wide flush.

- [ ] **Step 6: Run everything**

Run: `go test ./go/internal/stagefile/... ./go/internal/cli/commands/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/stagefile/cachekey/golden_keys_test.go
git commit -m "test(stagefile): freeze the cache-key corpus against silent drift"
```

---

## Done when

- `ir`, `cachekey` exist; `codegen` renders from `ir`.
- `TestGenerateGoldenExampleFixture` passes with its `want` string never edited.
- The key corpus is frozen and demonstrably fails on a recipe-version bump.
- `go test ./go/internal/stagefile/... ./go/internal/cli/commands/... ./go/internal/cli/optimize/...` is green.
- No caller outside `go/internal/stagefile/` changed.

## Explicitly out of scope

- BuildKit LLB emission (stage 2).
- Any CAS, any tier, any network call (stage 3+).
- apt/apk dependency pinning (stage 4) — until it lands, apt nodes are keyed
  over their declared package list, which is *not* sufficient for promotion
  beyond a local tier. The design doc's default-deny promotion rule is what
  keeps that honest; nothing in this stage may be read as making apt nodes
  shareable.
- Closing any DSL gap in `specs/stagefile-dsl-gaps.md`.
