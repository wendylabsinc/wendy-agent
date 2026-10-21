# `wendy device pair`

Discover and manage paired devices in the `Bluetooth | SensorLink | IP Cameras | USB Arms | Unitree`
tabs. Use `Tab` or `Shift+Tab` to switch tabs. Opening a tab refreshes its devices.

```sh
wendy device pair
```

| Key | Action |
|-----|--------|
| `↑` / `↓` | Select a device |
| `Tab` / `Shift+Tab` | Switch tabs |
| `Enter` | Pair a device, or edit an IP camera's login |
| `f` | Forget the selected device |
| `r` | Refresh discovery |
| `l` | Log in to a Unitree account in the Unitree tab |
| `i` | Enter the selected Unitree robot's IP address |
| `q` / `Esc` | Quit |

Bluetooth scans on the connected WendyOS device. Unnamed peripherals are hidden
by default; `h` shows or hides them. `Enter` connects and pairs, and `d`
disconnects the selected peripheral.

SensorLink discovers sources on the connected WendyOS device's network: WendyOS
devices and Wendy Lite boards that advertise sensors. The device does not list
itself. The source must be reachable from that device, even when the CLI runs
elsewhere, for example over Wendy Cloud. Addresses in the list are the ones the
device sees. After pairing, the device finds the source by its identity, so a
source whose address changes is found again.

If the device's agent is too old to scan its own network, the tab scans the
CLI's local network instead and labels the list. The device can pair only with
sources it can reach, so update its agent to list those. Other discovery errors,
such as an unreachable device, are reported instead of falling back.

Sources must belong to the same organization as the target device, and you must
be logged into that organization. An mTLS Wendy Lite source with a valid
advertised PKI principal can pair without a numeric asset ID when the target
Agent supports principal pairing.
Update the target Agent if it reports that principal pairing is unsupported.
Sources without either a numeric asset ID or an authenticated PKI identity appear
under "Found sensors that need enrollment". Enroll those sources before pairing,
then press `r` to rescan. Saved pairings remain available to forget when their
sources are offline.

IP cameras are discovered on the connected device's network. `Enter` opens a
username and password form that saves the camera's login on the device. Passwords
are masked. Inside the form, `Tab` switches fields, `Enter` advances or saves, and
`Esc` returns to the camera list. Saved offline cameras can be forgotten with `f`,
which removes the camera and its stored login.

The Unitree tab discovers Go2 robots on the selected WendyOS device's LAN. It
browses mDNS advertisements for known account or saved robot serials and sends
Unitree's UDP LAN discovery request for stock Go2 robots. The robot must be
reachable from that WendyOS device, even when the CLI runs elsewhere.

```sh
wendy device pair --device my-wendy --unitree
wendy device pair --device my-wendy --ip 192.168.1.42
```

Press `l` to enter your Unitree account email and password. The CLI logs in to
Unitree and retrieves the encryption keys for the robots registered to that
account. The password is masked and is not saved. Select the matching robot by
serial number, then press `Enter` to connect. Press `i` to enter or change its IP
address. With `--ip`, select the account robot at that address and press `Enter`.
Wendy never picks a key from a different account robot automatically.

Use `--unitree-region cn` for an account registered in Unitree's China region.
The default is `global`. Inside the login form, `Tab` switches fields, `Enter`
advances or submits, and `Esc` cancels.

The selected WendyOS device stores the robot's key and connects to its local
WebRTC service. Pairing requires an authenticated WendyOS connection belonging
to the agent's pairing-owner tenant, or the device's private local agent socket.
The owner check applies even if the shared mTLS listener uses `grace` or `off`
tenant enforcement for other services. Newer Go2 firmware requires a key from
the account. Older firmware can use its legacy local connection protocol when
the account has no encryption key. Wendy selects the protocol supported by the
robot. LAN offers never include the Unitree account token. If the account
supplies a robot key, Wendy refuses weaker notifications and legacy-port
fallback rather than exposing credentials or trusting an unauthenticated peer.
Saved robots remain listed while offline. Select one and press `Enter` to
reconnect without logging in again, or `i` to change its IP. Press `f` to remove
the saved pairing and key. To refresh a saved robot's key, log in again and
connect the matching account robot.

