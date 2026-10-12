# Wendy

Build and run apps on robots, Raspberry Pis, Jetsons, and other Linux devices.
Wendy builds your code, deploys it, and brings the logs back to your terminal.
Start with a simulated robot on your own computer, then connect hardware when
it is ready.

[Documentation](https://docs.wendy.dev) · [Discord](https://discord.gg/wendylabsinc) · [Examples](Examples)

## Start with a moving robot

The guided tour creates a simulated Unitree Go2, opens its 3D view, and runs a
short walking demonstration. You can try it without a physical robot or a
Wendy Cloud account.

You need macOS or Linux, a running Docker installation with Buildx, and internet
access for the initial downloads and build. The robot VM uses four virtual CPUs
and 4 GiB of memory; allow disk space for the OS image and containers. Apple
Silicon is the fastest path. x86 computers emulate the ARM64 virtual device and
can take longer. The first
run downloads an OS image and builds the robot software, so allow several minutes.
The tour checks Docker and QEMU before starting. See the prerequisite details
below if either is missing. Python runs inside the container; you do not need
to install it on your computer.

### 1. Install the CLI

On macOS or Linux:

```sh
curl -fsSL https://install.wendy.dev/cli.sh | bash
wendy --version
```

The CLI runs on the computer where you write code. It does not replace that
computer's operating system.

On Windows, install with `winget install WendyLabs.Wendy --source winget`.
The managed robot VM tour requires macOS or Linux. Windows users can follow the
[physical-device guides](https://docs.wendy.dev/latest/#2-set-up-a-device) or try
the [local Docker robot demo](https://wendy.dev/quickstart?target=simulator).

[Other installation methods](INSTALL.md) include Homebrew, .deb, .rpm, and AUR.

Already installed? Run `wendy update --check` to check your version and see the
right update method. Run `wendy update` to apply a supported update. Restart your
AI app or Wendy MCP server afterward so it uses the new CLI.

### 2. Take the tour

```sh
wendy tour
```

Choose the recommended simulated robot path. The tour checks the environment,
creates the virtual device, opens its browser viewer, and starts a five-second walk.
Look for the Go2 moving in the scene. The tour checks that its position changed
and stops the movement before reporting success.

The tour saves the movement settings in `~/Documents/wendy-robot-tour/tour.json`.
Change `turn_rps` to `0` to walk straight instead of turning, then run it again:

```sh
wendy tour --simulator
```

This reuses the same virtual device. The VM and viewer stay available after the
check. Use the exact `wendy vm stop` command printed in your terminal when you
are finished. To keep another tour project, use `wendy tour --simulator --project DIR`.

If setup fails, follow the specific prerequisite or connection instruction in
the terminal and rerun the tour. See [first-deployment troubleshooting](https://docs.wendy.dev/latest/errors/)
for builder, network, or readiness errors.

<details>
<summary>Install or check the simulator prerequisites</summary>

Start Docker, then check:

```sh
docker info
docker buildx version
```

Both commands should succeed. The virtual device also needs QEMU and its ARM64
firmware. On macOS, Wendy offers to install QEMU through Homebrew when needed.
On Debian or Ubuntu, install it before the tour:

```sh
sudo apt install qemu-system-arm qemu-efi-aarch64
```

See [virtual-device setup](https://docs.wendy.dev/latest/installation/wendyos-virtual-machine/)
for other setup details.

</details>

## Use your own device

Wendy has two parts: the CLI on your development computer, and the Agent on the
target running your apps. WendyOS includes the Agent. Existing Linux installations
can keep their OS and install the Agent separately.

| Your target | Setup |
| --- | --- |
| NVIDIA Jetson Orin Nano or AGX Orin developer kit | [Full USB recovery](https://docs.wendy.dev/latest/installation/wendyos-nvidia-jetson-orin-nano/) updates boot firmware and OS together. |
| NVIDIA Jetson AGX Thor | [USB recovery guide](https://docs.wendy.dev/latest/installation/wendyos-nvidia-jetson-agx-thor/) |
| Raspberry Pi 3, 4, or 5 | [Write a microSD card and connect the Pi](https://docs.wendy.dev/latest/installation/wendyos-raspberry-pi-5/) |
| Existing Linux computer or robot | [Install Wendy Agent](https://docs.wendy.dev/latest/installation/linux/) and keep your OS. |
| Unitree G1 | [Install the Agent on the development computer](https://docs.wendy.dev/latest/installation/wendy-agent-unitree-g1/) and keep the vendor OS. |
| Apple Silicon Mac | [Native Mac target](https://docs.wendy.dev/latest/installation/wendy-agent-macos/), currently beta. |
| ESP32 | [Wendy Lite setup](https://docs.wendy.dev/latest/installation/wendy-lite-esp32/), currently beta. |

The board guides list storage, power, and cable requirements before flashing.
Flashing erases the selected target storage. Confirm the intended device boots
and appears in `wendy discover` before deploying your first app.

For guided device setup, run `wendy tour --hardware`. Once connected, create a small HTTP app:

```sh
wendy init hello-wendy --target wendyos --language node --template simple-api --assistant skip --git-init no
cd hello-wendy
wendy run
```

Select your device. Open the address Wendy prints and check for
`{"message":"hello-world"}`. Edit `src/index.ts`, save, then run `wendy run` again
to rebuild and deploy your change. Node.js runs inside the container.

## Build with a coding agent

After your first successful run, you can let a coding agent deploy changes and
inspect the device through Wendy's MCP server:

```sh
wendy mcp setup
```

Follow the [coding-agent setup guide](https://docs.wendy.dev/latest/guides/llm-integration/)
for supported tools. For ChatGPT Desktop, see the [ChatGPT guide](docs/guides/chatgpt.mdx).

## Keep building

- [Tutorials](https://docs.wendy.dev/latest/guides/tutorials/) cover Python, Swift, Rust, C++, TypeScript, and more.
- [Hardware access](https://docs.wendy.dev/latest/hardware/) covers cameras, audio, Bluetooth, and GPUs.
- [Examples](Examples) include robot control, local AI, and web apps.
- [Wendy Cloud](https://docs.wendy.dev/latest/cloud/) adds remote access and fleet management when you need it. Local development does not require a cloud account.
