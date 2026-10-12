# `wendy tour`

Watch a simulated Go2 walk in your browser, or set up your first physical Wendy device.

## Choose your path

```sh
wendy tour
```

In an interactive terminal, choose **Walk a simulated robot** for the recommended
first experience. Choose **Set up a physical device** when you have hardware ready.

You can select either route directly:

```sh
wendy tour --simulator
wendy tour --hardware
```

## Simulated robot

The managed Go2 virtual device requires macOS or Linux, QEMU with ARM64 firmware,
and a running Docker builder with Buildx. It uses four virtual CPUs and 4 GiB
of memory. Apple Silicon uses hardware acceleration; x86 hosts use slower
emulation. The first run downloads the OS and robot assets and builds the runtime.
Allow several minutes and disk space for the images. No physical robot, cloud
account, or host Python installation is required.

See the [quickstart](/docs/#try-the-robot-tour) for prerequisite checks and
installation commands. Windows users can use the physical-device route or the
separate [local Docker robot demo](https://wendy.dev/quickstart?target=simulator).

The tour creates a dedicated Go2 VM and opens its browser viewer. The robot walks
a short arc. Success requires a measured change in its position and confirmation
that the movement stopped. The VM and browser viewer remain available afterward.
The terminal prints the viewer URL and the exact `wendy vm stop` command for that VM.

### Change the movement and repeat

The tour stores its settings in `~/Documents/wendy-robot-tour/tour.json`. Change
`turn_rps` to `0` for a straight walk, then rerun:

```sh
wendy tour --simulator
```

This reuses the saved VM. You can edit `duration_seconds` within the supported
range of 1 to 8 seconds, or `turn_rps` between -1 and 1. The starter's
`forward_mps` is bounded between 0.55 and 0.6 metres per second. These settings
control the simulated robot only.

To keep a separate starter and VM:

```sh
wendy tour --simulator --project ./my-robot-tour
```

If setup fails, fix the reported prerequisite or connection error and repeat the
same command to resume. If another controller owns the simulated robot, release
its controls in the viewer before retrying. Ctrl+C interrupts the demonstration;
use the printed VM stop command when you also want to shut down its virtual device.

## Physical device

`wendy tour --hardware` guides you through device setup, discovery, optional
Wi-Fi configuration, and a sample app. Use an interactive terminal. Check the
[board-specific setup guide](/docs/#2-set-up-a-device) for storage, power,
recovery mode, and cable requirements. Review the selected storage before
confirming a flash, which erases that storage.

At the Wi-Fi step, the wizard tries to prefill the host computer's current SSID.
If it cannot detect one, enter the network name yourself. Cloud enrollment and
coding-agent setup are optional. The physical-device route does not command
robot motion.
