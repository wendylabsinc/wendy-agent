# HelloLLM - Text Generation on NVIDIA Jetson

This example demonstrates GPU-accelerated text generation using HuggingFace Transformers on NVIDIA Jetson devices. It's based on the [NVIDIA Jetson AI Lab API Examples tutorial](https://www.jetson-ai-lab.com/tutorial_api-examples.html).

## What This Demonstrates

- Loading a language model (DistilGPT2) with CUDA support
- Simple text generation with performance timing
- Streaming text generation using `TextIteratorStreamer`
- Web interface for interactive prompt input
- Performance benchmarking (tokens/second)

## Model

Uses `distilbert/distilgpt2`:
- 82M parameters (66% smaller than GPT-2)
- ~500MB download size
- ~160MB GPU memory (float16)

This model is small enough to run efficiently on Jetson Orin Nano (8GB) while still demonstrating GPU-accelerated inference.

## Requirements

Runs GPU-accelerated on **NVIDIA Jetson Orin across WendyOS / JetPack 6 and 7** (verified on
an Orin Nano running WendyOS 0.17 / JetPack 7).

The example uses the JetPack-6 / **CUDA 12.6** PyTorch wheel — the only prebuilt PyTorch that
ships kernels for Orin's `sm_87` GPU. On WendyOS 0.17 (JetPack 7) the host provides CUDA 13,
so the image **bundles the CUDA-12 runtime** and puts it first on the loader path; CUDA 12.6
runs on the JetPack-7 GPU driver via backward compatibility. (The CUDA-13 `sbsa` PyTorch
wheels are built only for Thor/Spark `sm_110`/`sm_121` and have no Orin kernels — they load
but crash with *"no kernel image is available for execution on the device"*.) Bundling CUDA
12 makes the image larger (~2 GB) but keeps GPU acceleration working on JetPack 7.

## Running

```bash
cd Examples/HelloLLM
wendy run
```

By default, this:
1. Checks GPU availability
2. Downloads and loads the model (first run only)
3. Runs demo with predefined prompts + benchmark
4. Starts a web server for interactive use

### Command-Line Options

```bash
# Skip demo, go straight to web server
wendy run -- --skip-demo

# Run demo only, no web server
wendy run -- --demo-only

# Use a different port (default: 8080)
wendy run -- --port 3000
```

## Web Interface

After the demo completes, a web server starts on port 8080. Open your browser to:

```
http://wendyos-<device-hostname>.local:8080
```

For example: `http://wendyos-precise-peanut.local:8080`

The web interface provides:
- Text input for your prompts
- Slider to adjust max tokens (20-200)
- Real-time generation stats (tokens, time, tokens/sec)
- GPU status indicator (green badge if GPU enabled)

## First Run

The first run will download the model from HuggingFace Hub (~500MB). This may take 1-5 minutes depending on your network connection. Subsequent runs use the cached model.

## Expected Output

```
============================================================
  HelloLLM - Text Generation on NVIDIA Jetson
  Using HuggingFace Transformers + GPU Acceleration
============================================================

============================================================
  GPU Availability Check
============================================================
PyTorch Version: 2.8.0
CUDA Built: 12.6
CUDA Available: True
CUDA Version: 12.6
cuDNN Version: 92400
GPU Count: 1
GPU Name: Orin (nvgpu)
GPU Memory: 7.32 GB
Compute Capability: 8.7

============================================================
  Loading Model: distilbert/distilgpt2
============================================================
Loading tokenizer...
Loading model (this may take a moment on first run)...
Model loaded in 12.34 seconds
Model device: cuda:0
GPU Memory Used: 158.32 MB

============================================================
  Text Generation
============================================================
Prompt: "Once upon a time in a land far away,"
Max new tokens: 50

Generated 50 tokens in 0.82s (61.0 tok/s)
----------------------------------------
Once upon a time in a land far away, there lived a young
princess who dreamed of adventure beyond the castle walls...
----------------------------------------

...

============================================================
  Demo Summary
============================================================
Demo completed successfully!

  GPU: ENABLED
  Throughput: 58.3 tokens/second

============================================================
  Starting Web Interface
============================================================
Web interface available at: http://wendyos-device-name.local:8080
Press Ctrl+C to stop the server
```

## Troubleshooting

### CUDA Not Available

If you see "CUDA Available: False", check:

1. **GPU entitlement** - Ensure `wendy.json` includes the GPU entitlement:
   ```json
   "entitlements": [{"type": "gpu"}]
   ```

2. **NVIDIA runtime** - The Jetson must have nvidia-container-runtime installed

3. **Device files** - Check that `/dev/nvidia*` devices are accessible

### Network/DNS Errors

If you see "Failed to resolve 'huggingface.co'" errors:

1. **Network entitlement** - Ensure `wendy.json` includes network access:
   ```json
   "entitlements": [
       {"type": "gpu"},
       {"type": "network", "mode": "host"}
   ]
   ```

2. **Device connectivity** - Verify the Jetson has internet access

### `no kernel image is available for execution on the device`

PyTorch has no GPU kernels for the device's compute capability. This happens if you swap in
the CUDA-13 `sbsa` PyTorch wheels — they include only Thor/Spark (`sm_110`/`sm_121`) kernels,
not Orin's `sm_87`. Use the JetPack-6 / CUDA-12.6 wheel (as this example does), which
includes `sm_87`.

### `libcudart.so.12: cannot open shared object file`

The CUDA-12 runtime is missing. On JetPack 7 the host provides CUDA 13, so this example
bundles the CUDA-12 libraries (`nvidia-*-cu12`) into the image. Rebuild if you see this after
editing the image.

## Technical Details

- **PyTorch**: 2.8.0 from [Jetson AI Lab PyPI](https://pypi.jetson-ai-lab.io/jp6/cu126/) (CUDA 12.6; includes Orin `sm_87` kernels)
- **CUDA runtime**: CUDA 12.6 (`nvidia-*-cu12` wheels) bundled into the image and placed first on `LD_LIBRARY_PATH`, so it runs on JetPack 7's CUDA-13 GPU driver via backward compatibility
- **NumPy**: 1.26.4 (pinned; the JetPack-6 PyTorch wheel was compiled against NumPy 1.x)
- **Transformers**: Latest from PyPI
- **Flask**: Web server for interactive interface
- **Precision**: float16 on GPU, float32 on CPU

## Files

- `app.py` - Main application with demo and web server
- `build.stagefile.yaml` - Container definition; `cuda: true` resolves the CUDA
  version, wheel index and runtime from the device's reported `gpu_arch`
- `build.stagefile.lock.yaml` - Resolved base-image digest and CUDA profile, committed
- `wendy.json` - Wendy configuration with GPU and network entitlements
