# HANDOVER — Autonomous Thor-specific issue hunting

Date: 2026-07-02

## Mission

Run your own tests against the physical Jetson AGX Thor autonomously and hunt
for **Thor-specific issues** — things that would not reproduce on an Orin, a
Raspberry Pi, or a VM. Thor is the newest, least-tested WendyOS target
(JetPack 7.2, new GPU architecture, nvme install path, flashpack flashing), so
assume the interesting bugs live where Thor differs from older Jetsons.

Work autonomously: plan, test, record, clean up, repeat. Do not wait for the
user between tests. Stop and ask only before anything listed under "Never do"
or when the device becomes unreachable.

## Deliverables (in this worktree)

- **`FINDINGS.md`** — Thor-specific issues: symptom, repro steps, evidence
  (command output), severity, suspected layer (CLI / agent / OS image /
  JetPack). Do NOT file Linear issues autonomously; Konstantin reviews
  FINDINGS.md and decides what to file.
- **`OBSERVATIONS.md`** — everything else noticed on the way that is *not*
  Thor-specific: CLI papercuts, docs gaps, general WendyOS bugs, flaky
  behavior. One entry per observation, same evidence discipline. Check "Known
  issues" below first so you don't re-record those.
- **`TESTLOG.md`** — append-only log: what was run, when, result. This is what
  makes the session resumable after a crash or compaction.

## Device: Thor (SHARED — read this twice)

```text
hostname: wendyos-curious-meteor.local
LAN IP:   192.168.2.173
OS:       WendyOS-0.16.1, JetPack 7.2, nvme
SSH:      key auth as `wendy` and `root` (inspection is fine; config changes are not)
```

Two other live workstreams use this same physical device today:

1. **HelloVLM** (`sh.wendy.examples.hellovlm`, WDY-1799 session) — RUNNING.
   Owns port 8080, port 11434 (Ollama), the Brio 100 **camera**, and does GPU
   inference. Never stop/remove/redeploy it.
2. **Manual audio test** (kb.thor-audio-test session, Konstantin driving) —
   may deploy an audio app on **port 3004** using the Brio 100 **microphone**
   at any time today.

Sharing rules:

- App IDs: prefix everything you deploy with `autotest-` and only ever
  stop/remove `autotest-*` apps.
- Ports: use **3100–3199** only.
- Camera: OFF LIMITS (HelloVLM owns it). Mic: avoid while an audio app is
  deployed; check `device apps list` first.
- GPU: shared with Ollama. Light GPU checks are fine; before anything heavy,
  check whether HelloVLM is mid-inference (`wendy device logs --app
  sh.wendy.examples.hellovlm`) and keep heavy runs short.
- Before and after every test: `wendy --json --device
  wendyos-curious-meteor.local device apps list` — leave the device exactly as
  found (HelloVLM only).

## Never do (hard rails)

- No reboot, shutdown, reflash, `wendy os install`, or `wendy device update`.
- No OS/system config changes over SSH (no writes under /etc, no systemctl
  changes, no package installs on device). SSH is for read-only inspection.
- No stopping/removing/modifying apps you didn't create (`autotest-*` only).
- No `brew upgrade`/CLI update — other sessions share this machine's tooling.
- No `wendy device set-default` — other sessions rely on their own defaults.
- Nothing against `mac-mini.local` — it's a live agent on the LAN. **Always**
  pass `--device wendyos-curious-meteor.local` explicitly.

## Operating notes

- Builder: `--builder apple-container` (this user has no Docker socket).
- Use `--json` for inspection and `--yes`/`--force` for actions — you want to
  suppress interactive TUI (unlike the manual-test session, which wants it).
- Scaffold template projects under `/tmp/wendy-thor-tests/` (never `wendy run`
  inside `/Volumes/Projects/WendyLabs/templates` — unexpanded `{{.VAR}}`
  placeholders).
- CLI is `/opt/homebrew/bin/wendy` 2026.07.01-101829; ignore the update banner.
- E2E suite recipe (already ran green except one known failure, see below):
  `WENDY_E2E_AGENT_USER=wendy WENDY_E2E_CLI_AUTH_CONFIG_PATH=/Users/ai/.wendy/e2e-config.json
  make e2e-test-wendy DEVICE=wendyos-curious-meteor.local` from `swift/`.

## Known issues — do NOT re-record, but note anything NEW about them on Thor

- WDY-1796 — interactive CLI prompts freeze pty-based E2E tests.
- WDY-1797 — E2E harness inherits operator's ~/.wendy/config.json.
- WDY-1798 — CLI routes LAN connections through HTTPS_PROXY (breaks
  `device info --check-updates` on mDNS names).
- `nerdctl logs` broken for Wendy-managed containers (`LogPath: ""`,
  namespace error); supported path is `wendy device logs`.

## Suggested test directions (prioritize Thor-divergent surface)

Order roughly by expected signal; adapt freely as findings come in.

1. **Device introspection sweep** — `device info`, `device camera list`, cache
   commands, telemetry/logs paths. Does everything report Thor correctly
   (model name, JetPack 7.2, storage, memory)? Wrong/missing hardware metadata
   is a classic new-SoC bug.
2. **GPU entitlement on Thor** — deploy a minimal `autotest-gpu` app (CUDA
   device query or PyTorch `cuda.is_available()` — python/llm template's base
   image knowledge may help). Thor's GPU architecture is newer than Orin's:
   check the right `/dev/nvidia*` nodes appear in-container, CUDA init works,
   and library mounts match JetPack 7.2. This is the single most
   Thor-specific surface.
3. **Baseline + churn** — `python/simple-api` on port 3100: deploy, verify,
   remove, redeploy several times. Look for layer-cache issues, leftover
   volumes (`wendy cache`, `/var/lib/wendy/volumes` via SSH), readiness-probe
   flakes.
4. **Swift cross-compile path** — a Swift template (e.g. swift/simple-api) to
   Thor: does the Swift-on-ARM64 story hold on JetPack 7.2?
5. **Audio APE surface** — card 1 (Jetson APE/ADMAIF, dozens of pcm devices)
   is Thor-specific hardware. Does the audio entitlement map it sanely
   in-container? (Skip Brio mic capture if the manual audio session is live.)
6. **Resource truthfulness under load** — while something runs: `device logs`
   streaming stability, telemetry values plausible for Thor (memory, thermal),
   log rotation.
7. **E2E suite re-run** (optional, ~21s + build) — confirm the earlier result
   reproduces; any *new* failure vs. the 2026-07-02 run (807 tests, only
   WDY-1798's one failure) is signal.

## Session hygiene

- Append to TESTLOG.md as you go, not at the end.
- Commit FINDINGS/OBSERVATIONS/TESTLOG to this branch periodically
  (`kb.thor-autonomous-tests`) — do not push, do not open PRs.
- On wrap-up: device back to HelloVLM-only, `/tmp/wendy-thor-tests/autotest-*`
  dirs may stay, update this HANDOVER.md's status section below.

## Status

- 2026-07-02: worktree created, no tests run yet.
