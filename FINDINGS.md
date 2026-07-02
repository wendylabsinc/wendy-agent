# FINDINGS — Thor-specific issues

Issues that would not reproduce on Orin / Raspberry Pi / VM. Each entry: symptom, repro, evidence, severity, suspected layer.

## F1 — tegrastats GPU fallback parser yields no GPU stats on Thor (JetPack 7.2 format change)

**Filed: WDY-1803** (2026-07-02)

- **Symptom:** `hoststats.ParseTegrastats` (go/internal/agent/hoststats/gpu.go) matches `GR3D_FREQ (\d+)%` and `VDD_GPU_SOC (\d+)mW`. Thor's tegrastats (JetPack 7.2, kernel 6.8.12-l4t-r39.2.0) emits **neither**: there is no `GR3D_FREQ` field at all (even with the GPU at 85–98% util per nvidia-smi), and GPU power is reported as `VDD_GPU`, not `VDD_GPU_SOC`. The fallback therefore returns nil → no GPU panel/stats if nvidia-smi is ever missing or fails.
- **Repro:** `ssh wendy@wendyos-curious-meteor.local tegrastats --interval 500 | head -2` while HelloVLM runs inference; grep for `GR3D` → no match.
- **Evidence:** Thor tegrastats line (2026-07-02 13:45): `... RAM 36529/125749MB ... cpu@60.406C tj@63.281C ... gpu@62.906C ... VDD_GPU 37528mW/37528mW/37528mW VDD_CPU_SOC_MSS 18961mW ...` — no GR3D_FREQ. Simultaneous `nvidia-smi --query-gpu=utilization.gpu` → 85%.
- **Impact/severity:** Low today (nvidia-smi is present on JP7 and preferred by `SampleGPU`), but the fallback is silently dead on Thor; on any JP7 image without nvidia-smi the GPU panel disappears. Also `ParseTegrastats` labels the GPU "Integrated GPU" and would report power=nil on Thor.
- **Layer:** agent (`hoststats`).

## F3 — WENDY_CUDA_VERSION build-arg hint is empty on Thor (CUDA detection misses JetPack 7.2 layout)

- **Symptom:** `wendy run` injects device build-arg hints so templates can pick generation-correct base images (run.go:1779 explicitly tells Thor templates to branch on `WENDY_CUDA_VERSION` "for finer pins"). On Thor every hint arrives except CUDA: probe container env shows `PROBE_JP_MAJOR=7, PROBE_JP_VERSION=7.2, PROBE_GPU_VENDOR=nvidia, PROBE_HAS_GPU=true, PROBE_CUDA=""` (empty).
- **Root cause (verified on device):** `detectCUDAVersion()` (agent_service.go:197) probes `/usr/local/cuda/version.{txt,json}` then `nvcc` on PATH. On the Thor WendyOS-0.16.1 image: there is **no `/usr/local/cuda` symlink** (only `/usr/local/cuda-13.2`), **no version.txt/version.json anywhere** (checked `/usr/local/cuda-13.2/` — modern CUDA dropped these files), and **nvcc is not on the agent's PATH** (it exists at `/usr/local/cuda-13.2/bin/nvcc`, which reports "release 13.2, V13.2.78"). All three probes miss → empty.
- **Repro:** deploy any app whose Dockerfile has `ARG WENDY_CUDA_VERSION` + `ENV PROBE_CUDA=${WENDY_CUDA_VERSION}`; query env. Host check: `ssh wendy@wendyos-curious-meteor.local 'ls /usr/local; ls /usr/local/cuda-13.2/version.* '`.
- **Suggested fixes (any of):** glob `/usr/local/cuda-*/bin/nvcc`; parse `nvidia-smi`'s "CUDA Version: 13.2" banner (nvidia-smi is already used for gpuArch detection and IS on PATH at /usr/sbin/nvidia-smi); or add the `/usr/local/cuda` symlink to the Thor OS image.
- **Impact/severity:** Medium — the documented mechanism for Thor-vs-Orin CUDA image selection silently degrades to the coarse JETPACK_MAJOR hint; any template pinning by CUDA version keeps its (JetPack-6/CUDA-12) default on Thor.
- **Layer:** agent (`detectCUDAVersion`) and/or OS image (missing cuda symlink). Thor-specific in practice: Orin JetPack 6 images ship the `/usr/local/cuda` symlink + version.json.

## F2 — static GPU-entitlement fallback device list is wrong for Thor (uvm major 487, missing nvidia1)

**Filed: WDY-1804** (2026-07-02)

- **Symptom:** `applyGPU` (go/internal/agent/oci/entitlements.go:159) hardcodes `/dev/nvidia0, nvidiactl, nvidia-uvm, nvidia-uvm-tools, nvidia-modeset` all as char major **195** minor 0, and adds a cgroup allow rule for major 195 only. On Thor the real nodes are: nvidia0=195:0, nvidia1=195:1, nvidiactl=195:255, nvidia-modeset=195:254, **nvidia-uvm=487:0, nvidia-uvm-tools=487:1**, nvidia-caps=501:*. If the CDI path is unavailable (spec generation failed / nvidia-ctk missing), the fallback (a) mknods nvidia-uvm with the wrong major (487 expected, 195 created) and (b) the device-cgroup does not allow major 487, so CUDA init (which requires nvidia-uvm) fails. `/dev/nvidia1` is also absent from the fallback.
- **Evidence (host):** `ls -l /dev/nvidia*` → `crw-rw-rw- 1 root root 487,0 /dev/nvidia-uvm`, `487,1 nvidia-uvm-tools`, `195,1 nvidia1`. CDI spec at /etc/cdi/nvidia.yaml does include nvidia0/nvidia1/uvm — so the primary path is fine; only the fallback is broken.
- **Impact/severity:** Medium-low: latent. Only bites when CDI is missing — but that is exactly the "minimal fallback" scenario the code comments promise works. Note uvm's major is dynamically allocated on all recent drivers, so this is not Thor-only in principle, but Thor (JP7/CUDA 13) is where 487 was observed.
- **Layer:** agent (`oci/entitlements.go`).
- **Status:** Verified 2026-07-02: the CDI path is fully healthy on Thor — in-container probe (autotest-gpu) saw all 6 nodes with correct majors incl. uvm 487:0 and nvidia1, open() RW OK, cuInit=0, CC 11.0, cuCtxCreate+cuMemGetInfo OK (131.8 GB unified), /usr/local/cuda-13.2 mounted, nvidia-smi in-container works (driver 595.78). So F2 only bites when /etc/cdi/nvidia.yaml and /var/run/cdi/nvidia.yaml are both absent and nvidia-ctk generation fails.

