# Stagefile: `cuda:` — a domain-specific form for GPU stages

Status: implemented on `jo/stagefile-cuda-libs` (PR #1633).

## The problem

The first closure of DSL gap #3 gave the compiler two general mechanisms —
`install.pip` as a list, and a `sharedLibraries:` collect-and-register op — and
declared the CUDA examples converted. They were. But look at what an author has
to write to get a working GPU app:

```yaml
env:
  LD_LIBRARY_PATH: /opt/cuda12/lib
install:
  pip:
    - packages: ["torch==2.8.0"]
      index: https://pypi.jetson-ai-lab.io/jp6/cu126/
    - packages: [nvidia-cuda-runtime-cu12, nvidia-cuda-nvrtc-cu12, ... 11 more]
sharedLibraries:
  - dir: /opt/cuda12/lib
    collect: [/usr/local/lib/python3.10/dist-packages/nvidia]
user: root
```

Five coupled declarations, each of which fails in a different way if omitted or
mistyped, none of which is discoverable, and all of which the shipped examples
explain only in YAML comments that a newly written file will not have. The
knowledge is real and hard-won — CUDA-13 sbsa wheels carry no `sm_87` kernels,
so an Orin needs CUDA-12.6 wheels; a JetPack-7 host injects CUDA 13 via CDI,
whose `libcudnn.so.9` shadows the wheel's — and none of it belongs in the hands
of someone writing an app.

Two of the five values also encode a `dist-packages` path that depends on the
base image's Python version, and every one of them is correct only for Orin. A
project that also deploys to a Thor had nothing to say.

## The form

```yaml
stages:
  - name: app
    from: ubuntu:22.04
    cuda: true
    install:
      apt:
        packages: [python3-pip, python3-dev, libopenblas-dev, libgomp1]
      pip:
        - packages: ["torch==2.8.0"]
          cuda: true
        - packages: ["numpy==1.26.4", transformers, flask]
    copy:
      - from: local
        paths: [app.py]
    cmd: [python3, app.py]
```

`cuda: true` on the stage says it runs on the GPU. `cuda: true` on a pip group
says that group holds GPU wheels and should resolve from whichever index the
target's architecture implies. Nothing names a board, so the same file is
correct on every board with a profile.

## Resolution

The target's `gpu_arch` — which `wendy device info` already reports, alongside
`gpu_vendor` and `cuda_version` — selects a profile from a table compiled into
the CLI (`internal/stagefile/gpu`):

| `gpu_arch` | board | CUDA | index | runtime | collect dir |
|---|---|---|---|---|---|
| `sm_87` | Jetson Orin | 12.6 | `pypi.jetson-ai-lab.io/jp6/cu126/` | `nvidia-*-cu12` (13 packages) | `/opt/cuda12/lib` |

Only `sm_87` ships. Thor (`sm_110`) and Spark (`sm_121`) are deliberately
absent rather than guessed: a wrong index URL does not fail at build time, it
fails on the device as an absent kernel image, and a table entry nobody has
built against is worse than an error naming what is known. Adding a verified
board is a five-line change.

The host's own `cuda_version` is deliberately **not** an input. It can change
under a deployed image with a JetPack upgrade; baking it in would make that
image quietly wrong later. The wheel's runtime always wins.

### Where the architecture comes from

| command | source | if absent |
|---|---|---|
| `wendy run` | the selected device's `gpu_arch` | error naming `--gpu-arch` |
| `wendy build` | `--gpu-arch`, or the device if one is selected | error naming `--gpu-arch` |
| `wendy fleet run` | `--gpu-arch` only — a fleet has no single board | error naming `--gpu-arch` |
| compose / multi-service | the one device, asked once for the whole project | error naming `--gpu-arch` |

`wendy run` previously resolved its build file *before* connecting to a device,
so the build-file picker would show regardless of what happened next. A GPU
project cannot be compiled that way. Rather than reorder the command for
everyone, `runCommand` resolves the target early only when
`stagefile.NeedsGPUTarget(cwd)` says the project has a `cuda:` stage; step 2
reuses whatever that resolved. Every other project keeps the old ordering, and
no project pays an RPC for a question it does not have.

## What the compiler emits

For a `cuda:` stage the compiler adds, from the profile:

1. **The wheel index** on every `cuda: true` pip group (`--index-url`).
2. **The CUDA runtime**, as its own pip invocation with no index, spliced in
   directly after the last `cuda: true` group. Separate because merging it with
   the wheels would make the vendor index primary and PyPI an extra index,
   letting pip resolve `torch` from PyPI — the wrong-architecture wheel the
   split exists to prevent. Positioned before ordinary app dependencies so
   editing those never invalidates a several-hundred-megabyte layer.
3. **The collection step**: every `*.so*` under the installed `nvidia` package
   tree symlinked into the profile's `libDir`, that directory written to
   `/etc/ld.so.conf.d/000-stagefile-cuda.conf`, and `ldconfig`. The tree is
   located by asking Python where it put the package
   (`import nvidia; os.path.dirname(nvidia.__file__)`) rather than by naming a
   `dist-packages` path, so a base-image Python bump cannot silently make it
   find nothing. A failed import fails the build, which is also the check that
   the runtime installed.
4. **`LD_LIBRARY_PATH`**, set to `libDir`. `ld.so.conf` alone is not enough:
   CDI-injected paths are searched ahead of `ld.so.conf.d` entries, and only
   `LD_LIBRARY_PATH` beats them. A Stagefile that sets the variable itself is a
   validation error rather than a silent rewrite.
5. **`USER root`**, unless the stage names a user. CUDA's memory manager opens
   `/dev/nvmap`, which is root-only on a Jetson; a GPU stage taking the
   non-root default would build clean and fail at the first allocation.

The generated output is substantively identical to the hand-written form it
replaces, with the `dist-packages` hardcoding removed.

## Reproducibility

The resolved profile is written to `build.stagefile.lock.yaml`, keyed by
architecture:

```yaml
cuda:
    sm_87:
        board: NVIDIA Jetson Orin
        cudaVersion: "12.6"
        index: https://pypi.jetson-ai-lab.io/jp6/cu126/
        runtime: [nvidia-cuda-runtime-cu12, ...]
        libDir: /opt/cuda12/lib
```

This is the one input to a GPU build that lives in the CLI rather than the
project. Without the pin, upgrading the CLI could rebuild an app against a
different CUDA runtime with nothing in the project having changed. Recording it
puts that on the same footing as a base-image digest — visible in the diff, and
changed only deliberately. A project deploying to several boards accumulates
one entry per board.

## Validation

The errors are the feature, not a schema formality. Each names the fix, because
the reader is someone who does not already know that a GPU wheel needs a
different index from the runtime that accompanies it:

- `install.pip[N] sets cuda: but the stage does not` — add `cuda: true` to the
  stage so the runtime is installed and put on the loader path.
- `install.pip[N] sets both cuda: and index/extraIndex` — `cuda:` resolves the
  index from the architecture; drop one or the other.
- `stage sets env.LD_LIBRARY_PATH and cuda:` — the collected libraries must come
  first on that path.
- `no CUDA profile for GPU architecture "sm_X" (known: sm_87)` — with the
  suggestion to use `install.pip[].index` by hand, so an uncovered board is
  never a dead end.

A `cuda:` stage compiled with no profile is an error, never a CPU-only image:
that difference would only surface on the device.

## Surface changes

- `install.pip[].cuda` (new), `stages[].cuda` (new).
- `sharedLibraries:` **removed** from the public schema. It survives as the
  internal lowering of `cuda:`, its only consumer. Taken while Stagefile is
  unreleased and every in-repo Stagefile is ours.
- `--gpu-arch` on `wendy build`, `wendy run`, `wendy fleet run`.

## Verification status

- Compile-verified: all three CUDA examples generate Dockerfiles matching the
  hand-written originals in substance. `TestShippedExamplesParseAndValidate`
  now also runs codegen over every shipped Stagefile, so an example that stops
  lowering fails CI rather than being noticed at build time.
- Unit-tested: profile resolution, lockfile pinning and pin-beats-table,
  validation errors, and each of the five things the lowering emits.
- **Not hardware-verified.** No Orin build was run. The soname-shadowing this
  exists to work around only manifests on-device.

## Open

- Thor and Spark profiles, once someone has built against their wheel indexes.
- HelloLLM's `RUN mkdir -p /usr/local/bin` containerd-overlayfs workaround
  still has no Stagefile representation (carried over from the previous round).
