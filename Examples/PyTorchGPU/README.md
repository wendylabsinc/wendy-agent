# PyTorch GPU Example with CDI

This example demonstrates GPU access via CDI (Container Device Interface) using PyTorch.

## What This Tests

- ✅ CDI device mounting (`/dev/dri/*`)
- ✅ NVIDIA library mounts (libcuda.so, libcudnn.so, etc.)
- ✅ PyTorch CUDA availability
- ✅ GPU tensor operations
- ✅ Matrix multiplication on GPU

## Base Image

Uses `ubuntu:22.04` (~100MB) instead of dustynv/l4t-pytorch (~8GB+)

The PyTorch wheel is NVIDIA's JetPack-6 / **CUDA 12.6** ARM64 build, which includes kernels
for Orin's `sm_87` GPU. The image also **bundles the CUDA-12 runtime** so it runs on
JetPack 7 (WendyOS 0.17), whose host CUDA is 13 — CUDA 12.6 runs on the JetPack-7 GPU driver
via backward compatibility. Only the GPU **driver** comes from CDI at runtime.

> **Why CUDA 12 on JetPack 7:** the CUDA-13 `sbsa` wheels are built only for Thor/Spark
> (`sm_110`/`sm_121`) and have no Orin `sm_87` kernels — they crash with *"no kernel image
> is available"*. CUDA 12.6 is the only prebuilt PyTorch that runs on Orin. Works
> GPU-accelerated on JetPack 6 and 7.

## PyTorch Installation Reference

Official NVIDIA documentation: [Install PyTorch for Jetson Platform](https://docs.nvidia.com/deeplearning/frameworks/install-pytorch-jetson-platform/index.html)

This example does not name a wheel index. `build.stagefile.yaml` marks the
stage and the PyTorch pip group with `cuda: true`, and the compiler resolves
the index — along with the CUDA version, the matching runtime packages and the
loader path they need — from the `gpu_arch` the target device reports:

```yaml
cuda: true
install:
  pip:
    - packages: ["torch==2.8.0", "torchvision==0.23.0", "torchaudio==2.8.0"]
      cuda: true
    # NumPy pinned to 1.x — the GPU PyTorch wheels are compiled against it.
    - packages: ["numpy==1.26.4", scipy, matplotlib]
```

Nothing here names a board, so the same file is correct on an Orin (which
resolves to the Jetson AI Lab JetPack-6 / CUDA-12.6 index) and on a Thor. The
resolved profile is recorded per architecture in `build.stagefile.lock.yaml`.

**Important:** Always pin specific versions to ensure CUDA-enabled builds, not CPU-only versions from default PyPI.

## Running

```bash
cd Examples/PyTorchGPU
wendy run --device wendyos-happy-rover.local
```

## Expected Output

```
╔════════════════════════════════════════════════════════════════════╗
║               WendyOS CDI GPU Test with PyTorch                    ║
╚════════════════════════════════════════════════════════════════════╝

CDI Environment Check
======================================================================
NVIDIA Environment Variables:
  NVIDIA_VISIBLE_DEVICES: all
  NVIDIA_DRIVER_CAPABILITIES: all

NVIDIA Device Files (from CDI):
  /dev/dri/card0: ✓ exists
  /dev/dri/renderD128: ✓ exists

CUDA Libraries (from CDI mounts):
  /usr/lib/aarch64-linux-gnu/libcuda.so: ✓ exists
  /usr/lib/aarch64-linux-gnu/nvidia/libcuda.so.1.1: ✓ exists
  /usr/lib/aarch64-linux-gnu/nvidia/libcudnn.so: ✓ exists

PyTorch GPU Test
======================================================================
✓ PyTorch imported successfully
  PyTorch version: 2.8.0

CUDA Available: True
Number of GPUs: 1

GPU Information:
  Device Name: Orin Nano
  Compute Capability: 8.7
  Total Memory: 7.46 GB

Running GPU Computation Test...

GPU Computation Results:
  Matrix size: 1000x1000
  Operation: matrix multiplication
  Time taken: 12.34 ms
  Result shape: torch.Size([1000, 1000])
  Result device: cuda:0
  Successfully copied result to CPU: torch.Size([1000, 1000])

✓ GPU computation SUCCESSFUL!
  CDI correctly mounted NVIDIA libraries and GPU is working!

✓ ALL TESTS PASSED - GPU is working correctly via CDI!
```

## Troubleshooting

### Issue: Container shows CPU-only PyTorch version

**Symptoms:**
```
PyTorch successfully imported
Version: 2.8.0+cpu  # Wrong! Should be the CUDA build (2.8.0), not +cpu
CUDA Available: False
```

**Cause:** Containerd on the device has cached old image layers with the wrong PyTorch version.

**Solution:** Clear containerd cache on the device:

```bash
# SSH into the device
ssh edgeos@wendyos-patient-cedar.local

# Option 1: Clear specific PyTorch images
sudo ctr -n default images ls | grep pytorch
sudo ctr -n default images rm $(sudo ctr -n default images ls -q | grep pytorch)

# Option 2: Clear all container images (more aggressive)
sudo ctr -n default images ls
sudo ctr -n default images rm $(sudo ctr -n default images ls -q)

# Option 3: Clear snapshots (if corruption occurred during layer extraction)
sudo ctr -n default snapshots ls
sudo ctr -n default snapshots rm $(sudo ctr -n default snapshots ls -q)
```

After clearing the cache, rebuild and redeploy:

```bash
# On your laptop
docker rmi pytorchgpu:latest
docker build --platform linux/arm64 --no-cache -t pytorchgpu:latest .
wendy-dev run
```

### Issue: CUDA libraries not found during build

**Symptoms:**
```
OSError: libcudart.so.12: cannot open shared object file: No such file or directory
ValueError: libcublas.so.*[0-9] not found in the system path
```

**Expected behavior:** This is normal during build! CUDA libraries are not available in the Docker build environment on your laptop. They will be injected by CDI at runtime on the device.

**Solution:** Don't try to import torch during the build. Use `pip3 show torch` to verify the version instead.

### Issue: LD_LIBRARY_PATH not set

**To investigate:** Check the CDI specification on the device:

```bash
ssh edgeos@wendyos-patient-cedar.local
cat /var/run/cdi/nvidia.yaml 2>/dev/null || cat /etc/cdi/nvidia.yaml
```

The CDI spec should include environment variables for CUDA libraries.
