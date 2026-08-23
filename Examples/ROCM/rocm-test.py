#!/usr/bin/env python3
"""
AMD ROCm GPU test for WendyOS.

PyTorch's ROCm build reports the GPU through the same torch.cuda.* API as CUDA
(HIP masquerades as "cuda"), so torch.cuda.is_available() is the availability
check on AMD too — torch.version.hip tells the two builds apart.

Needs the gpu entitlement, which on an AMD host exposes /dev/kfd (compute) and
/dev/dri/renderD* (the GPU) into the container.
"""
import os
import sys
import time

import torch


def line():
    print("=" * 60)


def report_devices():
    line()
    print("ROCm device nodes (from the gpu entitlement)")
    line()
    for path in ("/dev/kfd",):
        print(f"  {path}: {'✓' if os.path.exists(path) else '✗ MISSING'}")
    dri = sorted(p for p in os.listdir("/dev/dri")) if os.path.isdir("/dev/dri") else []
    render = [p for p in dri if p.startswith("renderD")]
    print(f"  /dev/dri render nodes: {render or '✗ NONE'}")


def report_torch():
    line()
    print("PyTorch build")
    line()
    print(f"  torch version: {torch.__version__}")
    print(f"  HIP (ROCm) version: {torch.version.hip or 'None — this is a CUDA/CPU build, not ROCm'}")
    print(f"  CUDA compat version: {torch.version.cuda or 'None'}")


def main():
    report_torch()
    report_devices()

    line()
    print("GPU availability")
    line()
    if not torch.cuda.is_available():
        print("✗ No ROCm GPU visible to PyTorch.")
        print("  Checklist:")
        print("   - torch.version.hip above must be set (ROCm wheel, not +cpu)")
        print("   - /dev/kfd and a /dev/dri/renderD* node must be present")
        print("   - the host needs the amdgpu kernel driver loaded")
        return 1

    n = torch.cuda.device_count()
    print(f"✓ ROCm available — {n} GPU(s)")
    for i in range(n):
        p = torch.cuda.get_device_properties(i)
        print(f"  GPU {i}: {p.name}  ({p.total_memory / 1024**3:.1f} GB, gfx {getattr(p, 'gcnArchName', '?')})")

    line()
    print("GPU matmul (correctness-checked)")
    line()
    a = torch.randn(2048, 2048, device="cuda")
    b = torch.randn(2048, 2048, device="cuda")
    torch.matmul(a, b)  # warm up
    torch.cuda.synchronize()
    start = time.time()
    c = torch.matmul(a, b)
    torch.cuda.synchronize()
    print(f"  2048x2048 matmul: {(time.time() - start) * 1000:.1f} ms")

    # Self-check: the GPU result must match a CPU recompute.
    expected = torch.matmul(a.cpu(), b.cpu())
    max_diff = (c.cpu() - expected).abs().max().item()
    print(f"  max |GPU - CPU| diff: {max_diff:.2e}")
    assert max_diff < 1e-2, f"GPU matmul disagrees with CPU (diff {max_diff})"
    print("✓ GPU computation verified correct")
    return 0


if __name__ == "__main__":
    sys.exit(main())
