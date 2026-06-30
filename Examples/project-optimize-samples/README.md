# `wendy project optimize` sample apps

These are **intentionally un-optimized** sample projects. Each one trips a
different set of `wendy project optimize` findings. They exist as demos and as
manual-smoke fixtures — do **not** copy them as a starting point for a real app.

Run from inside any sample directory:

```bash
wendy project optimize            # human report (interactive) / JSON (CI)
wendy project optimize --json     # structured findings
wendy project optimize --fix      # apply the safe fixes (cache mount, .dockerignore, release flag)
wendy project optimize --agentic  # emit the context bundle for an AI agent
```

| Sample | Trips |
|--------|-------|
| `rust-debug-no-cache/` | arch-image (amd64-on-arm64 **error**), build-cache (cargo), release-debug (cargo debug build), arch-image (no `.dockerignore`, single-stage) |
| `python-cuda-mismatch/` | cuda-ml (x86 `nvidia/cuda` base on arm64; CPU torch wheel with `gpu` entitlement), build-cache (pip), arch-image (no `.dockerignore`) |
| `swift-debug-wendy-debug/` | release-debug (`swift build` missing `-c release`; `WENDY_DEBUG` declared but unused), build-cache (swift), arch-image (no `.dockerignore`, single-stage) |

Each `EXPECTED.txt` lists the findings that sample is designed to produce.
