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
WebRTC service. Pairing requires an authenticated WendyOS connection or the
device's local agent socket. Newer Go2 firmware requires an encryption key from
the account. Older firmware can use its legacy local connection protocol when
the account has no encryption key. Wendy selects the protocol supported by the
robot and uses the account token for the initial connection.
Saved robots remain listed while offline. Select one and press `Enter` to
reconnect without logging in again, or `i` to change its IP. Press `f` to remove
the saved pairing and key. To refresh a saved robot's key, log in again and
connect the matching account robot.

The agent reconnects saved robots after a restart. If the robot's DHCP address
changes, refresh discovery or enter its new IP. If a connection fails while the
Unitree mobile app is connected, close the app and retry.

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
