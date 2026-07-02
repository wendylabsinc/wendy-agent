# TESTLOG — Thor autonomous test campaign (append-only)

Session start: 2026-07-02. Device: wendyos-curious-meteor.local (Jetson AGX Thor, WendyOS-0.16.1, JetPack 7.2, agent 2026.07.02-dev-wdy1799, CLI 2026.07.01-101829).

## 2026-07-02

- [T0] Baseline `device apps list`: only `sh.wendy.examples.hellovlm` RUNNING (services app, llm). As expected. PASS.
- [T0] `device info --json`: deviceType=jetson-agx-thor, jetpackVersion=7.2, gpuArch=sm_110, storageMedium=nvme, osVersion=WendyOS-0.16.1, agent version 2026.07.02-dev-wdy1799. Partitions: / (25GB ext4 nvme0n1p1), /boot/efi, /config, /data (951GB ext4 nvme0n1p17). No memory field in output — checking whether that's expected.
- [T1] Switched to `dendy` (worktree-built dev CLI) per user instruction. `dendy --version` → "wendy version dev". Re-ran device info/camera list with dev CLI: identical results. PASS.
- [T1] Introspection sweep (task #1):
  - `device hardware list`: complete and Thor-correct — nvidia0+nvidia1, dri card0-2/renderD128-130, APE (controlC1 tegra-ape), HDA, Brio USB audio, 4x CAN, mgbe0-3 10G NICs, spidev, 14 i2c buses, NVMe WD SN5000S. PASS.
  - `device audio list`: 32 APE ADMAIF pcm devices as capture (type 1) AND playback (type 2), Brio 100 capture, 4x HDMI playback. Renders APE hardware fully. PASS (in-container mapping tested later).
  - `device volumes list`: 4 volumes, all usedBy:null incl. HelloVLM's mounted ones → O1.
  - `device top --json` (one-shot): memTotal 131857588224 (=125749 MiB, matches free/tegrastats), cpuCount 14, GPU "NVIDIA Thor" util 98%, temp 62C, power 37.9W, but GPU mem 0/0 → O2. HelloVLM container group mem 27.2GB. PASS otherwise.
  - SSH ground truth: model "NVIDIA Jetson AGX Thor Developer Kit", 128GiB RAM, 14 cores, kernel 6.8.12-l4t-r39.2.0-1021.21, /dev/nvidia0+nvidia1, uvm major 487 (!), CDI spec present /etc/cdi/nvidia.yaml (includes nvidia0/1, uvm, dri, v4l2-nvenc/nvdec; devices "0" and "all").
  - tegrastats on Thor: NO GR3D_FREQ field, VDD_GPU instead of VDD_GPU_SOC → F1 (fallback GPU parser dead on Thor).
  - Code inspection: applyGPU fallback hardcodes major 195 for uvm (real: 487) and misses nvidia1 → F2 (latent, CDI masks it).
  - `wendy cache list` (local): buildx 10.4GB, chunkmanifest 8.8MB, deploy 513B. OK.
- Findings so far: F1, F2. Observations: O1–O4.
- [T2] GPU entitlement test (task #2), app `autotest-gpu` (python simple-api base + ctypes CUDA probe, port 3101, gpu+network entitlements):
  - `wendy init --template simple-api --entitlement gpu` silently drops the gpu entitlement (template wendy.json used verbatim); confirmed with both installed and dev CLI; code: init_cmd.go runTemplateFlow bypasses resolveInitEntitlements → O5. Added gpu entitlement manually.
  - NOTE: `dendy` resolves the worktree by walking up from CWD — in /tmp it silently falls back to installed brew CLI. Used `<worktree>/go/bin/wendy` directly for project-dir commands.
  - Build (apple-container) + chunk deploy: 8.6s build, 9 layers, ready on 3101. PASS.
  - In-container /gpu probe: all 6 nvidia nodes present with CORRECT majors (uvm 487:0!), nvidia1 present, open() RW succeeds on ctl/0/1/uvm (cgroup rules correct via CDI), cuInit=0, 1 CUDA device "NVIDIA Thor" CC 11.0, cuCtxCreate OK, cuMemGetInfo total=131857588224 (full unified memory visible), /usr/local/cuda-13.2 mounted, NVIDIA_VISIBLE_DEVICES=all. nvidia-smi works in-container (driver 595.78, CUDA 13.2). PASS — CDI path fully healthy on Thor; F2 remains latent (fallback-only).
  - `device logs --app autotest-gpu` live streaming: works (JSON lines when not a tty).
  - `device logs --app autotest-gpu --tail N`: EMPTY even with fresh app logs; unfiltered `--tail 10` returns batches (all HelloVLM llm spam) → tail window is selected globally, then filtered by app → O6.
  - HelloVLM stderr INFO lines shown with severity=WARN → stderr→WARN mapping → O7.
  - `apps remove` headless without --force: clean error "confirm prompt: could not open a new TTY", rc=1 (no freeze — WDY-1796 shows as error here, OK). `--force` works.
  - Cleanup: autotest-gpu removed; device back to HelloVLM-only. VERIFIED.
