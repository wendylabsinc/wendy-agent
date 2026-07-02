# OBSERVATIONS — non-Thor-specific notes

CLI papercuts, docs gaps, general WendyOS bugs, flaky behavior noticed along the way. Known issues WDY-1796/1797/1798 and the nerdctl-logs limitation are excluded unless something NEW shows up.

## O1 — `device volumes list` usedBy is a name-prefix heuristic; null for real in-use volumes

`buildVolumeUsageMap` (container_service.go:873) marks a volume as used only if its name starts with the *appID* (`<appID>` or `<appID>-…`). HelloVLM (`sh.wendy.examples.hellovlm`) is RUNNING with `hellovlm-models` (17.4 GB) mounted, yet `wendy device volumes list --json` shows `"usedBy": null` for every volume. Consequence: an operator can't tell live volumes from orphans (e.g. leftover `thor-llm-kb-main-*`, 11.4 GB, from a removed app). Same prefix logic in `deleteVolumes` implies app removal may not delete such volumes either (volume names come from wendy.json, not prefixed by appID). Not Thor-specific. Layer: agent.

## O2 — `device top`/dashboard shows GPU memory 0 B on Jetson (nvidia-smi returns [N/A])

`nvidia-smi --query-gpu=memory.used,memory.total` on Thor returns `[N/A], [N/A]` (unified memory); `ParseNvidiaSMI` maps that to 0/0 and the JSON/TUI shows 0 B used / 0 B total instead of hiding the field or showing N/A. Affects any Jetson with nvidia-smi (JP6+), not just Thor. Layer: agent hoststats + CLI display.

## O3 — `device info` has no memory/CPU fields

`GetDeviceInfoResponse` has disk, GPU, JetPack, partitions — but no RAM size or CPU core count (Thor: 128 GiB / 14 cores, only visible via `device top`). Papercut for fleet inspection; not Thor-specific. Layer: proto/agent/CLI.

## O5 — `wendy init --template X --entitlement Y` silently ignores the entitlement flags

`wendy init --app-id autotest-gpu --target wendyos --language python --template simple-api --entitlement gpu` produces a wendy.json with only the template's `network` entitlement — the explicit `--entitlement gpu` is dropped without any warning. Code: `init_cmd.go` — `runTemplateFlow` uses the template's wendy.json verbatim; `resolveInitEntitlements` only runs in the wizard (no-template) flow. Reproduced with dev CLI (worktree @ 66b74dee) and installed 2026.07.01-101829. Expected: merge the requested entitlements into the template config, or error "cannot combine --template with --entitlement". Layer: CLI.

## O6 — `device logs --app X --tail N` returns nothing when other apps are chatty

`--tail` selects the last N log batches globally and only then applies the `--app` filter. On this device HelloVLM's llm service dominates the recent batches, so `device logs --app autotest-gpu --tail 10` printed nothing even though the app had logged seconds before (live streaming with the same filter works). Expected: replay the last N batches *of the filtered app*. Layer: agent StreamLogs or CLI. Not Thor-specific (needs a chatty co-tenant to notice).

## O7 — all container stderr lines are labeled severity=WARN

llama.cpp informational lines (`I slot print_timing: ...`) from HelloVLM's llm service appear with `"severity":"WARN"` purely because they were written to stderr. Many well-behaved servers (uvicorn included) log INFO to stderr, so `--level warn` filtering will be full of INFO noise, and dashboards will over-report warnings. Layer: agent log ingestion. Not Thor-specific.

## O4 — `wendy device camera list` shows only /dev/video0 for Brio 100 while `hardware list` shows video0+video1

Minor inconsistency: hardware list reports both uvcvideo nodes (video1 is the UVC metadata node), camera list filters to capture nodes. Arguably correct behavior; recorded for completeness. Not Thor-specific.

