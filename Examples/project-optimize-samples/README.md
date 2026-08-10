# `wendy project optimize` sample apps

> **These no longer trip any findings.** All four were converted from
> Dockerfiles to `build.stagefile.yaml`, and `wendy project optimize` analyses
> Dockerfiles, Containerfiles and compose services — not Stagefiles. Running
> the command inside any of these directories now reports nothing and exits 0.
>
> They are kept as a before/after record: each `EXPECTED.txt` lists the
> findings the sample used to produce and says, per finding, whether the
> Stagefile compiler makes it structurally impossible, leaves it true but
> unanalysed, or cannot express it at all. That accounting is the useful thing
> here now.
>
> To exercise the analyzer, point it at a project that still has a Dockerfile
> — `Examples/ClaudeOnDevice` and `Examples/HelloAudio` are the two left in
> this repository.

These were **intentionally un-optimized** sample projects. Do **not** copy
them as a starting point for a real app.

```bash
wendy project optimize            # human report (interactive) / JSON (CI)
wendy project optimize --json     # structured findings
wendy project optimize --fix      # apply the safe fixes (cache mount, .dockerignore, release flag)
wendy project optimize --agentic  # emit the context bundle for an AI agent
```

| Sample | Used to trip | Survives conversion |
|--------|--------------|---------------------|
| `rust-debug-no-cache/` | arch-image (amd64-on-arm64 **error**), build-cache (cargo), release-debug (cargo debug build), arch-image (no `.dockerignore`, single-stage) | the debug profile and the single stage; the amd64 pin is not expressible in a Stagefile at all |
| `python-cuda-mismatch/` | cuda-ml (x86 `nvidia/cuda` base on arm64; CPU torch wheel with `gpu` entitlement), build-cache (pip), arch-image (no `.dockerignore`) | both cuda-ml problems — a build-file format cannot fix a dependency set |
| `swift-debug-wendy-debug/` | release-debug (`swift build` missing `-c release`; `WENDY_DEBUG` declared but unused), build-cache (swift), arch-image (no `.dockerignore`, single-stage) | the debug profile, the unused arg, and the single stage |
| `dockerfile-hygiene/` | apt-install, pip-flags, node-ci, add-copy, image-hygiene (`FROM :latest`, broad `COPY --from`, shell-form `CMD`), build-cache, arch-image | nothing — every one of its findings is mechanical, and the compiler emits the correct form |

None of these four has ever been buildable: they carry no Cargo.toml, no
Package.swift, and install steps that fail on their own base images. They are
read by static analysis, not built. Each `EXPECTED.txt` says so explicitly.
