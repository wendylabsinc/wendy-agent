# WendyMC

A Wendy app that runs a Minecraft (Paper) server in a container alongside a
small web admin UI. Built to run on a Jetson under WendyOS, but it works
anywhere `wendy run` does.

## Quick start

From this directory, on a machine with the `wendy` CLI installed and a
WendyOS device reachable:

```sh
cd Examples/WendyMC
wendy run
```

That builds the two-service compose project, ships it to the device, and
starts both containers. First boot pulls `itzg/minecraft-server` (~600 MB)
and generates a world (60–120 s).

Once you see `Done (Xs)! For help, type "help"` in the stream, the world is
up. `wendy run` also opens the admin UI in your browser automatically as
soon as it's reachable:

```
http://<jetson-ip>:8080
```

If it doesn't open on its own (e.g. no display on this machine), visit that
URL manually. See [Why the browser opens](#why-the-browser-opens) below.

Connect a Minecraft Java client to `<jetson-ip>:25565`.

## Why the browser opens

The companion `wendy.json` (which sits right next to `docker-compose.yml` in
this directory, so `wendy run` picks it up automatically) declares a
top-level `readiness` (a TCP probe against port 8080, with a 180 s timeout to
cover first-boot world generation) and `hooks.postStart.openURL` pointing at
`http://${WENDY_HOSTNAME}:8080`. Since this is a two-service compose
project, that top-level pair is an app-level fallback: it fires once after
both `minecraft` and `webui` have started, rather than being tied to either
service individually — which is why `wendy run` waits for the web UI and
then opens it for you.

To scope a readiness probe or hook to one service instead, declare it under
`services.webui` in `wendy.json` or `x-wendy` on the `webui` service in
`docker-compose.yml` — see [Readiness probes and postStart hooks](../../docs/apps/compose.md#readiness-probes-and-poststart-hooks).

## What you get

- **Live console** — xterm.js terminal in the browser, streaming the server log
  over a WebSocket. The command box sends RCON commands (`say hello`, `op
  alice`, `list`, …).
- **Player list & metadata** — sidebar with version, MOTD, latency, and
  currently-online player names. Refreshed every 5 s via Server List Ping.
- **Restart** — single button. Sends `stop` over RCON; the container's
  `restart: always` policy brings it back. Expect ~30–60 s of downtime.

## Tuning the server

Every Minecraft setting is an env var on the `minecraft` service in
`docker-compose.yml`. The interesting ones:

| Var            | Default               | Notes                                   |
|----------------|-----------------------|-----------------------------------------|
| `TYPE`         | `PAPER`               | `VANILLA`, `FABRIC`, `FORGE`, etc.      |
| `VERSION`      | `LATEST`              | Pin to e.g. `1.21.1`                    |
| `MEMORY`       | `4G`                  | JVM heap. Tune to the Jetson model.     |
| `MAX_PLAYERS`  | `10`                  |                                         |
| `MOTD`         | `WendyMC — …`         |                                         |
| `ONLINE_MODE`  | `TRUE`                | `FALSE` for cracked / LAN-only          |
| `RCON_PASSWORD`| `wendymc-local`       | **Change for untrusted networks.**      |

See the [itzg/minecraft-server docs](https://docker-minecraft-server.readthedocs.io/)
for the full list (whitelist, ops, difficulty, mods, plugins, …).

The web UI picks up `RCON_*`, `MC_HOST`, `MC_PORT`, `LOG_PATH`, and
`WEBUI_PORT` from its own service block — keep them in sync if you change
`RCON_PASSWORD` or the server port.

## Persistence

World data lives in the named volume `mc-data`. It survives `wendy run`
re-deploys; it does not survive `wendy device` wipes or removing the
underlying containerd volume.

## What it doesn't do (v1)

- Real **Start/Stop**. The web UI runs inside a container and can't
  lifecycle its sibling container — there's no docker socket on WendyOS
  (it uses containerd), and the WendyAgent gRPC isn't exposed to
  in-container clients. The bring-up entry point is `wendy run`.
- **Auth** on the web UI. Assumes a trusted LAN. Don't expose port 8080
  to the open internet without putting a reverse proxy with auth in front.
- Settings editor / plugin manager / backup tooling.

## EULA

Setting `EULA: "TRUE"` in `docker-compose.yml` indicates acceptance of the
[Minecraft EULA](https://aka.ms/MinecraftEULA). By running this app you
accept it on behalf of your deployment.
