# HelloONNX — ONNX GPU Inference on Jetson Orin

Demonstrates running [ONNX Runtime](https://onnxruntime.ai/) inference on the
NVIDIA GPU of a Jetson Orin device using CDI (Container Device Interface) for
CUDA library injection.

## What This Demonstrates

- Installing `onnxruntime-gpu` from the [Jetson AI Lab](https://pypi.jetson-ai-lab.io/) package index
- Suppressing the harmless DRM device-discovery warning (WDY-1131)
- Explicitly selecting `CUDAExecutionProvider` for GPU inference
- Running a 2-layer MLP forward pass on GPU and measuring throughput

## The DRM Warning (WDY-1131)

On Jetson Orin, ONNX Runtime logs this warning during startup:

```
[W:onnxruntime:Default, device_discovery.cc:164] GPU device discovery failed:
  Failed to open file: "/sys/class/drm/card0/device/vendor"
```

**This is non-fatal.** ORT checks that sysfs path to identify NVIDIA PCI GPUs.
On Jetson, the GPU is an SoC platform device (not PCI), so the path does not
exist. CUDA inference still works correctly through CDI-mapped CUDA libraries.

The fix is one line before any ORT call:

```python
import onnxruntime as ort
ort.set_default_logger_severity(3)  # suppress WARNING and below
```

## Installation (ONNX Runtime GPU on Orin, JetPack 6 / 7)

Uses NVIDIA's JetPack-6 / CUDA-12.6 `onnxruntime-gpu` wheel — the prebuilt build that
includes kernels for Orin's `sm_87` GPU. On JetPack 7 (WendyOS 0.17) the host provides
CUDA 13, so the image also **bundles the CUDA-12 runtime** (`nvidia-*-cu12`) and puts it
first on `LD_LIBRARY_PATH`; CUDA 12.6 runs on the JetPack-7 GPU driver via backward
compatibility. (The CUDA-13 `sbsa` builds are Thor/Spark `sm_110`/`sm_121` only — no Orin
kernels.)

None of that is written out here. `build.stagefile.yaml` declares `cuda: true`
on the stage and on the pip group holding the GPU wheel, and the compiler
resolves the rest — the CUDA version, the wheel index, the matching runtime
packages, the directory they are collected into, its loader precedence, and
running as root for `/dev/nvmap` — from the `gpu_arch` the target device
reports. Nothing in the file names a board, so it is as correct on a Thor as
on an Orin, and the resolved profile is pinned per architecture in
`build.stagefile.lock.yaml` so a CLI upgrade cannot move the app to a
different CUDA runtime behind your back.

```yaml
install:
  apt:
    packages: [python3-pip, python3-dev, libopenblas-dev, libgomp1]
  pip:
    - packages: [onnxruntime-gpu]
      cuda: true
    # onnx and numpy from standard PyPI. NumPy pinned to 1.x — the GPU wheel
    # is compiled against NumPy 1.x.
    - packages: [onnx, "numpy==1.26.4"]
```

> **Why the two pip groups are separate:** making the vendor index primary and
> PyPI an extra index lets pip resolve either package from either source, so it
> can silently pick the CPU-only `onnxruntime` from PyPI. A `cuda: true` group
> is emitted as its own `pip install` with only the GPU index, which is what
> prevents that.

## Running

```bash
cd Examples/HelloONNX
wendy run --device wendyos-your-device.local
```

## Expected Output

```
============================================================
  Hello ONNX — GPU inference on Jetson Orin via CDI
============================================================

============================================================
  ONNX Runtime
============================================================
  Version  : 1.23.x
  Providers: ['TensorrtExecutionProvider', 'CUDAExecutionProvider', 'CPUExecutionProvider']
  ✓ CUDAExecutionProvider is available

============================================================
  Building ONNX model
============================================================
  Model size: 1,024,xxx bytes
  ✓ 2-layer MLP (784 → 256 → 10) built

============================================================
  CUDA Inference
============================================================
  Active provider : CUDAExecutionProvider
  Batch size      : 32
  Output shape    : (32, 10)
  Avg latency     : x.xx ms / forward pass
  Throughput      : xxxx samples/s
  ✓ Inference successful on GPU

============================================================
  ✓ All tests passed — ONNX GPU inference is working!
============================================================
```

## Related

- [PyTorchGPU](../PyTorchGPU) — PyTorch GPU example using the same CDI approach
- [Jetson AI Lab PyPI](https://pypi.jetson-ai-lab.io/jp6/cu126/)
- Linear issue: [WDY-1131](https://linear.app/wendylabsinc/issue/WDY-1131)
