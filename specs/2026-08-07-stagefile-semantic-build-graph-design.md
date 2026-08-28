# Stagefile as a semantic build graph

**Status:** design, not an implementation plan. No code in this PR.

**Depends on:** the Stagefile compiler introduced in PR #1585 (`jo/try-stagefile`).
This branch is stacked on it and references the packages it adds
(`go/internal/stagefile/{spec,codegen,lock,dockerignore}`).

**Companion:** `specs/stagefile-dsl-gaps.md` covers DSL *coverage*. This document
covers *performance and identity*, and deliberately does not close those gaps.

---

## Problem

Stagefile today is a safer notation for the same build.

Its safety properties are real and unusual — no raw-shell escape hatch, everything
shell-quoted by construction, base images digest-pinned in a lockfile. But its
performance contribution is approximately zero. Every cache mount `codegen`
emits (`/root/.cache/pip`, `/root/.cargo`, `/root/.swiftpm`) is one you would
write by hand in a Dockerfile. BuildKit does all the actual work. The compiler
produces a Dockerfile and shells out.

Meanwhile the build is the slowest thing in the WendyOS inner loop. The worst
realistic case — edit one line of Python in a CUDA app, get it running on a
Jetson over LTE — is 45–60 minutes, dominated by qemu-emulated arm64 wheel
builds and a multi-gigabyte image push.

**The asset nobody is spending:** Stagefile *knows what each step means*. A
Dockerfile does not. To BuildKit, `RUN pip install -r requirements.txt` is an
opaque string whose cache key is "this exact text, on this exact parent
chain." To Stagefile it is a typed op with declared inputs. That difference is
worth one to two orders of magnitude, and it is not available to any
Dockerfile-based tool at any level of engineering effort.

## Goals

- Node-level content addressing where a node's cache key is a function of its
  **semantic dependency closure** rather than of textual layer history, so two
  projects that reach the same rootfs by different routes share cache.
- A tiered cache — local → LAN → org → global — so an expensive node is built
  once by anyone rather than once per project.
- Devices participate as cache tiers, so deploying ships only missing nodes.
- The Dockerfile backend is retained, for audit artifacts and for environments
  without BuildKit.

## Non-goals

- Closing the DSL coverage gaps in `specs/stagefile-dsl-gaps.md`. Separate track.
- Replacing BuildKit's executor, snapshotter, or mount handling.
- Adding a `run:` escape hatch. It would destroy node determinism as thoroughly
  as it would destroy the security property — an opaque shell string has no
  computable cache key beyond its own text.

---

## Architecture

The current pipeline is a straight line with one hinge:

```
spec.Parse → spec.Validate → lock.Resolve(crane) → codegen.Generate → Dockerfile → buildx
```

The new one inserts a graph where the string-formatting used to be:

```
build.stagefile.yaml
  │
  ├─ spec.Parse / Validate            unchanged
  ├─ ir.Lower                         NEW  typed graph, not text
  ├─ lock.Resolve                     EXTENDED  pins every dep, not just from:
  ├─ cachekey.Key(node)               NEW  pure, versioned per-op
  ├─ cas.Tiered.Has(keys...)          NEW  local → LAN → org → global
  ├─ llb.Emit(graph, satisfied)       NEW  unsatisfied subgraph only
  └─ BuildKit executes → results promoted back up the tiers
        │
        └─ codegen.Generate retained as the Dockerfile backend
```

### Components

| Package | Responsibility | Depends on |
|---|---|---|
| `spec` *(exists)* | parse + validate YAML | — |
| `ir` **new** | lower spec → `Graph` of `Node{Op, Inputs, Attrs}`. Closed op set: `Image`, `Exec`, `CopyTree`, `Merge`. The only place that knows how an `install.pip` becomes nodes. | `spec` |
| `cachekey` **new** | `Key(node, resolvedInputs) → digest`. Pure. Versioned per op (`pip/v1`). | `ir` |
| `cas` **new** | `Has/Get/Put`. Impls: `Local`, `Mesh`, `Org`, `Global`; `Tiered` composes them. Read: first hit wins, promote upward on hit. Write: policy-gated per tier. | — |
| `llb` **new** | `Emit(graph, satisfied) → *llb.Definition`. Satisfied nodes become sources, not exec ops. | `ir`, `cas` |
| `codegen` *(exists)* | Dockerfile backend | retargeted from `spec` to `ir` |

