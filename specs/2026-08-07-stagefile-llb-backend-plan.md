# Stagefile LLB backend (stage 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compile the Stagefile IR to BuildKit LLB and solve it through BuildKit's
Go client, behind a flag, producing images equivalent to the Dockerfile backend.

**Architecture:** `ir.Graph` → `recipe` (shared structured render) → either
`codegen` (Dockerfile text, default) or `llbgen` (LLB definition + image config,
opt-in) → `solve` (`client.Build` with a gateway callback that attaches image
config). No CAS in this stage — that is stage 3. The point of doing LLB first is
that stage 3's cache lookup then happens host-side, where it can consult a local
store without network egress or credentials inside the build sandbox.

**Tech Stack:** Go 1.26, `github.com/moby/buildkit v0.32.2` (already added and
verified in commit `a7a435d94`), existing `buildprogress.go` renderer.

## Global Constraints

- **Branch base: `jo/stagefile-ir-cachekey` (stage 1).** This branch is
  `jo/stagefile-llb-backend`, worktree `.worktrees/stagefile-llb`.
- **The Dockerfile backend stays the default and stays byte-identical.**
  `codegen/golden_test.go`'s `want` strings must never be edited. Any task that
  cannot keep them is wrong.
- **The frozen cache-key corpus must not move.** `cachekey/golden_keys_test.go`
  values stay as committed and `keyFormatVersion` stays 2. Nothing in this stage
  should touch key computation at all; if a key moves, you changed the IR.
- **Recipes get exactly one definition.** After task 1, the shell command and
  cache-mount directory for apt/apk/pip/npm/build live in `recipe` and nowhere
  else. A backend that re-derives a command is a defect — that duplication is
  precisely how two backends come to build different images.
- **`apple-container` is out of scope.** It has no BuildKit underneath, so
  `--builder apple-container` keeps Dockerfile emission permanently. Document it;
  do not attempt it.
- **No network in unit tests.** Anything needing a live buildkitd goes behind the
  build tag in task 5.
- Test command: `go test ./go/internal/stagefile/... ./go/internal/cli/commands/...`
- Module prefix: `github.com/wendylabsinc/wendy/go/internal/stagefile`
- Import naming: the BuildKit package is `github.com/moby/buildkit/client/llb`.
  Our package is `llbgen` to avoid the collision. Never name a local package `llb`.

---

## File Structure

| File | Responsibility |
|---|---|
| `go/internal/stagefile/recipe/recipe.go` **create** | `RunSpec` type + one function per recipe returning it. The single definition of what each recipe *does*. |
| `go/internal/stagefile/recipe/recipe_test.go` **create** | Per-recipe spec assertions. |
| `go/internal/stagefile/codegen/codegen.go` **modify** | Renders `RunSpec` to Dockerfile text. Stops owning command strings. |
| `go/internal/stagefile/llbgen/llbgen.go` **create** | `Emit(g, images, platform) (*llb.Definition, *ImageConfig, error)`. |
| `go/internal/stagefile/llbgen/llbgen_test.go` **create** | Structural assertions over the emitted definition. |
| `go/internal/stagefile/solve/addr.go` **create** | buildkitd address resolution. |
| `go/internal/stagefile/solve/solve.go` **create** | `client.Build` driver + gateway callback attaching image config. |
| `go/internal/cli/commands/stagefilebackend.go` **create** | Flag/env plumbing selecting the backend. |
| `go/internal/stagefile/llbgen/differential_test.go` **create** | Build-tagged differential test against a live buildkitd. |

---

### Task 1: Extract recipe rendering into a shared package

**Files:**
- Create: `go/internal/stagefile/recipe/recipe.go`
- Create: `go/internal/stagefile/recipe/recipe_test.go`
- Modify: `go/internal/stagefile/codegen/codegen.go`

**Interfaces:**
- Consumes: `ir.AptParams`, `ir.PipParams`, `ir.NpmParams`, `ir.BuildParams`.
- Produces: `recipe.RunSpec`, `recipe.CacheMount`, and
  `recipe.For(x *ir.ExecOp) (RunSpec, error)`. Task 2 consumes `RunSpec`.

