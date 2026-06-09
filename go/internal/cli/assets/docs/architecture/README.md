Wendy Labs is the infrastructure for Physical AI, targetting Edge (AI) devices such as robotics, IoT and sattelites.

### Problem

Before Wendy, it's very hard to install, develop and deploy these devices.

- Setting up the OS, drivers and tooling is manual, and requires peripherals such as a keyboard, mouse and monitor
- Running on a device is a custom effort, whether through rsync, zip files, or containers
- Scaling your solution to many devices is not standardised either

In cloud services, all of these are solved through pre-baked images and container infrastructure. Wendy brings that to the edge.

### Solution

Wendy Labs has three main components to achieve this mission:
- WendyOS
- Wendy-Agent
- Cloud

[WendyOS](../wendyos/) is a pre-built OS for specific hardware (NVIDIA Jeton, Raspberry Pi, ...) that packages all the tools you need to build & run apps.

[Wendy-Agent](../wendy-agent/) is a daemon, akin to Docker, that manages and runs your apps. It also exposes a way to remotely control the machine and upload apps.

[Wendy Cloud](../cloud/) is a tool for managing your Wendy devices, viewing (crash) logs, provisioning updates and more.

### Agent & CLI

The Agent comes in two flavours, [Linux](../wendy-agent/linux/) and [macOS](../wendy-agent/macos/) for running apps on these platforms respectively.

The [Wendy Clients](../clients/) enable your developers to manage these devices, locally through LAN, BLE or over USB.

> **TODO**: We want the Clients to also be able to talk to the cloud, and manage your Wendy devices through that.