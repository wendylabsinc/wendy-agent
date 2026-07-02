# HelloVLM — Webcam + Vision Language Model on Jetson

HelloVLM is a Thor-first port of HelloMLX: a web UI on port 8080 shows the
live camera feed, lets you edit a prompt, and continuously runs that prompt
plus the most recent camera frames through a local vision language model.
Every completed pass is persisted as a "run" (prompt, response, frames,
timing) and shown in the UI.

Instead of HelloMLX's Mac-only stack (AVFoundation + MLX), HelloVLM uses:

- **Linux V4L2** (MJPG) for camera capture
- **llama.cpp's `llama-server`** with `gemma-3-4b-it` (Q4_K_M) for inference,
  served over the OpenAI-compatible `/v1/chat/completions` API

## Layout

Two deployables that share the app id `sh.wendy.examples.hellovlm`
(the HelloMultiService pattern):

```text
Examples/HelloVLM/
  app/   Swift app: camera capture, web UI, run history, VLM client
  llm/   VLM backend: llama-server + baked-in model, GPU-accelerated
```

## Requirements

- A WendyOS device with an NVIDIA GPU (tested on Jetson AGX Thor,
  WendyOS 0.16.1 / JetPack 7.2)
- A UVC webcam that can deliver MJPG frames (most USB webcams; tested
  with a Logitech Brio 100)
- ~4 GB free disk on the device for the backend image

## Running

Deploy the backend once (stays running across app iterations):

```sh
cd Examples/HelloVLM/llm
wendy run --device <device> --detach
```

The first build downloads the model weights (~3.3 GB) into the image;
Docker caches the layer, so subsequent builds are fast.

Then run the app:

```sh
cd ../app
wendy run --device <device>
```

Open `http://<device>:8080`. The first inference after the backend starts
takes ~30s extra (one-time CUDA kernel compilation for new GPU
architectures like Thor's sm_110); after that, expect ~1.5 s of prompt
processing per frame and 50–100 generated tokens/s on Thor.

Useful app flags (set in `app/wendy.json` under `run.args`):

- `--prompt` — the standing prompt; can also be edited live in the UI
- `--llm-url` — any OpenAI-compatible endpoint (default `http://localhost:11434`)
- `--model` — model name to request (default: first model the backend reports)
- `--interval` / `--fps` / `--max-frames` — how much camera history each pass sends
- `--camera` — camera name substring, if the device has several

## VLM vs. LLM

A plain LLM is text-in/text-out. A vision language model (VLM) pairs a
vision encoder with a language model: the user message carries images plus
text, the output is text. The API looks like a normal chat API — but the
model must actually be vision-capable. Sending images to a text-only model
fails or silently ignores them. `gemma-3-4b-it` with its `mmproj-model-f16`
projector (both baked into the backend image) is vision-capable.

## Why llama-server and not Ollama?

The backend image is built **from** the official Ollama image, but runs the
bundled `llama-server` directly instead of `ollama serve`. Ollama
unconditionally disables vision-projector GPU offload on integrated
non-Metal GPUs (`shouldDisableMMProjOffload` in `llm/llama_server.go`,
reason `shared-memory-gpu`) — and every Jetson is an integrated-GPU
machine, even a Thor with 122 GB of unified memory.

Measured on AGX Thor with `gemma3:4b` (Q4_K_M), single 640×480 frame:

| Backend | Image prefill | Decode |
| --- | --- | --- |
| Ollama (`ollama serve`) | ~60 s (vision encoder on CPU) | ~44 tok/s |
| `llama-server`, `-ngl 999` | **~1.5 s** | **50–100 tok/s** |

The app only speaks the OpenAI-compatible API, so you can point `--llm-url`
at a stock Ollama, vLLM, or any other compatible server if you prefer —
it is just slow for vision on Jetson until the upstream Ollama heuristic
learns that "integrated" does not mean "small".

## Troubleshooting

- **Model status stuck on `loading`** — the backend is still starting.
  Check it with `wendy device logs --app sh.wendy.examples.hellovlm --service llm`.
- **`Camera does not support MJPG frames`** — the app requests MJPG so
  frames go straight from the camera to JPEG without re-encoding. Check
  formats with `v4l2-ctl -d /dev/video0 --list-formats` on the device.
- **Green or corrupted first frames** — one-shot V4L2 captures return
  frames before exposure settles; the app keeps the camera streaming and
  skips the first 10 frames, so this should not appear in the UI.
- **Port 11434 already in use** — another Ollama-style backend is running
  on the device (e.g. a previously deployed LLM app). Either remove it or
  point `--llm-url` at it instead of deploying the `llm` service.
- **Very slow responses** — confirm the model is on the GPU: run
  `nvidia-smi` on the device during inference; also see the tokens/s stats
  recorded with each run in the UI.