The Unitree account token stays within the CLI's vendor-cloud exchange; it is
not returned to the pairing UI or sent to the agent. Legacy pairing uses a
fixed, non-secret account-flow marker instead, not proof of account ownership.

Robot keys are stored in a private, permission-restricted file on the agent;
they are not encrypted at rest. Account passwords and tokens are not persisted
or returned by pairing RPCs. Pairings are bound to the agent's enrolled tenant
(or local ownership before enrollment), not automatically transferred to a new
tenant. Unprovisioning closes sessions and removes the Unitree credential file
before it reports success; if removal fails, the agent retries when it restarts
unenrolled. Within-tenant certificate renewal preserves pairings when the current signed
certificate scope proves the relationship. The version-2 store keeps both
legacy-org and tenant-UUID identity during dual-SAN renewal; UUID wins over a
conflicting legacy org. Stored old identity never widens the current RPC scope.
Unscoped/version-1 stores from pre-release builds are rejected rather than
automatically adopted.

Legacy firmware's public protocol validation is not a cryptographic proof of
the robot's identity: use a trusted robot LAN.

Pairing sends validation and heartbeat messages, not movement commands. It
still occupies a robot WebRTC client slot and remains active after the CLI
exits. `connected` means the data channel passed protocol validation, not that
camera streaming, movement, or a physical safety check has been tested.

The agent reconnects saved robots after a restart, with at most four concurrent
connection setups and staggered retries, including after a connection drops. If the robot's DHCP address changes,
refresh discovery or enter its new IP. Before pairing, agree with the
robot's current operator who owns the connection. Do not interrupt an active
mobile-app or other robot session; if the robot is busy, arrange an explicit
handoff before retrying.

| Flag | Description |
|------|-------------|
| `--list` | Print current SensorLink pairings |
| `--name` | Set a friendly name for a SensorLink pairing |
| `--sensors` | Limit a SensorLink pairing to these sensor names |
| `--unitree` | Open the Unitree Go2 tab |
| `--ip` | Connect to a Unitree Go2 at this IPv4 address |
| `--unitree-region` | Unitree account region, `global` or `cn` |

Passing `--name` or `--sensors` opens the SensorLink tab first.
Passing any Unitree flag opens the Unitree tab first. `--list` cannot be combined
with Unitree flags.

The camera username starts with `admin` as an editable suggestion. Enter the
account configured on your camera and replace any factory-default password
before pairing it.

## USB robot arms (native Mac and Linux Agents)

Linux uses the Go Agent's `robotarm.DiscoverLinux` sysfs inventory. Native
macOS uses the separate Swift Agent implementation (`USBSerialInventory` and
`RobotArmService`) backed by IOKit. Running the Go Agent on macOS does not
provide USB arm discovery; select an updated native Mac Agent instead.

The **USB Arms** tab lists USB-backed serial endpoints attached to the selected
Agent's host, alongside saved arm bindings. A CLI on laptop A targeting laptop B
shows B's devices and saves records on B. It does not scan A's local serial ports.
Start directly on that tab with:

```sh
wendy device pair --device <target-laptop> --usb-arms
```

Select an endpoint to inspect its current path, VID/PID, USB serial number,
manufacturer/product, interface and USB location. `Enter` asks you to identify it
as an SO-101; `y` saves the binding and `n` or `Esc` cancels. `f` forgets a saved
binding, including when it is disconnected. `r` refreshes host metadata.