**Design note.** `RunSpec` describes a step without committing to a rendering.
`Command` is the `/bin/sh -c` argument — a shell string, because apt genuinely
needs `&&` and that is not expressible as argv. That is safe here for the same
reason it is safe today: every interpolated value passes through `shellQuote`,
and there is no user-supplied raw-shell field in the spec. `PreCopies` carries
the file(s) a recipe needs staged before it runs (pip's requirements file, npm's
manifest and lockfile) — today those are `COPY` lines emitted inline by
`pipInstallLines`/`npmInstallLines`, and both backends need them.

- [ ] **Step 1: Write the failing test**

Create `go/internal/stagefile/recipe/recipe_test.go`:

```go
package recipe

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
)

func TestForAptQuotesPackagesAndCleansLists(t *testing.T) {
	got, err := For(&ir.ExecOp{Recipe: ir.RecipeApt, Apt: &ir.AptParams{Packages: []string{"build-essential", "flask>=2.0"}}})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if !strings.Contains(got.Command, "'flask>=2.0'") {
		t.Fatalf("package not shell-quoted: %q", got.Command)
	}
	if !strings.Contains(got.Command, "--no-install-recommends") {
		t.Fatalf("missing --no-install-recommends: %q", got.Command)
	}
	if !strings.Contains(got.Command, "rm -rf /var/lib/apt/lists/*") {
		t.Fatalf("missing list cleanup: %q", got.Command)
	}
	if len(got.CacheMounts) != 0 {
		t.Fatalf("apt should declare no cache mount, got %+v", got.CacheMounts)
	}
}

func TestForPipStagesRequirementsAndMountsCache(t *testing.T) {
	got, err := For(&ir.ExecOp{Recipe: ir.RecipePip, Pip: &ir.PipParams{Requirements: "requirements.txt"}})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if len(got.PreCopies) != 1 || got.PreCopies[0] != "requirements.txt" {
		t.Fatalf("PreCopies = %v, want [requirements.txt]", got.PreCopies)
	}
	if len(got.CacheMounts) != 1 || got.CacheMounts[0].Dir != "/root/.cache/pip" {
		t.Fatalf("CacheMounts = %+v, want one at /root/.cache/pip", got.CacheMounts)
	}
	if !got.CacheMounts[0].Locked {
		t.Fatal("cache mount must be locked; concurrent service builds share it")
	}
	if !strings.Contains(got.Command, "-r 'requirements.txt'") {
		t.Fatalf("requirements not passed: %q", got.Command)
	}
}

func TestForNpmStagesManifestAndLockfile(t *testing.T) {
	got, err := For(&ir.ExecOp{Recipe: ir.RecipeNpm, Npm: &ir.NpmParams{Manager: "pnpm", Manifest: "package.json", Lockfile: "pnpm-lock.yaml"}})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if len(got.PreCopies) != 2 || got.PreCopies[0] != "package.json" || got.PreCopies[1] != "pnpm-lock.yaml" {
		t.Fatalf("PreCopies = %v", got.PreCopies)
	}
	if !strings.Contains(got.Command, "pnpm install --frozen-lockfile") {
		t.Fatalf("wrong command: %q", got.Command)
	}
}

func TestForBuildRejectsUnsupportedLang(t *testing.T) {
	if _, err := For(&ir.ExecOp{Recipe: ir.RecipeBuild, Build: &ir.BuildParams{Lang: "cobol", Profile: "release"}}); err == nil {
		t.Fatal("expected an error for an unsupported build.lang")
	}
}

func TestForRejectsNilPayload(t *testing.T) {
	if _, err := For(&ir.ExecOp{Recipe: ir.RecipeApt}); err == nil {
		t.Fatal("expected an error for an exec op with no params")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./go/internal/stagefile/recipe/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the recipe package**

Create `go/internal/stagefile/recipe/recipe.go`. Move the bodies of
`aptInstallLines`, `apkInstallLines`, `pipInstallLines`, `npmInstallLines`, and
`buildLines` out of `codegen/codegen.go`, reshaped to return a `RunSpec` instead
of Dockerfile lines. Move `shellQuote` here too — it is the safety property both
backends depend on, and it must not exist twice.

```go
// Package recipe is the single definition of what each Stagefile install and
// build step actually does: the command to run, the files it needs staged
// first, and the cache directories it mounts.
//
// It exists because there is now more than one backend. codegen renders a
// RunSpec to Dockerfile text; llbgen renders the same RunSpec to BuildKit LLB.
// If either derived its own command, the two backends would build subtly
// different images and only an end-to-end differential test would notice.
package recipe

