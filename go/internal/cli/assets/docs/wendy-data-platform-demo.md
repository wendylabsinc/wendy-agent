# Wendy Data Platform demo runbook (Hubert)

How to reproduce the end-to-end Wendy Data Platform demo on a real device from
a clean start: a Jetson Orin Nano ("Hubert", `wendyos-hubert.local`) running
the `feature/wendy-data-platform` agent, the D5 reference model app
(`Examples/WendyDataModelApp`), a flight-recorder campaign, and episode
uploads into the deployed dev cloud ingest service (`wendy-data-dev` on
Cloud Run, project `cloud-c7e56`).

Everything below was executed and verified on 2026-08-23. Episode
`20260823T094944Z-8b61e446d12c495f` fired organically on
`model.uncertainty > 0.65`, carried 97 yolov8n prediction records in
`events.jsonl` (including the pre-trigger buffer), and reached the dev ingest
catalog (upload state `uploaded`, which only flips after `CommitEpisode`
verifies every file hash server-side).

## Device facts (Hubert)

| Fact | Value |
|---|---|
| Hostname / IP | `wendyos-hubert.local` / 10.60.10.90 (wlan0) |
| Hardware | Jetson Orin Nano, 6 CPU, 8 GB RAM, GPU sm_87, CUDA 13.2, JetPack 7.2 |
| OS | WendyOS 0.16.0 (blacksail), slot A, tegrauefi |
| Enrollment | Production cloud, org 2, asset 172 (UNTOUCHED by this demo) |
| Camera | Logitech C920 PRO HD on `/dev/video0` (`/dev/video1` is its metadata node) |
| Agent before demo | 2026.08.07-174446 |
| Agent during demo | `dev-data-platform-demo-ca322e8` (override + cgroupfs attribution fix) |
| Disk | root 7.3 GiB (tight, see below); `/data` 440 GiB |
| SSH | `root@10.60.10.90` (key auth; the `wendy` user refuses keys) |

## 0. Prerequisites on the workstation

- Go toolchain; cgo builds need `CC=/usr/bin/clang` on this Mac (the default
  `clang` resolves to a swiftly shim that breaks cgo with a silent
  "exit status 2").
- Docker Desktop for the app image build. If a pull hangs, the desktop
  credsStore is the cause: point `DOCKER_CONFIG` at a scratch directory
  containing a `config.json` of `{}`.
- `gcloud` for the ingest URL; on this Mac it needs
  `CLOUDSDK_PYTHON=/opt/homebrew/bin/python3` (system Python 3.9 crashes it).

## 1. Recon (read-only)

```sh
wendy device list --timeout 8s          # no --scan flag; --timeout scans once
wendy device info --device wendyos-hubert.local
ssh root@10.60.10.90 'df -h / /data; ls /dev/video*'
```

Check `df` before anything else. Hubert's root filesystem had been filled to
100 percent by 64 daily agent-updater backups in `/opt/wendy/bin`
(`wendy-agent.backup.*`, about 1.3 GiB); the nightly updater had been failing
since Aug 12 (zero-byte backups) and the Aug 23 run left
`/usr/local/bin/wendy-agent` truncated and the agent dead. Recovery, if this
recurs:

```sh
ssh root@10.60.10.90 '
  rm -f /opt/wendy/bin/wendy-agent.backup.*
  cp /opt/wendy/bin/wendy-agent.latest /usr/local/bin/wendy-agent
  systemctl restart wendyos-agent'
```

## 2. Build the integration agent and CLI

```sh
git -C ~/Documents/Projects/WendyOS fetch origin feature/wendy-data-platform
git -C ~/Documents/Projects/WendyOS worktree add \
    ~/Documents/Projects/WendyOS/.worktrees/demo-hubert origin/feature/wendy-data-platform
cd ~/Documents/Projects/WendyOS/.worktrees/demo-hubert/go

# Agent: pure Go, cross-compiles from macOS directly (CGO_ENABLED=0).
make build-agent-linux-arm64 VERSION=dev-data-platform-demo

# CLI: needs cgo (gousb, BLE); the darwin Makefile target's CGO_ENABLED=0
# does not link, so build it directly:
CC=/usr/bin/clang CGO_ENABLED=1 go build \
    -ldflags "-s -w -X github.com/wendylabsinc/wendy/go/internal/shared/version.Version=dev-data-platform" \
    -o bin/wendy-demo ./cmd/wendy
./bin/wendy-demo data --help   # the data verbs exist only on this branch
```

## 3. Install the agent on Hubert

Keep a rollback first (done; lives on the device):

```sh
ssh root@10.60.10.90 'ls /data/demo-hubert-rollback'
# wendy-agent.2026.08.07.sha-5fd12f + ROLLBACK.txt with the restore command
```

Then push with the sanctioned update path and verify:

```sh
./bin/wendy-demo device update --binary bin/wendy-agent-linux-arm64 --device wendyos-hubert.local
./bin/wendy-demo device info --device wendyos-hubert.local   # "version" shows the new build
./bin/wendy-demo data sources --device wendyos-hubert.local  # v2 DataService answers; camera healthy
```

The nightly auto-updater would overwrite this agent at ~00:10; it is disabled
for the demo window:

```sh
ssh root@10.60.10.90 'systemctl disable --now wendyos-agent-updater.timer'
# undo after the demo:
ssh root@10.60.10.90 'systemctl enable --now wendyos-agent-updater.timer'
```

## 4. Point the data plane at the dev cloud

Enrollment stays on production; only the episode transfer worker is
redirected. The agent's systemd unit reads `/etc/default/wendy-agent`
(`EnvironmentFile=`), so:

```sh
URL=$(CLOUDSDK_PYTHON=/opt/homebrew/bin/python3 gcloud run services describe \
      wendy-data-dev --region us-central1 --project cloud-c7e56 \
      --format='value(status.url)')
ssh root@10.60.10.90 "cat >> /etc/default/wendy-agent <<EOF
WENDY_DATA_INGEST_URL=$URL
WENDY_DATA_DIR=/data/wendy-agent/episodes
EOF
mkdir -p /data/wendy-agent/episodes && systemctl restart wendyos-agent"
```

- `WENDY_DATA_INGEST_URL` is new in PR #1754: it redirects the transfer
  worker's dial target and, because Cloud Run has no Wendy Envoy ingress to
  re-inject the mTLS identity, also attaches
  `x-wendy-client-cert: URI=urn:wendy:org:<org>:asset:<asset>` (the header
  `EnvoyCertMetadataExtractor` reads) carrying the enrolled asset identity.
- `WENDY_DATA_DIR` moves the episode store off the 7.3 GiB root partition
  (default `/var/lib/wendy-agent/data/episodes`) onto `/data`.

Verify the override took: the agent logs
`data transfer worker: ingest endpoint override set` at startup.

## 5. Deploy the D5 app and campaign

```sh
cd ../Examples/WendyDataModelApp
../../go/bin/wendy-demo run --device wendyos-hubert.local --detach --yes
../../go/bin/wendy-demo data campaign deploy campaign-hubert.yaml --device wendyos-hubert.local
../../go/bin/wendy-demo data campaign trigger model-harness-demo --device wendyos-hubert.local
```

Campaign adjustment made for Hubert: the checked-in `campaign.yaml` declares a
`camera: front` snapshot source, but the app itself holds `/dev/video0` open
(OpenCV), so the campaign's capture adapter gets `VIDIOC_S_FMT: device busy`
and the trigger fails. The deployed variant replaces the camera source with
`- telemetry: true` and keeps the triggers, upload policy, and model pin; the
`applications` source (every prediction and event record) is always captured
regardless, so episodes still carry the model's outputs. A second USB webcam
would let the original camera source work unchanged.

Verify, in order:

```sh
./bin/wendy-demo data episodes --device wendyos-hubert.local     # newest episode "complete"
./bin/wendy-demo data inspect <episode-id> --device wendyos-hubert.local
# trigger.reason, files [events.jsonl, telemetry.jsonl], upload.state "uploaded"
```

`upload.state: "uploaded"` is the cloud-side proof: the worker only records it
after `CommitEpisode` succeeds, and the server verifies every stored object's
SHA-256 against the manifest before flipping the catalog row to complete.
Read-side inspection of the catalog (QueryEpisodes/GetEpisode) requires a user
bearer token, not the asset identity; check rows in the dev ClickHouse
(`episodes` / `episode_files` tables) or through the BI surface once wired.

With nothing in front of the camera the app reports uncertainty 1.0, so the
`model.uncertainty > 0.65` trigger fires organically; walking into the frame
fires the edge-triggered `person_detected` instead. Each trigger seals one
episode (10 s pre-buffer, 20 s post) which uploads within seconds.

## Pitfalls discovered on the way (all hit for real)

1. Full root disk from updater backups: see Recon above.
2. `Dockerfile` export stage: ultralytics pulls the GUI `opencv-python`
   wheel needing `libxcb1 libgl1 libglib2.0-0`, and torch >= 2.13 needs
   `onnxscript` for ONNX export. Both fixed in this PR's Dockerfile.
3. App data socket refused with "peer is not in a wendy app scope": Hubert's
   containerd uses the cgroupfs driver, so the `system.slice:edge-agent:<app>`
   cgroup path is literal and the attribution parser did not recognize it.
   Fixed in PR #1755; without it no record ever reaches the agent.
4. After ANY agent restart the app's data socket listener is gone and the app
   gets ECONNREFUSED until the app is redeployed (`device apps remove --force`
   then `wendy run`). Cause: the chunk-diff deploy path creates containers
   without `sh.wendy/entitlement.*` labels, so `RestoreAppSystemAPISockets`
   skips them. Open bug; fix the deploy order (agent first, app last) until
   it lands.
5. An episode that exhausts its 5 upload attempts is terminally `failed` and
   is never retried; trigger a fresh episode after fixing connectivity.
6. `wendy device apps start` streams logs and does not return with `--detach`
   semantics; script around it or use `wendy run`.

## Rollback

```sh
ssh root@10.60.10.90 '
  cp /data/demo-hubert-rollback/wendy-agent.2026.08.07.sha-5fd12f /usr/local/bin/wendy-agent
  chmod 755 /usr/local/bin/wendy-agent
  sed -i "/WENDY_DATA_INGEST_URL/d;/WENDY_DATA_DIR/d" /etc/default/wendy-agent
  systemctl restart wendyos-agent
  systemctl enable --now wendyos-agent-updater.timer'
```

## Open gaps

- Camera frames inside episodes need a second webcam (or an app change to
  share frames) because the app monopolizes `/dev/video0`.
- The cgroupfs attribution fix (PR #1755) and the ingest override (PR #1754)
  must merge into `feature/wendy-data-platform`; Hubert currently runs a local
  build combining both.
- Chunk-diff deploys omit entitlement labels (pitfall 4) - open bug.
- Catalog read verification from the CLI needs a dev bearer token; today the
  proof is the commit-gated `uploaded` state plus the dev ClickHouse.
- The GPU (onnxruntime-gpu) variant of the app was not exercised; the demo
  runs the CPU path at 5 predictions per second.