USB **pairing means saving host-local device configuration**, not Bluetooth
bonding. Currently `so101` is the supported binding model. Shared WCH adapter IDs
(`1a86:55d3`, `1a86:55d4`, `1a86:7523`) are adapter candidates only; they do not
prove that an SO-101 is attached. Other USB serial endpoints appear as **unknown
USB serial devices**. You may explicitly identify one as SO-101, but that is a
user configuration assertion, not hardware verification. Built-in consoles and
Bluetooth serial endpoints are excluded.

- **Candidate / unknown:** discovered USB serial metadata; no arm configured.
- **Configured / unverified:** the user saved an SO-101 binding; the arm has not
  been physically verified. `verified` remains false.
- **Present:** one matching USB endpoint exists. This does not establish motor
  power, serial access, port availability, joint telemetry or control readiness.
- **Disconnected:** a saved binding has no matching current endpoint. Its current
  path is empty and it remains available to forget.
  New binding is rejected; reconnect the device and refresh first.
- **Ambiguous:** several endpoints share the same identity. They are shown with
  their current paths, but `connected` is false and binding is blocked until the
  ambiguity is resolved. The Agent never chooses one automatically.

Serial-based identity includes VID/PID, adapter serial number and USB interface;
node renumbering and USB socket changes retain the binding. Different serial
numbers or interfaces remain distinct. Devices without a serial number use their
USB topology location and interface instead. This identity follows the **USB
port**, including a replacement adapter; it cannot establish physical-device
continuity. Interactive saving explicitly acknowledges this limitation. Scripts
must pass `--allow-location-binding`. Changing the hub/topology may require a new
binding. No machine-specific `/dev` path is hard-coded or trusted as identity.

For scripts, list the target's inventory and bindings as JSON, then use an exact
`id` from that response:

```sh
wendy --json device pair --device <target-laptop> --usb-arms --list
wendy --json device pair --device <target-laptop> --usb-arms --bind '<id>' --arm-name Follower
wendy --json device pair --device <target-laptop> --usb-arms --forget '<id>'
```

The additive USB JSON list is `{ "devices": [...] }`, including an empty array
when nothing is found. Bind returns `{ "device": {...} }`; forget returns
`{ "forgotten": "<id>" }`. Fields use snake_case and include explicit default
values, including `configured`, `verified`, `connected` and `access_status`.
Without `--usb-arms`, existing `--list`, `--name` and `--sensors` retain their
SensorLink meaning. USB operations cannot be combined with SensorLink flags.
Older Agents return an explicit update/unsupported message; there is no fallback
to discovery on the CLI's machine.
`--arm-name` accepts at most 128 UTF-8 bytes. Binding a saved arm again without
`--arm-name` keeps its name; a new binding defaults to `SO-101`. Binding requires a currently
connected, unambiguous endpoint; metadata discovery alone cannot authorize it.

The Linux Agent stores bindings in `/var/lib/wendy/robot-arm-bindings.json`.
The Mac Agent stores them in `robot-arm-bindings.json` inside its existing
Application Support state directory (the running app's bundle identifier, or
process name when unbundled). Records survive Agent restarts. Forgetting deletes
only the saved configuration; it does not disconnect a USB device or change motors.
If the binding file is corrupt, list, bind and forget fail without overwriting
it. Inspect and back up the file on that Agent's host before repairing its JSON
or deliberately resetting saved configuration. Disconnecting the USB device
does not repair a corrupt file.

Discovery, save and forget only read OS metadata or update that JSON file. They
never open a serial port, send torque writes, home/calibrate motors, access a
camera or enable physical control. `access_status` is `unknown`: busy, permission
and unpowered-servo conditions cannot be diagnosed from metadata alone. Use `r`
after reconnecting hardware; a disappearing device cannot be newly bound.

Pairing does **not** install or start an SO-101 hardware adapter and does not
produce live joint telemetry. Observation, calibration verification and control
remain separate, explicitly managed work. A future adapter can resolve a saved
binding's identity through the Agent's current inventory; it must recheck
connection/ambiguity and manage exclusive access before using the serial port.
Calibration import/reference selection is not implemented by this increment.
