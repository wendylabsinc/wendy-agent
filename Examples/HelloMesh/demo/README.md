# WendyOS mesh demo

A visual walkthrough of the mesh data path: a container calls another device by
name (`device-<id>.cloud.wendy.dev:8080`), the name resolves to a VIP, the
request routes device-to-device (LAN-direct, or cloud-relay fallback), and a
`200` comes back.

## Two ways to view it

**Static** — `mesh-demo.html`. Open it directly (or share the hosted artifact).
Fully self-contained; the "from this lab" devices are hardcoded.

**Live** — `index.html` + `server.py`. The top strip shows your **real enrolled
fleet**, polled from `wendy cloud discover` and refreshed every few seconds:

```bash
cd Examples/HelloMesh/demo
python3 server.py                 # → http://localhost:8787
# point it at a specific cloud / CLI if needed:
WENDY_CLOUD_GRPC=127.0.0.1:50051 WENDY_BIN=wendy python3 server.py
```

Python 3 stdlib only, no dependencies.

## What's real vs. illustrative

- **Real, live:** the fleet strip and the "enrolled devices" grid — actual
  device names and their computed VIPs (`10.99.<id÷256>.<id mod 256>`).
- **Illustrative:** the animated traffic stage (packet flow, request counter).
  It depicts the real routing but is not driven by live traffic.

To exercise the real round-trip end-to-end, deploy `HelloHTTP` on one device and
`HelloMesh` on another (`MESH_TARGET=device-<id>.cloud.wendy.dev:8080`) and watch
`wendy device logs … --app hellomesh` for `OK 200`.
