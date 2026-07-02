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
- [T3] Deploy churn (task #3), app `autotest-churn` (python simple-api, port 3100):
  - Volume lifecycle probe first: deployed with persist volumes `autotest-churn-data` (/data) + `autotest-orphanvol` (/data2). usedBy: prefixed vol attributed, non-prefixed None (O1 live-confirmed). `apps remove --force --delete-volumes` deleted the prefixed one, silently leaked `autotest-orphanvol` → O1 extended. Leak cleaned via `volumes remove --force` (note: `--json` doesn't imply non-interactive; TTY confirm error headless without --force).
  - Churn 3 rounds deploy→health→remove: each deploy 4s wall (build 1.5-1.6s, 9/9 layers reused, chunking 0), health 200 every round, remove <1s. No readiness flakes, no layer-cache issues, no leftover apps/volumes. Rootfs 28% used, /data 9% — stable. PASS.
  - Device baseline verified after test: HelloVLM only. VERIFIED.
- [T4] Swift cross-compile (task #4), app `autotest-swift` (swift/simple-api template, Hummingbird+OTel, port 3103):
  - First build FAILED after 87s: `✗ container build (OCI layout) failed: exit status 1` with zero build output, also with --debug → O8.
  - Manual `container build` revealed: build context transferred EMPTY (2B) → `COPY Package.swift: not found`. Reproduced for ALL /tmp contexts incl. fresh trivial alpine probe and the previously-working autotest-gpu dir; home-dir contexts fine. `container builder stop/start` didn't help → O9 (Apple container stack env issue; /tmp contexts silently empty; regressed between 15:53 and 15:58 local while builds from those exact dirs worked earlier).
  - Mitigation: moved test workspace to /Users/ai/wendy-thor-tests (deviation from HANDOVER's /tmp/wendy-thor-tests guidance, same intent). Cleaned up probe images.
  - Swift build retry from home dir: SUCCESS — cold cross-compile (Hummingbird 2.21 + swift-otel, swift:6.3-bookworm) 2m30s via apple-container, 6 layers, deployed, ready on 3103, `/` → hello-world, /health 200. Logs show swift-otel bootstrapped against on-device OTLP collector (127.0.0.1:4317, env-injected). Swift-on-ARM64 story holds on JetPack 7.2. PASS.
  - Removed with `--force --cleanup` (image cleanup path exercised, OK). Device back to HelloVLM-only. VERIFIED.
  - Note: Swift app's stderr INFO lines also labeled WARN (consistent with O7).
- [T5] Audio APE surface (task #5), app `autotest-audio` (python, audio entitlement, port 3104; Brio mic deliberately untouched, no audio app was deployed):
  - In-container: 75 /dev/snd nodes (bind mount complete), /proc/asound/cards shows all 3 cards (HDA, APE, B100) identical to host, controlC0/C1/timer open RW OK, ADMAIF playback pcmC1D0p opens OK, 32 APE playback + 32 capture pcm nodes visible, node perms 116:* mode 660 (audio gid 29 matches device), PipeWire system socket mounted at /run/pipewire/pipewire-0 with PIPEWIRE_RUNTIME_DIR set. Thor's APE/ADMAIF surface maps fully and sanely in-container. PASS.
  - Cleanup with --force --cleanup; device back to HelloVLM-only. VERIFIED.
- [T6] Telemetry/log truthfulness (task #6): 60s unfiltered `device logs` stream during HelloVLM inference: 46 JSON lines, 0 unparseable, services hellovlm_llm/hellovlm_app/wendy-agent, no gaps/disconnects. 3x `device top` samples stable/plausible (cpu 10-14%, mem 30.3/122.8 GiB, GPU 97-98% util at 64C/37.5W — HelloVLM runs continuous inference, so heavy GPU tests skipped per sharing rules). Device-side log growth bounded: journal 168M (capped), /var/log/messages tiny, chunk-staging 9.5M. PASS.
- [T7] E2E suite re-run (task #7): `make e2e-test-wendy DEVICE=wendyos-curious-meteor.local` from worktree swift/ — 807 tests in 108 suites, 18.5s, failed with 2 issues, BOTH inside the single known WDY-1798 test ("'--check-updates' reports update-source failure", WendyDeviceInfoTests.swift:412-413). Identical to the 2026-07-02 morning run → reproducible, no new failures. PASS (modulo known issue). Log: /Users/ai/wendy-thor-tests/e2e-rerun.log.
- [T8] Build-arg hint probe (extra Thor-divergent check): Dockerfile ARG/ENV probe app on port 3100. WENDY_DEVICE_TYPE=jetson-agx-thor, WENDY_JETPACK_MAJOR=7, WENDY_JETPACK_VERSION=7.2, WENDY_GPU_VENDOR=nvidia, WENDY_HAS_GPU=true all injected correctly; WENDY_CUDA_VERSION EMPTY → F3 (no /usr/local/cuda symlink, no version.{txt,json}, nvcc not on PATH on Thor image; nvidia-smi banner says CUDA 13.2).
- [T9] Cleanup: all autotest-* apps removed with --cleanup (images purged); re-cycled autotest-gpu once purely to delete its leftover image. Final state: apps=[sh.wendy.examples.hellovlm], volumes=[hellovlm-models, hellovlm-runs, thor-llm-kb-main-models, thor-llm-kb-main-openwebui] (all pre-existing). Device left exactly as found. VERIFIED.