`ir.Lower`, `cachekey.Key`, and `llb.Emit` are all pure functions. `cas` is the
only component with I/O, and it sits behind one interface.

### The load-bearing property

`cachekey.Key` is taken over a node's **semantic dependency closure** —
`(recipe_version, resolved_input_keys, declared_params, input_file_digests)` —
and not over textual or positional layer history.

Stated carefully, because the imprecise version of this claim is tempting and
wrong. A node that runs on the result of another node genuinely *does* depend on
it: `pip install` on a rootfs where `apt install foo` ran is not the same node as
the identical install where `apt install bar` ran, and keying them alike would
make the cache unsound. The dependency is real and the key must carry it.

What the key drops is everything Docker's key carries *beyond* that dependency:
the literal instruction text, the position in the file, the identity of unrelated
sibling stages, and the accumulated history of layers this op does not actually
read. Under Docker, two projects that reach a byte-identical rootfs by different
routes have different layer IDs and therefore can never share cache. Under
content addressing, they converge — the key is a function of what the node
depends on, not of how the file got there.

So reuse happens exactly when dependency closures coincide. That is a narrower
claim than "identical ops always share," and it is still where the money is: the
expensive nodes in practice are common dependencies on stock bases (torch/cu126
on `python:3.12-slim`), which is precisely the case where closures do coincide
across projects and orgs.

This is why it cannot be retrofitted onto Dockerfile emission: it is a property
of how the key is computed, not of where the cache lives.

**Tradeoff worth naming:** the rejected "pure derivations" option (a node
produces a free-standing tree, composed at COPY time) *would* give true
base-independence, and a strictly higher reuse ceiling — the same site-packages
node reusable across *different* base images. It was rejected because ecosystems
that run postinstall scripts, `ldconfig`, or absolute paths assume a real rootfs.
If the closure-coincidence rate measured in stage 3 turns out disappointing, that
is the decision to revisit.

### The inner loop, concretely

You edit `app.py`. Only the `CopyTree(local, [app.py])` node's key changes.
Every upstream node is CAS-satisfied, so `llb.Emit` produces a DAG with **zero
exec ops** — BuildKit assembles an image without running a container. Your pip
install, your `swift build`, your CUDA layer: never touched, not even
cache-validated against a shell command.

### Devices are cache tiers

When `wendy run` targets a Jetson, the device reports which node digests it
already holds and receives only the missing ones. Delta OTA stops being a
feature to build and becomes a consequence of the cache being content-addressed.
The robot is not a deploy target; it is the bottom of the tier stack.

This is where the performance axis and the identity axis turn out to be the same
lever seen from two ends: a node is safely shareable exactly when its inputs are
fully declared and pinned, which is also exactly when the image has a truthful
SBOM.

---

## Where the multiplier actually comes from

| Lever | Multiplier | Applies to |
|---|---|---|
| Native arm64 instead of qemu | 10–50× | every arm64 build on x86 CI |
| CAS hit on an expensive node (torch/cu126 Jetson: ~42 min → ~50 s pull) | ~50× | first-time-in-org builds of common deps |
| Fine-grained invalidation | rebuild one node vs. rebuild the tail | inner loop |
| Node-delta ship | 2 GB → single-digit MB | every device deploy |

**Compounded worst-case inner loop** (one-line Python edit in a CUDA app, onto a
Jetson over LTE): ~45–60 min → ~20–40 s. That is roughly **100×**, and it is the
number to plan against.

**The 2000× case is real but narrow:** a cold CI job on a machine that has never
seen the project, where every node is a global-tier hit. 45 minutes of building
becomes sub-second graph resolution plus parallel pulls. It exists *only* because
of the cross-org tier, and only for dependency-heavy projects whose deps someone
else has already built.

Stated plainly so nobody quotes the wrong number later: **the median build
improves 10–100×. 2000× is the tail, not the median.**

---

## The four things that can sink this

### 1. apt and apk are not deterministic

`apt-get install foo` today does not produce the same bytes as `apt-get install
foo` tomorrow. Under content addressing that is no longer a reproducibility
nicety — it is a correctness hazard, because the same key would map to different
content across the fleet.

**Resolution:** apt/apk installs must resolve to a pinned package set, either via
a snapshot archive (`snapshot.debian.org`) or by resolving-then-pinning into the
lockfile the way pip and npm locks already work. `lock.Resolve` today pins only
`from:` values (`stagefile.go:67`, `imageRefs`); it must come to pin every
declared dependency.