import (
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
)

// CacheMount is a persistent build cache directory.
type CacheMount struct {
	Dir string
	// Locked serializes concurrent access. Wendy builds up to four services
	// at once on top of BuildKit's own parallelism; an unlocked mount lets
	// them collide inside the package manager where the waiting is invisible.
	Locked bool
}

// RunSpec is one execution step, backend-agnostic.
type RunSpec struct {
	// Command is the argument to /bin/sh -c. It is a shell string rather
	// than argv because apt genuinely needs "&&". Every interpolated value
	// is shell-quoted by this package, and the spec has no raw-shell field,
	// so nothing user-supplied reaches the shell unquoted.
	Command string
	// PreCopies are build-context paths that must be staged into the image
	// before Command runs, in order.
	PreCopies   []string
	CacheMounts []CacheMount
}

// For returns the RunSpec for one exec op.
func For(x *ir.ExecOp) (RunSpec, error) {
	switch {
	case x.Apt != nil:
		return aptSpec(x.Apt, false), nil
	case x.Apk != nil:
		return aptSpec(x.Apk, true), nil
	case x.Pip != nil:
		return pipSpec(x.Pip), nil
	case x.Npm != nil:
		return npmSpec(x.Npm), nil
	case x.Build != nil:
		return buildSpec(x.Build)
	default:
		return RunSpec{}, fmt.Errorf("recipe: exec op %q has no params", x.Recipe.Name)
	}
}

