# Roadmap Status

This page mirrors the current public roadmap language for engineering and docs references.

## In Progress

- **MLX Swift support**: MLX Swift integration for unified-memory AI workloads. The containerised MLX LLM demo has landed (see Shipped); the remaining work is first-class MLX Swift integration in the Wendy ecosystem.
- **Companion SDK** — _not yet shipped_ ([WDY-1235](https://linear.app/wendylabsinc/issue/WDY-1235)): the companion SDK and its associated applications are demoable today but still need polish for an official release. The original 2026-07-03 target and its sequencing behind the Pipecat Agent template refresh have both lapsed — the Pipecat refresh ([WDY-1228](https://linear.app/wendylabsinc/issue/WDY-1228)) shipped, so the launch is no longer gated on it and needs a new date.

## Available Beta

- **Wendy Cloud** — _beta_: fleet management over the internet. Devices enroll once, dial home to the cloud broker over mutual TLS, and are reachable by name from anywhere via `wendy cloud` and `wendy fleet` — enroll, discover, deploy, tunnel, stream telemetry. See [Cloud](/docs/cloud).
- **Headless Mac** — _beta_: deploy native macOS apps to Apple Silicon Mac targets with [Headless Mac](/docs/installation/wendy-agent-macos). Apple Silicon only. Since the initial beta it has gained remote agent self-update, a LAN registry served over mTLS, app supervision and restart parity across agent restarts, and native gamepad entitlements. Beta limitations are documented in the macOS install guide.

## Available Preview

- **Jetson AGX Thor** — _preview_: WendyOS support for AGX Thor-class physical AI workloads, including the dedicated USB-recovery flash path that writes QSPI and internal NVMe directly. Install, deploy, GPU, and camera all work; see the [AGX Thor install guide](/docs/installation/wendyos-nvidia-jetson-agx-thor).
- **Wendy Lite** — _preview_: ESP32 runtime and deployment layer for C5, C6, C61, P4, and S3 targets. Regular native ESP-IDF projects are the recommended app model and deploy with `wendy run`; Swift and other WASM guests remain available as an optional portable runtime. See [ESP32 installation](/docs/installation/wendy-lite-esp32) and the [Wendy Lite reference](/docs/advanced/wendy-lite).

## Shipped

- **Jetson AGX Orin support**: stable — install, deploy, GPU, and camera.
- **Jetson Orin Nano support**: stable.
- **Raspberry Pi 3, 4, and 5 support** (8GB Pi 5 recommended).
- **Windows support** for the Wendy CLI and VS Code deployment flow.
- **Zero-trust PKI, deployed to GCP** ([WDY-1226](https://linear.app/wendylabsinc/issue/WDY-1226)): the public-key infrastructure backing device-to-cloud auth is deployed, with CI auto-deploy from main. Every cloud connection is mutually authenticated. See [PKI](/docs/advanced/pki).
- **Stagefile build descriptors**: `build.stagefile.yaml` is now a first-class build input alongside Dockerfiles and compose files, with parallel compilation, CUDA stages resolved from the device's GPU architecture, pinned source installs, and cache scoping. The bundled example apps have been converted to it.
- **ROS 2 support**: standard upstream ROS 2 in stock containers with a selectable middleware (CycloneDDS, Fast DDS, RTI Connext, GurumDDS), plus `wendy device ros2` for remote nodes, topics, services, parameters, actions, lifecycle nodes, and rosbags. See [Robotics](/docs/integrations) and the [Foxglove integration](/docs/integrations/foxglove).
- **Swift on the device**: Swift app templates, tutorials, cross-compilation, and remote Swift debugging, plus native Swift apps on macOS targets.
- **Unitree G1 support**: agent install and reflash guides for the G1.
- **Network (IP) cameras**: a third camera transport alongside USB and CSI, with camera discovery, a credential store, and a picker.
- **Python debugging** from the VS Code extension.
- **Local OS updates** without requiring a full system flash.
- **MLX LLM in a local container** ([WDY-1229](https://linear.app/wendylabsinc/issue/WDY-1229)): a working containerised MLX LLM demo, backed by an upstream MLX segfault fix. The demo container is not size-optimised — it ships Ubuntu 24, the CUDA toolkit, and the Swift toolchain, because MLX compiles CUDA just-in-time.

## Planned

- **RISC-V support**.
- **3D geospatial digital twin** targeted for Q3.
- **Logging and telemetry demo** — _planned, not yet started_ ([WDY-1231](https://linear.app/wendylabsinc/issue/WDY-1231)): a demo showcasing remote hardware debugging through logging and telemetry. It was originally sequenced against the PKI release, which has since shipped; the demo is still in the backlog.
- **Wendy Swift initiative** — _separate release track_: the Swift toolchain, templates, and debugging support have shipped (see Shipped). The remaining work is the coordinated launch, which is deliberately kept on its own track so it is not diluted by adjacent releases. The original sequencing behind the Pipecat Agent template refresh no longer applies — that work shipped.