Until an ecosystem is pinnable, **its nodes are local-tier-only and are never
promoted** to org or global. Promotion is default-deny.

### 2. Existence probing leaks private dependency sets

A node key is a hash, but `Has(key)` on a shared tier answers a yes/no question
about your private `requirements.txt`. An attacker who can guess a dependency
set can confirm it, and can do so at scale.

**Resolution:** the global tier serves only nodes whose inputs are
publicly derivable — public registries, public package indexes. Anything
touching `from: local`, a private index, or private source is salted per-org and
can never be promoted past the org tier. Again: default-deny on promotion.

### 3. Trust in shared nodes

A poisoned global node is a supply-chain event with a blast radius of every org.

**Resolution:** nodes are signed at the tier that produced them; a transparency
log records promotions to the global tier; orgs can set policy to
rebuild-verify a sample rather than trusting on signature alone. The cross-org
tier should ship last, behind the org tier, precisely because it is the only one
that introduces a new trust root.

### 4. A device cache tier with no eviction policy is a leak, not a cache

"The robot is the bottom of the tier stack" is the design's most elegant
consequence, and it is also the one with an unowned failure mode. A device
accumulates nodes; a Jetson or a Pi has a finite disk. Nothing above specifies an
eviction policy, a disk budget, or what happens when the node store fills
partway through a deploy.

On a workstation a full cache is an annoyance. On a robot in the field it is a
device that stops accepting deployments, or worse, one that fills the partition
its runtime shares. That is a bricking-shaped risk, not a performance nit, and it
belongs to stage 8 rather than to whoever discovers it.

**Resolution:** stage 8 does not ship without an explicit per-device node budget,
an eviction policy (LRU over node last-use, with pinning for nodes belonging to
the currently-running image), and a deploy path that fails cleanly and reversibly
when the budget cannot be met — never a partial assembly. The device tier should
also be the only tier permitted to refuse a promotion outright.

### Also worth naming: cache-key stability

Bumping `pip/v1` → `pip/v2` invalidates that node for everyone. Op versions live
in a registry file with a changelog, and bumping one is a deliberate, reviewed
act rather than a side effect of refactoring codegen.

---

## Staging

Each stage delivers value on its own; no stage depends on a later one to be
worth shipping.

1. **`ir` + `cachekey`, retarget `codegen` to `ir`.** No behavior change.
   Dockerfile output byte-identical — the existing golden tests
   (`codegen/golden_test.go`) prove it.
2. **`llb` backend behind a flag.** Same images, real DAG.
3. **`cas.Local` + `cas.Tiered` with a single tier.** The inner-loop
   invalidation win lands here — this is the first stage a user *feels*.
4. **Dependency pinning for apt/apk.** Unblocks promotion beyond local.
5. **`cas.Org`** on the per-org registry work.
6. **`cas.Mesh`** on the existing mesh mTLS data plane.
7. **`cas.Global`** + signing + transparency log + promotion policy.
8. **Device as a tier**; node-delta deploy. Gated on the per-device node budget
   and eviction policy from risk 4 — this stage does not ship without them.

Native-arch build farms are orthogonal to all eight and can proceed in parallel;
they are the single largest multiplier and the least architecturally risky.

---

## Testing

- `ir.Lower`, `cachekey.Key`, `llb.Emit` are pure → golden tests throughout.
- **Key-stability corpus:** a golden set of `(node → key)` pairs. Any diff must
  be accompanied by an op-version bump, enforced in CI.
- `cas` tiers sit behind one interface → fakes, no live registry in unit tests
  (matching how `compileFile` already injects `lock.Resolver`).
- **Differential test, the strong one:** for every `Examples/*` Stagefile, the
  LLB backend and the Dockerfile backend must produce identical image
  filesystems. This is how the new backend earns trust, and it is why stage 1
  keeps Dockerfile output byte-identical.

---

## Open questions

- Does `cas.Mesh` reuse the datagram/tunnelframe transport, or warrant its own
  RPC?
- Is the global tier opt-in or opt-out for a new org? (Recommend opt-in until
  the transparency log exists.)
- Does `codegen`'s Dockerfile backend stay a first-class supported path
  long-term, or become audit-only once LLB is default?