// ShellQuote wraps s in single quotes so shell metacharacters in it — including
// the ">" in an ordinary version specifier like "flask>=2.0" — are never given
// special meaning. Strictly more complete than a denylist: it needs no
// enumeration of "dangerous" characters, several of which are also legal and
// necessary in real package specifiers.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
```

Write `aptSpec`, `pipSpec`, `npmSpec`, and `buildSpec` by transcribing the
existing command construction from `codegen.go` exactly — same flags, same order,
same quoting, same cache directories. The apt spec's `Command` must reproduce the
current two-line `RUN` as a single shell string joined with ` && `; codegen will
re-split it for rendering (see step 4).

- [ ] **Step 4: Rewire codegen onto RunSpec**

In `codegen.go`, replace the five `*InstallLines`/`buildLines` helpers with one
function that renders a `RunSpec` to Dockerfile lines: emit a `COPY <p> <p>` for
each `PreCopies` entry, then a single `RUN` carrying
`--mount=type=cache,sharing=locked,target=<dir>` for each cache mount, then the
command.

**The apt line is the byte-identity trap.** Today apt renders as two physical
lines with a `\` continuation and four-space indent:

```
RUN apt-get update && apt-get install -y --no-install-recommends 'x' \
    && rm -rf /var/lib/apt/lists/*
```

Reproduce that exactly. The simplest faithful approach is for `aptSpec` to keep
the ` && rm -rf /var/lib/apt/lists/*` as the final clause and for the renderer to
split the command on that final ` && ` when emitting Dockerfile text. Whatever
approach you choose, the golden test is the arbiter — do not edit it.

Delete `shellQuote` from `codegen.go` and use `recipe.ShellQuote`.

- [ ] **Step 5: Verify byte-identity**

Run: `go test ./go/internal/stagefile/... ./go/internal/cli/commands/... ./go/internal/cli/optimize/...`
Expected: all PASS, `codegen/golden_test.go` untouched.

- [ ] **Step 6: Commit**

```bash
git add go/internal/stagefile/recipe/ go/internal/stagefile/codegen/
git commit -m "refactor(stagefile): give recipes one definition shared by both backends"
```

---

### Task 2: `llbgen.Emit`

**Files:**
- Create: `go/internal/stagefile/llbgen/llbgen.go`
- Test: `go/internal/stagefile/llbgen/llbgen_test.go`

**Interfaces:**
- Consumes: `ir.Graph`, `recipe.For`, `recipe.RunSpec`.
- Produces: `llbgen.ImageConfig{Entrypoint []string; User string}` and
  `llbgen.Emit(g *ir.Graph, images map[string]string, configs map[string][]byte, platform string) (*llb.Definition, *ImageConfig, error)`.
  Task 3 solves the definition and stamps the returned output config onto the
  result.

  `configs` holds each base image's raw OCI image-config JSON, keyed by the same
  ref as `images`. It is a parameter rather than something a later stage applies
  because `WithImageConfig` acts on an `llb.State` and `Emit` marshals before
  returning — after that the states no longer exist. It is also a genuine build
  input: `PATH` and `WorkingDir` change the resulting filesystem, so it belongs
  in the function whose output a cache key will eventually describe.

**Design note.** Build one `llb.State` per node, indexed like `ir.Graph.Nodes`, so
the graph maps across one-to-one. Ops:

- `ir.OpImage` → `llb.Image(ref+"@"+digest, llb.Platform(p))`
- `ir.OpExec` → `state.Run(llb.Args([]string{"/bin/sh","-c", spec.Command}), mounts...).Root()`,
  with each `CacheMount` as `llb.AddMount(dir, llb.Scratch(), llb.AsPersistentCacheDir(dir, llb.CacheMountLocked))`,
  and each `PreCopies` entry copied from the local context first
- `ir.OpCopy` → `llb.Copy(src, path, dest)` where `src` is `llb.Local("context")`
  for `FromLocal`, else the state of `Inputs[1]`

`ImageConfig` comes from the final stage — the same place `codegen` reads it.

- [ ] **Step 1: Write the failing test**

Create `go/internal/stagefile/llbgen/llbgen_test.go`. Assert on structure rather
than on marshalled bytes — the protobuf encoding is a BuildKit implementation
detail and pinning it would make a dependency bump look like a regression.

```go
package llbgen

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/ir"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func lower(t *testing.T, y string) *ir.Graph {
	t.Helper()
	f, err := spec.Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g, err := ir.Lower(f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	return g
}

const twoStage = `
version: 1
stages:
  - name: deps
    from: python:3.12-slim
    install:
      pip:
        requirements: requirements.txt
  - name: app
    from: python:3.12-slim
    copy:
      - from: deps
        paths: ["/usr/local/lib"]
      - from: local
        paths: ["app.py"]
    entrypoint:
      exec: [python3, app.py]
`

func TestEmitProducesADefinitionAndConfig(t *testing.T) {
	g := lower(t, twoStage)
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	def, cfg, err := Emit(g, images, "linux/arm64")
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if def == nil || len(def.Def) == 0 {
		t.Fatal("empty definition")
	}
	if cfg == nil {
		t.Fatal("nil image config")
	}
	if len(cfg.Entrypoint) != 2 || cfg.Entrypoint[0] != "python3" {
		t.Fatalf("Entrypoint = %v", cfg.Entrypoint)
	}
	if cfg.User != "65532" {
		t.Fatalf("User = %q, want the distroless-style default 65532", cfg.User)
	}
}

func TestEmitErrorsOnMissingDigest(t *testing.T) {
	g := lower(t, twoStage)
	if _, _, err := Emit(g, map[string]string{}, ""); err == nil {
		t.Fatal("Emit succeeded with no resolved digest")
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	g := lower(t, twoStage)
	images := map[string]string{"python:3.12-slim": "sha256:abc123"}

	a, _, err := Emit(g, images, "linux/arm64")
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, _, err := Emit(g, images, "linux/arm64")
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(a.Def) != len(b.Def) {
		t.Fatalf("definition length differs across runs: %d vs %d", len(a.Def), len(b.Def))
	}
	for i := range a.Def {
		if string(a.Def[i]) != string(b.Def[i]) {
			t.Fatalf("definition op %d differs across runs", i)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./go/internal/stagefile/llbgen/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `Emit`**

Create `go/internal/stagefile/llbgen/llbgen.go`. Walk `g.Nodes` in order building
a `states []llb.State` parallel to it, exactly as `codegen.Generate` walks stages
by slicing on `Stage.Final`. Marshal with
`st.Marshal(context.TODO(), llb.LinuxArm64)` or the parsed platform; return
`ErrUnsupported`-style errors for a nil payload or a missing digest, mirroring
`codegen`'s guards — do not trust the graph silently.

For the default user, reuse `codegen`'s `defaultUser` value `"65532"`. Export it
from `codegen` or lift it into `ir`; do not type the literal twice.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./go/internal/stagefile/llbgen/... -v`
Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/stagefile/llbgen/
git commit -m "feat(stagefile): compile the IR to BuildKit LLB"
```

---

### Task 3: buildkitd address resolution and the solver

**Files:**
- Create: `go/internal/stagefile/solve/addr.go`
- Create: `go/internal/stagefile/solve/solve.go`
- Test: `go/internal/stagefile/solve/addr_test.go`

**Interfaces:**
- Produces: `solve.Address(ctx) (string, error)` and
  `solve.Run(ctx, addr string, def *llb.Definition, cfg *llbgen.ImageConfig, out Output) error`.

**Design note.** Address resolution order, most explicit first:
1. `BUILDKIT_HOST` if set — the standard BuildKit env var; respect it.
2. On-device: the buildkitd socket, when `WENDY_AGENT_SOCKET` is set (the same
   condition `shouldUseBuildkitOnDevice` already uses in `docker.go`).
3. buildx's own daemon: `docker-container://buildx_buildkit_<builder>0`, via
   `github.com/moby/buildkit/client/connhelper/dockercontainer`. This is the
   macOS path, where buildkitd lives inside a container rather than on the host.

Only `Address` needs unit tests; `Run` needs a daemon and is covered in task 5.

**Correction to this plan, found during task 2.** "Image config" is two
different things, and the original text conflated them:

- **Output image config** — the `Entrypoint` and `User` stamped onto the image
  this build produces. That belongs here, in the gateway callback, exactly as
  written below.
- **Base image config** — the `Env` (notably `PATH`) and `WorkingDir` that a
  Dockerfile `RUN` inherits from its base image. That canNOT be applied here.
  `WithImageConfig` operates on an `llb.State` and must be applied before any
  dependent op is built, but `llbgen.Emit` marshals before returning, so by the
  time this task holds a `*llb.Definition` the states are gone.

Base config therefore became a parameter of `Emit`
(`configs map[string][]byte`, keyed like `images`). **This task's remaining job
for it is to resolve those configs** — fetch each base image's OCI config JSON
via the registry client already used by `lock` — and hand them to `Emit`. Do not
default a missing config to empty: an exec with no inherited `PATH` fails to
find `go` or `cargo`, and a missing `WorkingDir` silently relocates every
relative copy to `/`, producing a build that succeeds and yields a different
image than the Dockerfile backend.

The gateway callback below is for the output config only:

```go
res, err := c.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB()})
if err != nil {
    return nil, err
}
ref, err := res.SingleRef()
if err != nil {
    return nil, err
}
img := specs.Image{ /* platform, rootfs from the solve */ }
img.Config.Entrypoint = cfg.Entrypoint
img.Config.User = cfg.User
cfgJSON, err := json.Marshal(img)
if err != nil {
    return nil, err
}
out := gateway.NewResult()
out.SetRef(ref)
out.AddMeta(exptypes.ExporterImageConfigKey, cfgJSON)
return out, nil
```

- [ ] **Step 1: Write the failing test for address resolution**

Create `go/internal/stagefile/solve/addr_test.go` with table-driven cases
covering: `BUILDKIT_HOST` set wins over everything; `WENDY_AGENT_SOCKET` set and
no `BUILDKIT_HOST` selects the device socket; neither set falls back to the
buildx container address. Inject the environment through package-level function
variables (`lookupEnv`, `lookPath`) the way `docker.go` already does with
`imageBuilderLookPath` — do not call `os.Setenv` in tests.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./go/internal/stagefile/solve/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `addr.go`, then `solve.go`**

Wire progress through the existing renderer in
`go/internal/cli/commands/buildprogress.go` — read it first and match how the
buildx path feeds it, so LLB builds look identical to users. Do not invent a
second progress UI.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./go/internal/stagefile/solve/... -v`
Expected: PASS. `Run` is not unit-tested here by design; task 5 covers it.

