# Wendy common knowledge

Snapshot: 2026-08-15. This is bundled background knowledge, not a live copy of
the website. For release-specific details, direct users to https://docs.wendy.dev.

## What Wendy is

- Wendy is the full-stack Physical AI platform for deploying software to
  robots, drones, cameras, sensors, and other edge devices.
- WendyOS is its Apache 2.0 open-source Linux operating system and toolchain.
  It is designed for local development, deployment, debugging, observation,
  secure hardware access, and fleet-safe updates.
- Apps execute locally on the target, so they can keep working when internet
  access is unreliable or unavailable. Development and deployment can use
  USB-C, a local network, or cloud workflows.
- Wendy uses containers underneath on Linux targets, but adds device
  discovery, app lifecycle management, hardware permissions, device identity,
  incremental transfers, remote tooling, and signed atomic OS updates with
  automatic rollback.
- Wendy complements robotics frameworks such as ROS 2; it supplies the OS,
  deployment, security, update, and device-management layer around them.

## Platforms and developer experience

- Wendy supports NVIDIA Jetson-class devices, including Orin and AGX Thor,
  Raspberry Pi 3/4/5, and compatible Linux machines through `wendy-agent`.
  Wendy for macOS is a beta workflow for native apps on Apple Silicon targets.
- Developers can work from macOS, Windows, and common Linux distributions.
  Apps may use Swift, Python, C++, Rust, TypeScript/Node, Mojo, or a custom
  Dockerfile. Wendy frameworks focus on AI inference, hardware I/O, and
  high-speed telemetry; ordinary frameworks remain usable.
- The CLI and editor tooling support workflows such as device discovery,
  deployment, streamed logs, debugging, hot reloading, and one-click runs.
- Wendy Cloud is the fleet layer for OTA updates, crash reporting, remote
  telemetry, monitoring, and deployment at scale.

## Common workflow

- Install the CLI on macOS or Linux with
  `curl -fsSL https://install.wendy.dev/cli.sh | bash`; on Windows use
  `winget install WendyLabs.Wendy --source winget`.
- Existing compatible Linux targets can install the agent with
  `curl -fsSL https://install.wendy.dev/agent.sh | bash`. WendyOS images
  already include the agent.
- `wendy install` installs WendyOS onto supported media or installs Wendy Lite
  firmware on supported microcontrollers.
- `wendy discover` finds Wendy devices over USB Ethernet and the LAN via mDNS.
- `wendy init` scaffolds a project. `wendy run` selects a target when needed,
  builds the app on the developer machine, transfers changed container layers,
  starts it on the device, and normally streams its output.
- `wendy device apps list|start|stop|remove` manages deployed applications.
  `wendy device logs --app <name>` inspects application logs.

## Applications and permissions

- A typical project contains source code, a Dockerfile or supported native
  project, and `wendy.json`. The manifest describes the app ID, target
  platform, version, runtime configuration, readiness behavior, and requested
  entitlements. `wendy json validate` validates it; `wendy json schema` prints
  its schema.
- WendyOS apps are isolated with minimal access by default. Entitlements grant
  only the hardware and capabilities an app needs. Common types include
  `network`, `http`, `gpu`, `camera`, `audio`, `bluetooth`, `display`, and
  `persist`. Persistent storage declares both a volume name and mount path.
- A networked GPU service commonly requests host networking plus `gpu`; a web
  service may also declare an `http` port; a voice app requests `audio`; and a
  vision app requests `camera`. Avoid suggesting `--privileged` when an
  entitlement provides the required access.
- `wendy.json` can define multiple services and dependencies. `wendy run`
  builds those services in parallel, creates them in dependency order, starts
  them, and multiplexes their logs. Docker Compose projects are also supported
  on Linux/WendyOS targets.

## Product principles

- Security is capability-based: network, GPU, camera, audio, storage, and
  similar resources are opt-in rather than ambient container privileges.
- Devices have cryptographic identities, communication is protected, and OS
  releases use signed A/B updates that can roll back after a failed boot.
- Builds run on the development machine and Wendy transfers only changed
  layers when possible, avoiding a registry round trip for the normal local
  loop.
- The canonical product site is https://wendy.dev, documentation is at
  https://docs.wendy.dev, and source is linked from the Wendy Labs GitHub
  organization. Do not invent flags, compatibility, pricing, or release status;
  recommend the current docs for details not covered by this snapshot.
