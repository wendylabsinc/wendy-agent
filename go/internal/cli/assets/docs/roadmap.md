# Roadmap Status

This page mirrors the current public roadmap language for engineering and docs references.

## Imminent

- **Jetson AGX Orin support**: WendyOS support for production robotics, vision, and physical AI workloads is imminent.
- **Cloud fleet management**: fleet deploy, manage, and debug workflows are being worked on and are imminent.
- **PKI / cloud release** — _planned, target **2026-06-19**, not yet shipped_ ([WDY-1226](https://linear.app/wendylabsinc/issue/WDY-1226)): public-key infrastructure backing device-to-cloud auth is tested locally; remaining work is the GCP deployment, supporting marketing assets, and cloud documentation. The logging/telemetry demo ([WDY-1231](https://linear.app/wendylabsinc/issue/WDY-1231)) is sequenced against the same date.

## In Progress

- **Wendy for Mac**: headless Mac mini and Mac Studio support using a macOS-specific `wendy-agent` workflow.
- **Jetson AGX Thor support**: WendyOS support for AGX Thor-class physical AI workloads.
- **MLX Swift support**: MLX Swift integration for unified-memory AI workloads.
- **Companion SDK** — _planned release **2026-07-03**, not yet shipped_ ([WDY-1235](https://linear.app/wendylabsinc/issue/WDY-1235)): the companion SDK and its associated applications are demoable today but still need polish for an official release. The launch is sequenced one week after the Pipecat Agent template refresh ([WDY-1228](https://linear.app/wendylabsinc/issue/WDY-1228)) to avoid overlapping marketing pushes.
- **MLX LLM in a local container** — _planned, not yet shipped_ ([WDY-1229](https://linear.app/wendylabsinc/issue/WDY-1229)): MLX currently compiles as a Swift package; remaining work is Wendy-ecosystem integration plus Dockerfiles for Linux compilation, building toward a containerised demo of MLX LLM.

## Shipped

- **Windows support** for the Wendy CLI and VS Code deployment flow.
- **Jetson Orin Nano support**.
- **Raspberry Pi 3, 4, and 5 support**. Continuous-integration coverage for the Raspberry Pi 3 and Pi 4 boards is currently being re-instrumented — physical hardware needs to be sourced and brought back online before automated regression coverage resumes. See [WDY-1230](https://linear.app/wendylabsinc/issue/WDY-1230) for the CI re-onboarding work.
- **Python debugging** from the VS Code extension.
- **Local OS updates** without requiring a full system flash.

## Planned

- **RISC-V support**.
- **3D geospatial digital twin** targeted for Q3.
- **ROS 2 support** — _planned, not yet shipped_: officially designated as a development priority. Many prospective users and partners expect first-class Robot Operating System 2 (ROS 2) compatibility; scope and timeline are still being defined.
- **Wendy Swift initiative** — _planned, separate release track, not yet shipped_: Swift support is being developed as a separate initiative from the primary release cycle so that its launch is not diluted by adjacent releases. Target window is approximately one week after the Pipecat Agent template refresh ([WDY-1228](https://linear.app/wendylabsinc/issue/WDY-1228)).
