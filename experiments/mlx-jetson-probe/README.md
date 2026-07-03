# MLX-on-Jetson feasibility probe

Validates Gabriele's MLX CUDA work on an NVIDIA Jetson (AGX Thor) using the
Swift stack: `wendylabsinc/mlx-swift` (CUDA backend) via
`wendylabsinc/mlx-swift-lm` (LM runtime). The probe is a small CLI that loads
an MLX checkpoint, generates text, and prints tokens/second — directly
comparable with the llama.cpp numbers measured for HelloVLM on the same
device (gemma-3-4b Q4: ~56 tok/s decode, gemma-3-27b Q4: ~12 tok/s).

## Status (2026-07-03)

All three stages proven on the physical Thor:

- **MLXProbe** — text generation, ~44 tok/s (gemma-3-4b-it-4bit; llama.cpp
  Q4 on the same device: ~56 tok/s).
- **MLXVisionProbe** — camera (or `--image-file`) → GStreamer 896×896 RGB →
  `UserInput.Image.array` → gemma-3 vision tower, correct descriptions at
  ~42 tok/s. Uses the Linux image path added on `mlx-swift-lm`
  branch `kb/linux-image-path` (draft PR wendylabsinc/mlx-swift-lm#1).
- **MLXServer** — minimal OpenAI-compatible server (`/v1/models`,
  non-streaming `/v1/chat/completions` with data-URI JPEG images) around the
  same stack, so HelloVLM's app runs against MLX **unchanged** — a drop-in
  alternative to the llama.cpp `llm/` service:

  ```sh
  MLXServer --model-path /models/mlx/gemma-3-4b-it-4bit --port 11434
  ```

  One request at a time (like `llama-server -np 1`); images are decoded via
  GStreamer `jpegdec` at the model's native size, so no MLX-side resize.

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

## Next steps

1. Finalize the Dockerfile from the on-device dev-container recipe
   (cudnn-frontend + CUTLASS headers, CUDA 13 `host_config.h` clang cap
   patch, gfortran, dpkg path-exclude for the read-only CDI glvnd mount)
   so `wendy run` can deploy MLXServer as a proper `llm-mlx/` service.
2. Bicubic resize parity in `MLXImageProcessing` (currently bilinear).
3. Converge with upstream ml-explore/mlx-swift-lm#321 when it lands.

Note for macOS consumers of the same package graph: plain SwiftPM does not
compile Metal shaders — build through Xcode/`xcodebuild` (upstream mlx-swift
documents this). Also, the fork's `.gitmodules` references the mlx submodule
over SSH (`git@github.com:wendylabsinc/mlx.git`), which breaks resolution on
machines without GitHub SSH keys; workaround:
`git config --global url."https://github.com/".insteadOf "git@github.com:"`.
