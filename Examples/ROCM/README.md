# AMD ROCm GPU Example (x86)

Runs PyTorch on an **AMD** GPU via **ROCm** on an x86_64 WendyOS device — the
AMD counterpart to the NVIDIA `PyTorchGPU` example.

## What's different from the NVIDIA examples

WendyOS's `cuda: true` stagefile flag is NVIDIA/Jetson-only: it resolves a wheel
index and CUDA runtime from the device's `gpu_arch`. There is **no** ROCm
equivalent, so this example does the plain thing instead — an amd64 base image
plus PyTorch's ROCm wheel index:

```yaml
install:
  pip:
    - packages: ["torch", "torchvision"]
      index: "https://download.pytorch.org/whl/rocm6.2"
```

The ROCm runtime libraries are bundled inside the wheel. Only the kernel driver
(`amdgpu`) and its device nodes come from the host at runtime.

## Requirements

- An **AMD** GPU on an **x86_64** WendyOS device, with the `amdgpu` kernel
  driver loaded (so `/dev/kfd` and `/dev/dri/renderD*` exist on the host).
- The `gpu` entitlement (already in `wendy.json`).

On an AMD host, the `gpu` entitlement now exposes `/dev/kfd` (the ROCm compute
device) and the `/dev/dri/renderD*` node into the container, and adds the
`render`/`video` groups. Both nodes are required: with `/dev/kfd` alone ROCm
initializes but every GPU allocation fails.

> This device-enablement is the agent-side change this example ships with. Before
> it, the `gpu` entitlement only wired up NVIDIA nodes, so ROCm containers saw no
> GPU. `wendy device info` also now reports `GPU: amd` on these hosts.

## Running

```bash
cd Examples/ROCM
wendy run --device <your-amd-device>.local
```

## Expected output

```
PyTorch build
  torch version: 2.x.x+rocm6.2
  HIP (ROCm) version: 6.2.x
ROCm device nodes (from the gpu entitlement)
  /dev/kfd: ✓
  /dev/dri render nodes: ['renderD128']
GPU availability
✓ ROCm available — 1 GPU(s)
  GPU 0: AMD Radeon ...  (gfx1100)
GPU matmul (correctness-checked)
✓ GPU computation verified correct
```

## Troubleshooting

- **`HIP version: None` / `torch ...+cpu`** — the CPU wheel was installed. Make
  sure the `index:` line points at a `rocm*` index and isn't shadowed by a plain
  PyPI pin.
- **No GPU visible / `/dev/kfd` missing** — the host has no `amdgpu` driver
  loaded, or the GPU isn't ROCm-supported. `torch.version.hip` being set but
  `torch.cuda.is_available()` False almost always means the device nodes didn't
  reach the container (check the `gpu` entitlement).
- **`HIP error: invalid device function`** — the ROCm wheel has no kernels for
  your GPU's `gfx` arch. The stagefile sets `HSA_OVERRIDE_GFX_VERSION=11.0.0` so
  a newer RDNA3.5 APU (gfx1151, e.g. Radeon 8060S / Strix Halo) runs the gfx1100
  kernels — verified against reality on the ROG Flow Z13. For a different card,
  set the override to the nearest supported arch (gfx1030 → `10.3.0`,
  gfx1100 → `11.0.0`), or drop it entirely if your GPU has native kernels.