- [ ] **Step 5: Commit**

```bash
git add go/internal/stagefile/solve/
git commit -m "feat(stagefile): solve LLB through BuildKit's Go client"
```

---

### Task 4: CLI wiring behind a flag

**Files:**
- Create: `go/internal/cli/commands/stagefilebackend.go`
- Modify: `go/internal/stagefile/stagefile.go`
- Test: `go/internal/cli/commands/stagefilebackend_test.go`

**Interfaces:**
- Produces: `stagefileBackendLLB() bool` and a `CompileToLLB` entry point on the
  `stagefile` package mirroring `CompileFile`.

**Design note.** Opt-in only. Selection order: an explicit
`--stagefile-backend=llb|dockerfile` flag wins; else `WENDY_STAGEFILE_BACKEND`;
else `dockerfile`. When `--builder apple-container` is active, LLB is
unavailable — if the user asked for LLB explicitly, fail with a message naming
the incompatibility rather than silently falling back, because a silent fallback
here means the user believes they tested the new backend when they did not.

- [ ] **Step 1: Write the failing test**

Cover: flag beats env; env used when no flag; default is `dockerfile`; an invalid
value is a clear error; `llb` combined with `apple-container` errors rather than
falling back. Follow the table-driven style already in `docker_test.go`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run Stagefile`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add `CompileToLLB(dir, platform string) (*llb.Definition, *llbgen.ImageConfig, error)`
to `stagefile.go`, sharing `compileFile`'s parse/lock/resolve path so the
lockfile behaviour is identical between backends — resolve once, then branch on
backend. Do not duplicate the lock logic.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./go/internal/stagefile/... ./go/internal/cli/commands/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/ go/internal/stagefile/stagefile.go
git commit -m "feat(stagefile): select the LLB backend behind an opt-in flag"
```

