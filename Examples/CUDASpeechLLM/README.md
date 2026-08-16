# CUDA SpeechLLM — Ultravox 8B

Runs the 8B-parameter Ultravox SpeechLLM locally on an NVIDIA CUDA device
managed by WendyOS. The demo uses llama.cpp's CUDA 13 backend, fully offloads a
Llama 3.1 8B Q4_K_M model to the GPU, listens through the device's ALSA
microphone, synthesizes replies with Kokoro, and writes them directly to ALSA.
It is a headless voice loop and does not require a browser.

Ultravox accepts speech plus a text instruction and returns text. Unlike a
Whisper-then-LLM pipeline, its audio encoder projects speech directly into the
LLM, so it can reason about wording, tone, pauses, and other audible cues in
addition to transcribing. Audio, prompts, generation, and speech synthesis all
stay on the DGX Spark after the initial model download.

## 8B memory profile

This compact profile leaves ample headroom on a DGX Spark and can run on
smaller CUDA systems:

| Allocation | Approximate size |
| --- | ---: |
| Llama 3.1 8B, Q4_K_M | 4.92 GB |
| Ultravox v0.5 F16 speech projector | 1.38 GB |
| 16K KV cache, CUDA work buffers, voice service, and OS | within a 12 GiB runtime budget |

The model and projector downloads total about 6.3 GB. The entrypoint requires
at least 16 GiB total system memory and 12 GiB currently available before it
starts. Keep at least 8 GB of free persistent storage.

## Requirements

- An NVIDIA CUDA 13-capable device with at least 16 GiB system memory and
  12 GiB available GPU or unified memory. DGX Spark / GB10 is the tested target.
- WendyOS with the `gpu`, `audio`, host networking, HTTP, and persistence
  entitlements enabled as provided in `wendy.json`.
- An ALSA-compatible microphone connected to the Spark.
- Internet access for the first model and TTS download.

The official `ghcr.io/ggml-org/llama.cpp:server-cuda13` image supplies the CUDA
userspace. Wendy's GPU entitlement supplies the NVIDIA driver and CDI device
mounts at runtime; the stagefile does not use `cuda: true` because the base
image already contains the matching CUDA userspace.

## Run

```sh
cd Examples/CUDASpeechLLM
wendy run --device <your-dgx-spark>.local
```

The first start downloads the Q4_K_M model, speech projector, and Kokoro voice
directly on the device. Downloads are resumable, use multiple connections, and
are pinned to exact upstream revisions. They live in the
`dgx-spark-speechllm-models` persistent volume, so redeploys reuse them instead
of moving roughly 6.3 GB through the development machine.

When the log says `SpeechLLM -> Kokoro -> ALSA ready`, start speaking. The
service listens automatically, detects the end of each voice turn, sends it to
Ultravox, synthesizes the reply with Kokoro, and plays it on ALSA's `default`
output device.

An optional status/control UI remains available at:

```text
http://<your-dgx-spark>.local:8080
```

The UI can show detected turns and stream the 8B model's text, but it is not in
the audio path. Kokoro and ALSA playback run in the device service even when no
browser is connected.

Try a request that uses the SpeechLLM rather than plain transcription:

```text
Summarize what I asked for, then describe the tone or emphasis that supports
your interpretation.
```

Short, clear turns work best. Capture is normalized to 16 kHz mono PCM before
it is sent to Ultravox.

## ALSA input and output

The default profile records from ALSA's `default` capture device. To pin a USB
microphone, override its ALSA capture name at deploy time:

```sh
wendy run --device <your-dgx-spark>.local \
  --env AUDIO_SOURCE="USB microphone" \
  --env ALSA_CAPTURE_DEVICE="plughw:CARD=Microphone,DEV=0"
```

Use `arecord -L` on the Spark to find an ALSA capture name.
If the selected source disappears or capture fails, the service rescans ALSA
hardware and tries newly attached capture devices automatically.

Replies play on ALSA's `default` PCM. To select a specific speaker, set
`ALSA_PLAYBACK_DEVICE` to a name shown by `aplay -L`:

```sh
wendy run --device <your-dgx-spark>.local \
  --env ALSA_CAPTURE_DEVICE="plughw:CARD=Microphone,DEV=0" \
  --env ALSA_PLAYBACK_DEVICE="plughw:CARD=Speaker,DEV=0"
```

Set `AUTO_LISTEN=false` only when you want the optional API/UI to control
capture instead of starting the headless loop automatically.

## Tuning

The defaults favor a single high-quality interactive session:

- `CONTEXT_SIZE=16384` provides a 16K shared context and KV cache.
- `FLASH_ATTENTION=on` enables llama.cpp's CUDA Flash Attention path.
- `GPU_LAYERS=all` fully offloads the 8B model and speech projector.
- `--cache-ram 0` disables the optional 8 GB host prompt cache.
- `--no-mmap` avoids retaining a file-backed mapping while loading the weights
  into GPU or unified memory.
- `TTS_THREADS=8` keeps Kokoro synthesis on CPU and leaves the GPU to Ultravox.

Reduce `CONTEXT_SIZE` if another workload needs more unified memory. Changing
context size affects the KV cache, not the roughly 6.3 GB model/projector pair.

## Troubleshooting

- **`/dev/nvidia0 is missing`** — verify that `wendy.json` still has the `gpu`
  entitlement and that NVIDIA CDI is healthy on the Spark.
- **`/dev/nvidiactl is missing`** — the GPU control devices were not mounted
  into the container; restart the NVIDIA container runtime/CDI service.
- **Not enough free unified memory** — stop other GPU and memory-heavy
  workloads. The preflight intentionally stops before downloading when less
  than 12 GiB is available.
- **`nvidia-smi` reports 0 MiB of GPU memory** — this is expected on GB10:
  CUDA allocates from unified system memory rather than a dedicated
  framebuffer. The entrypoint validates `/proc/meminfo` instead.
- **An interrupted first run** — rerun `wendy run`; aria2 resumes partial
  files, and completed artifacts remain in the persistent volume.
- **No microphone is detected** — run `wendy device audio list --device <ip>`
  and set `ALSA_CAPTURE_DEVICE` to the reported capture name. The `audio`
  entitlement must remain enabled.
- **A USB speaker is missing** — if it does not appear in `wendy device audio
  list`, it has not enumerated as a USB Audio device. Check that its USB cable
  carries data (some speakers use USB only for power), reconnect it directly,
  and set `ALSA_PLAYBACK_DEVICE` to the corresponding PCM from `aplay -L`.
- **Audio is treated as text-only** — confirm the startup log says the
  multimodal projector loaded, then speak again or reset the conversation.
- **The service never becomes ready** — inspect startup output for a CUDA
  backend line and compute capability 12.1. A CPU-only load is not expected for
  this profile.

## Model sources

- Text backbone and speech projector:
  `ggml-org/ultravox-v0_5-llama-3_1-8b-GGUF`, Q4_K_M plus F16 projector,
  revision `7a0280d66c0700c366c2c26586e0f0967f97bad0` (MIT Ultravox adapter;
  Llama 3.1 backbone).
- TTS: `k2-fsa/sherpa-onnx` 1.13.4 with `kokoro-multi-lang-v1_0`.
- Runtime: `ghcr.io/ggml-org/llama.cpp:server-cuda13`.
