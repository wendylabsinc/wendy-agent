# MLX-on-Jetson feasibility probe

Validates Gabriele's MLX CUDA work on an NVIDIA Jetson (AGX Thor) using the
Swift stack: `wendylabsinc/mlx-swift` (CUDA backend) via
`wendylabsinc/mlx-swift-lm` (LM runtime). The probe is a small CLI that loads
an MLX checkpoint, generates text, and prints tokens/second — directly
comparable with the llama.cpp numbers measured for HelloVLM on the same
device (gemma-3-4b Q4: ~56 tok/s decode, gemma-3-27b Q4: ~12 tok/s).

## Why text-only (for now)

`mlx-swift-lm`'s vision path (MLXVLM) is **gated to Apple platforms**: all
image preprocessing (`MediaProcessing.swift`), the `CIImage` input type, and
parts of the VLM model classes are `#if os(macOS/iOS/...)`. On Linux, images
can only enter as raw `MLXArray` pixels or file URLs with no
decode/resize/normalize behind them. Until a Linux image pipeline exists in
`mlx-swift-lm` (JPEG decode + model-specific preprocessing), an MLX-backed
HelloVLM cannot see camera frames. Text generation is the honest first
milestone — it validates the CUDA backend, kernels, memory behavior, and
performance on Thor.

## Findings from the branch study (2026-07-02)

- `wendylabsinc/mlx-swift` `gab/demo-jetson-lm-v2`: CUDA builds via SPM
  (`SPM_CUDA=1`, on by default on Linux) with nvrtc-based runtime JIT
  (`CUDA_ARCH` env selects the SM target; Thor is `sm_110`, which needs
  CUDA 13 — or PTX forward-compat from `compute_90` on CUDA 12.9).
  Native arm64 build only; no cross-compile, no Dockerfile upstream —
  this directory's Dockerfile is the first.
- Their CI covers x86_64 + CUDA 12.9 only; aarch64/Jetson is unvalidated.
- `GPU+CUDA.swift` is a stub (`maxRecommendedWorkingSetBytes` returns nil).
- The "demo" on the branch is array ops only; no LM/VLM executable exists in
  either repo (the fork removed upstream's tools) — hence this probe.
- `mlx-swift-lm` pins `wendylabsinc/mlx-swift` at exact `0.0.1`.
- The stack does **not compile with Swift 6.3 on macOS**
  (`UserInput.Image.array(MLXArray)` violates `Sendable`); their CI uses
  Swift 6.2.3 on Linux. The Linux build path avoids the error.

## Running (needs the Thor / an arm64 host)

```sh
cd experiments/mlx-jetson-probe
wendy run --device <device> --builder apple-container
```

First start downloads the MLX checkpoint (default
`mlx-community/gemma-3-4b-it-4bit`, ~3 GB) into the `mlxprobe-models`
persist volume; a Hugging Face token at `/models/.hf-token` is used if
present. Then the probe prints generation output and a tok/s summary.

Override the model or prompt:

```sh
wendy run --device <device> -- --prompt "..." --max-tokens 100
# different checkpoint: set HF_MODEL in the Dockerfile or container env
```

`--device cpu` runs the same probe on CPU for a sanity baseline.

## What to look at

1. Does the CUDA build complete on arm64/CUDA 13 at all (first ever attempt)?
2. First-token latency (includes nvrtc JIT) and steady-state tok/s vs
   llama.cpp's 56 tok/s (gemma-3-4b Q4 class).
3. Memory behavior (unified memory; watch `nvidia-smi` / `free`).

## Next steps after a successful probe

1. Wrap in an OpenAI-compatible server → drop-in `llm/` alternative for
   HelloVLM (text-only at first).
2. Contribute a Linux image pipeline to `mlx-swift-lm` (JPEG decode +
   preprocessing) to unlock MLXVLM on Jetson — then HelloVLM gains a true
   MLX backend and converges with HelloMLX.
