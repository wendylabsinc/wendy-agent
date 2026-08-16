# DGX Spark SpeechLLM — Ultravox 70B

Runs the 70B-parameter Ultravox SpeechLLM locally on an NVIDIA DGX Spark
managed by WendyOS. The demo uses llama.cpp's CUDA 13 backend, fully offloads a
high-quality Llama 3.3 70B Q6_K model to the GB10 Blackwell GPU, listens to the
Spark's microphone, and speaks replies with a local Kokoro neural voice.

Ultravox accepts speech plus a text instruction and returns text. Unlike a
Whisper-then-LLM pipeline, its audio encoder projects speech directly into the
LLM, so it can reason about wording, tone, pauses, and other audible cues in
addition to transcribing. Audio, prompts, generation, and speech synthesis all
stay on the DGX Spark after the initial model download.

## 70B memory profile

This is deliberately much larger than the compact 8B demo and is sized for the
DGX Spark's 128 GB unified memory:

| Allocation | Approximate size |
| --- | ---: |
| Llama 3.3 70B, Q6_K | 57.9 GB |
| Ultravox v0.5 F16 speech projector | 1.38 GB |
| 16K KV cache, CUDA work buffers, voice service, and OS | within the remaining headroom |

The model and projector downloads total about 59.3 GB. The entrypoint requires
a Spark-class system with at least 100 GiB total memory and 72 GiB currently
available before it starts. Keep at least 80 GB of free persistent storage.

## Requirements

- NVIDIA DGX Spark / GB10 with 128 GB unified memory (`arm64`, CUDA 13,
  compute capability 12.1 / `sm_121`).
- WendyOS with the `gpu`, `audio`, host networking, HTTP, and persistence
  entitlements enabled as provided in `wendy.json`.
- A microphone selected as the default PipeWire input, or an ALSA/PipeWire
  source supplied with deployment environment overrides.
- Internet access for the first model and TTS download.

The official `ghcr.io/ggml-org/llama.cpp:server-cuda13` image publishes a native
Linux ARM64 build. Wendy's GPU entitlement supplies the NVIDIA driver and CDI
device mounts at runtime; the stagefile does not use `cuda: true` because the
base image already contains the matching CUDA userspace.

## Run

```sh
cd Examples/ROCMSpeechLLM
wendy run --device <your-dgx-spark>.local
```

The first start downloads two Q6_K model shards, the speech projector, and the
Kokoro voice directly on the Spark. Downloads are resumable, use multiple
connections, and are pinned to exact upstream revisions. They live in the
`dgx-spark-speechllm-models` persistent volume, so redeploys reuse them instead
of moving roughly 60 GB through the development machine.

When the log says `Ultravox Live listening`, open:

```text
http://<your-dgx-spark>.local:8080
```

Press the orb to listen through the Spark's default microphone. The browser UI
shows each detected voice turn, streams the 70B model's reply, and plays the
reply using local Kokoro TTS. You can also type messages without enabling the
microphone.

The assistant preloads a dated, offline summary of Wendy and WendyOS from
`wendy_common_knowledge.md`, so it can answer common product, workflow, CLI,
platform, and entitlement questions without internet access. Update that file
when the public product or documentation changes. To use a different reference
at runtime, set `WENDY_KNOWLEDGE_PATH` to a non-empty UTF-8 text or Markdown
file; startup fails clearly if the configured file cannot be loaded.

Try a request that uses the SpeechLLM rather than plain transcription:

```text
Summarize what I asked for, then describe the tone or emphasis that supports
your interpretation.
```

Short, clear turns work best. Capture is normalized to 16 kHz mono PCM before
it is sent to Ultravox.

## Audio source overrides

The default profile uses PipeWire's selected default source. To pin an ALSA USB
microphone, override the environment at deploy time:

```sh
wendy run --device <your-dgx-spark>.local \
  --env AUDIO_BACKEND=alsa \
  --env AUDIO_SOURCE="USB microphone" \
  --env ALSA_CAPTURE_DEVICE="plughw:CARD=Microphone,DEV=0"
```

Use `arecord -L` on the Spark to find an ALSA capture name.

## Tuning

The defaults favor a single high-quality interactive session:

- `CONTEXT_SIZE=16384` provides a 16K shared context and KV cache.
- `FLASH_ATTENTION=on` enables llama.cpp's CUDA Flash Attention path.
- `GPU_LAYERS=all` fully offloads the 70B model and speech projector.
- `--cache-ram 0` disables the optional 8 GB host prompt cache.
- `--no-mmap` avoids retaining a large file-backed mapping while loading the
  weights into Spark's unified memory.
- `TTS_THREADS=8` keeps Kokoro synthesis on CPU and leaves the GPU to Ultravox.

Reduce `CONTEXT_SIZE` if another workload needs more unified memory. Changing
context size affects the KV cache, not the roughly 59.3 GB of model weights.

## Troubleshooting

- **`/dev/nvidia0 is missing`** — verify that `wendy.json` still has the `gpu`
  entitlement and that NVIDIA CDI is healthy on the Spark.
- **`/dev/nvidiactl is missing`** — the GPU control devices were not mounted
  into the container; restart the NVIDIA container runtime/CDI service.
- **Not enough free unified memory** — stop other GPU and memory-heavy
  workloads. The preflight intentionally stops before downloading when less
  than 72 GiB is available.
- **An interrupted first run** — rerun `wendy run`; aria2 resumes partial
  files, and completed artifacts remain in the persistent volume.
- **No microphone is detected** — select a default input in PipeWire or use the
  ALSA overrides above. The `audio` entitlement must remain enabled.
- **Audio is treated as text-only** — confirm the startup log says the
  multimodal projector loaded, then speak again or reset the conversation.
- **The service never becomes ready** — inspect startup output for a CUDA
  backend line and compute capability 12.1. A CPU-only load is not expected for
  this profile.

## Model sources

- Text backbone: `bartowski/Llama-3.3-70B-Instruct-GGUF`, Q6_K (Llama 3.3
  Community License), revision `b6c5c9f176f3279204034e1d16d393105e95cb88`.
- Speech projector:
  `steampunque/ultravox-v0_5-llama-3_3-70b-MP-GGUF`, derived from
  `fixie-ai/ultravox-v0_5-llama-3_3-70b` (MIT adapter; Llama backbone), revision
  `8b7e699d53719d33cf84e96871273e7a54876fed`.
- TTS: `k2-fsa/sherpa-onnx` 1.13.4 with `kokoro-multi-lang-v1_0`.
- Runtime: `ghcr.io/ggml-org/llama.cpp:server-cuda13`.
