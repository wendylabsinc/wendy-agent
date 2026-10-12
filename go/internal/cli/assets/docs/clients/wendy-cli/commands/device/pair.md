# `wendy device pair`

Discover and manage paired devices in the `Bluetooth | SensorLink | IP Cameras | Unitree`
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

SensorLink discovers sources on the CLI's local network. Devices must belong to
an organization you are logged into. Saved pairings remain available to forget
when their sources are offline.

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
tenant. Unprovisioning closes sessions and removes the Unitree credential file;
within-tenant certificate renewal preserves pairings when the current signed
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
connection setups and staggered retries. If the robot's DHCP address changes,
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
