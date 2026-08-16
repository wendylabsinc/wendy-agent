# WendyOS

WendyOS is infrastructure for shipping AI applications to robots, drones, and
edge computers with an app-like workflow. A developer declares the app,
hardware access, persistence, ports, dependencies, and readiness in source,
then runs `wendy run` to build, transfer, replace, start, and observe it on a
selected device.

WendyOS is the prebuilt operating-system layer for supported edge hardware.
`wendy-agent` is the device daemon for app lifecycle, hardware entitlements,
networking, logs, and remote APIs; it can also be installed on standard Linux,
so a Wendy target is not automatically booted from a WendyOS image. Wendy
clients and cloud handle discovery, enrolled identity, delivery, logs,
telemetry, and fleets.

The stable app ID and version live in `wendy.json`. Apps receive only declared
hardware entitlements such as GPU, audio, camera, network, USB, and persistent
storage. Named volumes keep large models and data across app replacement.
Readiness checks distinguish a container that exists from a service that is
actually ready. WendyOS also supports signed A/B operating-system updates with
health checking and rollback while durable identity and configuration remain
outside the replaced root filesystem.

At ModCon, Wendy lets the team iterate individual G1, Spark, and Go2 services
without rebuilding every model or manually reproducing shell commands on each
robot.