---

### Task 5: Differential test against a live buildkitd

**Files:**
- Create: `go/internal/stagefile/llbgen/differential_test.go`

**Design note.** This is the test that earns trust in the new backend, and the
one thing no unit test can substitute for: **the same Stagefile through both
backends must produce the same image filesystem.** It needs a daemon, so gate it
behind `//go:build buildkit_integration` and skip when no address resolves. State
in the file's doc comment how to run it, because a gated test nobody knows how to
run is a test that does not exist.

For each `Examples/*/build.stagefile.yaml` plus both `testdata` fixtures: build
via the Dockerfile backend to an OCI layout, build via the LLB backend to an OCI
layout, then compare the layer diff-IDs and the image config (entrypoint, user).
Compare unpacked content, not layer digests — timestamps and layer boundaries
legitimately differ between the two paths, and asserting on them would produce a
test that fails for reasons nobody can act on.

- [ ] **Step 1: Write the test**
- [ ] **Step 2: Run it with the tag against a live daemon; record the result**

Run: `go test -tags buildkit_integration ./go/internal/stagefile/llbgen/ -run Differential -v`
Expected: PASS, or a precise report of which fixture diverges and how.

- [ ] **Step 3: Run the untagged suite to confirm it stays skipped**

Run: `go test ./go/internal/stagefile/...`
Expected: PASS, differential test not compiled.

- [ ] **Step 4: Commit**

```bash
git add go/internal/stagefile/llbgen/differential_test.go
git commit -m "test(stagefile): differential-test the LLB backend against Dockerfile output"
```

---

## Done when

- `recipe` is the only definition of each recipe's command and cache mounts.
- `codegen` output byte-identical; its golden `want` strings never edited.
- The cache-key corpus unmoved; `keyFormatVersion` still 2.
- `llbgen.Emit` is pure and deterministic.
- `--stagefile-backend=llb` builds a runnable image with correct entrypoint and user.
- The differential test passes, or its divergences are documented and understood.
- `go test ./go/...` green.

## Explicitly out of scope

- **Any CAS, any tier, any cache lookup.** Stage 3. This stage only makes the
  graph executable; it does not yet skip work. Do not add a cache here — the
  tiering and trust rules in the design doc are what make it safe, and none of
  them exist yet.
- `apple-container` support.
- Removing the Dockerfile backend. It stays the default and remains the fallback
  for builders BuildKit does not serve.
