# Linux Desktop in `wendy install` with token pre-provisioning

Date: 2026-07-07

## Overview

Add a **Linux Desktop** entry to the `wendy install` device picker. Selecting it
does not flash a drive — it prints the same `curl … | bash` `agent.sh`
instructions from the [Linux Desktop docs
page](https://docs.wendy.dev/latest/installation/wendy-agent-linux/), optionally
enriched with a **short-lived enrollment token** so the freshly-installed
`wendy-agent` enrolls itself into the user's org on first startup.

Today the picker (`wendy install`, an alias for the hidden `wendy os install`)
offers two sections — **WendyOS** (disk images) and **Wendy Lite** (ESP32
firmware). "Linux Desktop" is neither: it installs `wendy-agent` on an existing
Linux install via a shell script, so it needs a distinct, non-flashing path.

## Motivation

- The docs already tell users to `curl … | bash` `agent.sh` to turn any Linux
  machine into a managed Wendy device. Surfacing that same flow inside `wendy
  install` makes it discoverable from the CLI users already have open.
- Pre-enrollment already exists for WendyOS images (the CLI mints an asset
  enrollment token, issues a full cert, and writes it to the config partition).
  A Linux desktop has no config partition, so we instead hand the agent a
  **short-lived token** and let it self-enroll — no private key or long-lived
  cert is ever embedded in a pasted shell command.

## Non-goals

- No change to the WendyOS image / config-partition pre-enrollment path.
- No new cloud RPCs. `CreateAssetEnrollmentToken` already supports
  `ttl_seconds` and returns `expires_at`; the agent already self-enrolls from a
  token via `ProvisioningService.StartProvisioning`.
- Windows/macOS targets: `agent.sh` already refuses non-Linux hosts; unchanged.

## Architecture & flow

```
wendy install
  └─ picker: [WendyOS…] [Wendy Lite…] [Linux Desktop]
                                            │ selected
                                            ▼
                    installLinuxDesktop(ctx, …)      (NEW, no drive/download/elevation)
                       │  logged in & not declined?
                       ├─ yes → CreateAssetEnrollmentToken(ttl=1h) → token, expires_at
                       │        print:  curl … | WENDY_ENROLLMENT_TOKEN=… WENDY_CLOUD_HOST=… bash
                       └─ no  → print plain:  curl … | bash        (docs parity, unenrolled)

target Linux machine:
  curl … | WENDY_ENROLLMENT_TOKEN=… WENDY_CLOUD_HOST=… bash
    └─ agent.sh installs wendy-agent (apt/dnf/yum/pacman/tarball)
         └─ if $WENDY_ENROLLMENT_TOKEN set:
              write /etc/wendy-agent/enrollment.json (0600, {token, cloudHost})
              (re)start wendy-agent

  wendy-agent startup:
    if not enrolled and /etc/wendy-agent/enrollment.json exists:
       decode org_id/asset_id from token claims
       ProvisioningService.StartProvisioning{token, cloudHost, orgID, assetID}
         → key-gen → CSR → IssueCertificate → saveState
       delete enrollment.json (bounded retry on transient network failure)
```

## Component 1 — CLI

**Files:** `go/internal/cli/commands/os_install.go` (picker wiring + routing),
new `go/internal/cli/commands/os_install_linux_desktop.go` (handler),
new `os_install_linux_desktop_test.go`.

- **Picker entry.** In `runOSInstall`, after building the WendyOS and Wendy Lite
  items, append one item when `flagDeviceType == "" && prNumber == 0`:
  - `Name: "Linux Desktop"`, `Description: "Install wendy-agent on an existing Linux machine"`,
    `Section: "Linux Desktop"`, `SortKey: "2_linux_desktop"`, `Value: linuxDesktopValue`
    (const `"linux-desktop"`).
- **Routing.** After the picker resolves `selected`, before `deviceMap[selected]`
  is dereferenced, add: `if selected == linuxDesktopValue { return installLinuxDesktop(ctx, preOpts, deviceName) }`.
  (Mirrors the existing `if selected == thorDeviceType` early return.)
- **`installLinuxDesktop`.** Skips `preAuthElevation`, drive picking, download,
  and config-partition writes entirely. It:
  1. Loads config (`config.Load()`); treats an unreadable config as "not logged in".
  2. Applies the same enroll gate as `resolvePreEnrollment`: `preEnrollSkip` →
     never enroll; `preEnrollAuto` → prompt via `confirmPreEnroll` only when
     interactive and sessions exist; `preEnrollForced` → require a session.
  3. When enrolling: `selectEnrollmentAuth` → `resolveOrg` → mint a token via a
     new small helper `createLinuxDesktopToken(ctx, auth, deviceName, orgID)`
     that calls `CreateAssetEnrollmentToken{OrganizationId, Name, TtlSeconds:
     3600}` and returns `(token, cloudHost, expiresAt)`.
  4. Prints the command via a pure, testable `renderLinuxDesktopInstructions(token,
     cloudHost, expiresAt)` returning a string. Empty token → plain command.
- **Failure / decline handling** matches `resolvePreEnrollment`: a token-mint
  failure in interactive mode falls back (with an explicit notice) to printing
  the plain command; in `--pre-enroll` (forced) non-interactive mode it errors.
- **Flags:** no new flags. Reuses `--pre-enroll` and `--cloud-grpc`.

### Printed instructions

Enrolled:
```
Install wendy-agent on your Linux machine, then it will enroll into <org> automatically.

  curl -fsSL https://install.wendy.dev/agent.sh | \
    WENDY_ENROLLMENT_TOKEN=<token> WENDY_CLOUD_HOST=<cloud-grpc-host> bash

This enrollment token expires at <expires_at> (in ~1h). Run the command before then.
After it boots, run `wendy discover` to find the device.
```

Unenrolled (not logged in / declined):
```
Install wendy-agent on your Linux machine:

  curl -fsSL https://install.wendy.dev/agent.sh | bash

The device is discovered over your local network — run `wendy discover`.
To enroll it into an org later, run `wendy device enroll` (or re-run
`wendy install` while logged in for a pre-enrollment token).
```

The env assignments prefix `bash`, so the piped script inherits them as
environment variables.

## Component 2 — `agent.sh`

**File:** `go/internal/cli/assets/docs/agent.sh` (the published
`install.wendy.dev/agent.sh` source).

- **Docs.** Add `WENDY_ENROLLMENT_TOKEN` and `WENDY_CLOUD_HOST` to the usage
  `Environment:` block.
- **Enrollment write.** After the final verify step (all install branches —
  apt/dnf/yum/pacman and the tarball fallback — reach the shared tail), add:
  if `${WENDY_ENROLLMENT_TOKEN:-}` is non-empty, require `${WENDY_CLOUD_HOST:-}`
  too (warn and skip if missing), then write `/etc/wendy-agent/enrollment.json`
  with mode `600`:
  ```json
  { "token": "<WENDY_ENROLLMENT_TOKEN>", "cloudHost": "<WENDY_CLOUD_HOST>" }
  ```
  The token is written via a heredoc/`printf` that does not echo it to stdout.
  Then `systemctl try-restart wendy-agent` (best-effort) so a
  package-manager-installed, already-running agent re-reads it. Print:
  "Enrollment token staged; the device will enroll on startup."
- The write uses the same `$SUDO` prefix already established in the script and
  `mkdir -p /etc/wendy-agent` (already created in the tarball path; created
  defensively here for package installs).

## Component 3 — Agent

**Files:** `go/internal/agent/services/provisioning_service.go` (startup hook),
shared token-claims helper, agent bootstrap wiring, tests.

- **Shared claim parsing.** Extract the asset-token claim decoding currently
  inside `enrollmentTokenCommonName` (`go/internal/cli/commands/auth.go`) into a
  shared helper (e.g. `certs` or a new `enrolltoken` package):
  `ParseAssetToken(token) (orgID, assetID int32, err error)`. The CLI keeps its
  existing behavior by calling the shared helper; the agent reuses it.
- **Startup hook.** In the agent bootstrap where the `ProvisioningService` is
  constructed and after `configpartition.Apply` runs, add
  `provisioning.ApplyEnrollmentFile(ctx)`:
  1. Read `/etc/wendy-agent/enrollment.json`; return silently if absent.
  2. If the service reports already enrolled, delete the file and return.
  3. Parse `{token, cloudHost}`; decode `orgID/assetID` from the token via the
     shared helper.
  4. Call `svc.StartProvisioning(ctx, &StartProvisioningRequest{EnrollmentToken:
     token, CloudHost: cloudHost, OrganizationId: orgID, AssetId: assetID})` —
     reusing the entire existing enroll pipeline (key-gen, CSR,
     `IssueCertificate`, `saveState`, PEM files, avahi update via callback).
  5. On success: delete the file, log enrolled org/asset.
  6. On failure: bounded retry (e.g. 3 attempts, ~5s apart) to tolerate a slow
     boot-time network; then delete the file and log loudly with a pointer to
     `wendy device enroll`. The 1h TTL self-limits any stuck token.
- The hook runs in a goroutine or blocks briefly at startup? Decision:
  **best-effort, non-blocking** — spawn it after the gRPC server is serving so a
  cloud outage never delays the agent coming up locally (mDNS discovery still
  works). Enrollment completing flips the mDNS advertisement to `tls=true` via
  the existing `OnProvisioned` callback.

## Security considerations

- The token is printed to the terminal and lands in the target machine's shell
  history and (briefly) in `/etc/wendy-agent/enrollment.json`. Mitigations: 1h
  TTL, file mode `600`, deletion immediately after the enroll attempt, and the
  token grants only the ability to enroll a single asset into one org (no other
  authority).
- No private key or issued certificate is ever placed in the pasted command or
  transmitted to the target by the CLI — the agent generates its own key locally
  and the key never leaves the device.

## Testing

- **CLI:** table-test `renderLinuxDesktopInstructions` (token vs. empty token).
  Test `installLinuxDesktop` routing/gate with stubbed token minting and
  `confirmPreEnroll`, mirroring the `os_install_enroll_test.go` stub-var pattern
  (`preEnrollDeviceFn`, `confirmPreEnroll`, `promptEnrollmentSession`).
- **Shared helper:** unit tests for `ParseAssetToken` (valid asset token,
  user token rejected, malformed token) — port the existing
  `TestEnrollmentTokenCommonName_*` assertions.
- **agent.sh:** a bats/shell test (or documented manual check) that
  `enrollment.json` is written only when `WENDY_ENROLLMENT_TOKEN` is set and is
  skipped (with a warning) when `WENDY_CLOUD_HOST` is missing.
- **Agent:** unit-test `ApplyEnrollmentFile` with a fake provisioning service /
  cloud dialer: file present → `StartProvisioning` called with claims-derived
  org/asset → file deleted; already-enrolled → file deleted, no call;
  malformed/expired token → file deleted, no crash, loud log.

## Decisions

- **TTL: 3600s (1h).** Long enough to SSH in and paste; short enough to limit
  exposure. Configurable later via a flag if needed.
- **Two env vars** (`WENDY_ENROLLMENT_TOKEN`, `WENDY_CLOUD_HOST`); the agent
  derives org/asset from token claims rather than requiring four env vars.
- **Enrollment handoff via a `600` file the agent consumes-and-deletes**, not a
  persistent entry in `/etc/default/wendy-agent`, so the token does not linger.
```
