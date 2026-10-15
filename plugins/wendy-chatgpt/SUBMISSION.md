# Wendy marketplace submission

The public package uses the production CLI relay at `https://mcp.wendy.dev`.
ChatGPT sends calls through Wendy Cloud to a paired `wendy cloud mcp` process
on the operator's computer. Calls stop when that process exits. The publisher
is Wendy Labs Inc. The package allows all countries and declares no commerce.

## Connection and OAuth

The public endpoint uses Streamable HTTP. Its protected-resource metadata
advertises only `mcp:relay` and issuer `https://auth.wendy.dev/relay`.
Issuer discovery is available at
`https://auth.wendy.dev/.well-known/oauth-authorization-server/relay`.
It advertises authorization at `https://auth.wendy.dev/authorize`, token
exchange at `https://auth.wendy.dev/relay/token`, PKCE S256, and client metadata
discovery. The package declares `client.mode: cimd` and
`baseScopes: ["mcp:relay"]`.

Do not use the standalone gateway's `wendy-chatgpt` client or its
`robots:read`, `cameras:capture`, `apps:control`, and `preferences:write` scopes
for this relay. Its separate configuration examples are not in the public ZIP.

Read-only checks on 2026-10-15 confirmed the relay metadata and that the
authorization endpoint accepts ChatGPT's client metadata URL before sign-in.
Those checks did not establish a successful OAuth grant or device operation.
Verify the current metadata and the complete connection before submission.

A live smoke check on 2026-10-15 used the rebuilt branch CLI with
`--cloud-grpc api.wendy.dev:443 --auto-accept` and an existing ChatGPT OAuth
connection. The CLI accepted the exact ChatGPT client identity without a
pairing prompt. The first `wendy_status` returned `relay_not_paired`; a retry
returned `auth: logged_in`. `cloud_discover(online_only=false)` returned the
account's enrolled devices. The relay accepted the CLI's tool catalog. The
session was then stopped. This check did not exercise a fresh sign-in,
reviewer account, camera, app control, or video recording.

## CLI setup and permissions

Use a dedicated test account and computer. Its Wendy organization must allow
MCP and Hosted MCP. Check the CLI before starting:

```sh
wendy --version
wendy cloud mcp --help
wendy auth login
wendy cloud mcp --allow-tool device_connect,camera_snapshot,container_stop
```

Use a CLI compatible with the relay's published catalog. The local installation
`2026.10.13-144211` checked during preparation lacked `cloud mcp`; a build of
this branch provides the command. A build alone does not establish compatibility
with the live relay or availability in the published CLI release.

Connect ChatGPT using the intended Wendy account and organization, then call
`wendy_status` to start pairing. OAuth sign-in alone does not start it. Compare
the returned code with the CLI terminal before accepting, then retry the status
call. With `--auto-accept`, the initial status request may return
`relay_not_paired` while approval is being processed; retry it after acceptance.
Keep that computer awake and the CLI process running. The relay serves its
reviewed CLI catalog, including `wendy_status`, `cloud_discover`,
`device_connect`, `device_info`, `container_list`, `telemetry_logs`,
`camera_list`, `camera_snapshot`, and `container_stop`. It does not serve the
standalone gateway's `list_robots` or `inspect_robot` tools.

The default policy permits read-only tools that do not access the host.
`cloud_discover` lists the paired session's Cloud enrollments. `device_list`
also accesses host configuration or LAN discovery, so it requires its own
`--allow-tool` permission even when `scan=false`. Device connection, camera
snapshots, and app stops require both an allowlisted tool name and confirmation
of each call in the terminal. Tool catalog visibility does not grant permission.

Add `--auto-accept` to automatically accept a pairing request from ChatGPT's
known OAuth client identity. It does not trust a client merely because its
display name says ChatGPT. Other clients still require manual pairing. The
flag does not grant tool permissions or skip per-call confirmations.

## Reviewer access remains a release gate

Pairing is scoped to the Wendy subject, organization, OAuth client, and OAuth
connection grant. A publisher's existing pairing does not prepair a reviewer's
new grant. `--auto-accept` can accept a new ChatGPT pairing while an eligible
CLI session is waiting, but that path still needs live verification. Session
expiry, network loss, or exiting the CLI can require setup again.

The extra tools in the walkthrough still need per-call terminal confirmation.
OpenAI requires a demo account reviewers can use without further configuration.
Verify how reviewers can complete all submitted cases without depending on a
developer's terminal before calling this package ready. Do not assume that
pairing automation alone resolves this requirement, or enable `--allow-all`
without an explicit decision about its much broader access.

Provide account credentials and access instructions only in the secure reviewer
fields, never in the repository or ZIP. Reviewers must not depend on a personal
mailbox, phone, production account, or private network.

## Fixtures and cases

Five positive and three negative cases are embedded in `plugin.json`. Their
status is **not run** against the submitted version. Prepare:

- An online Cloud enrollment named `Review Device` and an offline enrollment
  named `Review Offline`.
- A disposable running `review-logger` app with synthetic INFO/WARN telemetry,
  another harmless running app named `review-helper`, and a stopped sample app.
- A working test camera aimed at a staged scene without people or private data.
  The CLI host needs GStreamer with snapshot decoding and JPEG plugins.

Positive cases cover Cloud inventory, device and app inspection, bounded logs,
one camera snapshot, and an explicit app stop followed by `container_list`
read-back. The stop response alone acknowledges a request. Negative cases cover
unsupported WiFi credential handling, retrieving authentication secrets, and
purchases. The relay can expose simulator and development tools with permission,
so local simulation and deployment are not unsupported-capability tests.

Also test unpaired/expired sessions, denied tool permissions, denied terminal
confirmation, account/organization isolation, and reconnect behavior. Record
Passed, Failed, Blocked, or Not run with evidence outside the manifest. Keep
unknown readiness explicit. Test desktop and mobile access.

## Record the walkthrough

Use an existing development connection to this relay with the dedicated test
account. Rehearse the exact prompts in `plugin.json` before recording. Show the
CLI version and command, ChatGPT connection, pairing, actual tool results, and
terminal confirmations. Keep credentials and unrelated content off screen.
Do not substitute mock results or a different gateway for this package.

Record the five positive prompts in their manifest order, then the unsupported
Raspberry Pi purchase prompt. Allow time to read each result. The sequence
shows inventory, inspection, logs, a snapshot, a verified disposable app stop,
and an explanation of an unsupported action.

Verify playback, readable results, and absence of secrets. Host the actual
recording at a reviewer-accessible URL and verify playback without requesting
access. Add it to `extensions.com.openai.review.demo_recording_url`. A script
or a local video alone does not complete this field.

## Package checks

From the repository root, synchronize generated skills and the embedded
manifest, then validate and build the draft:

```sh
python3 scripts/sync-agent-skills.py
python3 scripts/sync-agent-skills.py --check
python3 scripts/test-package-chatgpt-plugin.py
python3 scripts/package-chatgpt-plugin.py --check
python3 scripts/package-chatgpt-plugin.py --output /tmp/wendy-robots-marketplace-draft.zip
```

After adding and verifying the recording URL:

```sh
python3 scripts/sync-agent-skills.py
python3 scripts/package-chatgpt-plugin.py --require-submission-metadata --output /tmp/wendy-robots-marketplace.zip
```

Strict validation checks metadata structure and presence. It does not verify
playback, reviewer access, live cases, or approval. Inspect the final ZIP,
which contains only `plugin.json`, `mcp.json`, `skills/`, and `assets/`.

## Listing and final portal checks

Verify the existing website, support, privacy, and terms URLs in the manifest.
The privacy policy must cover requested device results and camera images
passing through the CLI and Wendy Cloud to ChatGPT, with actual retention and
account-linking practices. Do not infer coverage from an HTTP success.

Use the existing draft in the intended OpenAI organization and project.
Reuploading replaces the bundle and resets attestations; inspect the saved
version and preserved connection before changing it. Complete domain
verification, OAuth setup, tool/skill scans, reviewer access, and live cases.
The domain challenge belongs to the Cloud relay deployment; do not replace it
with an invented value or route traffic to a different service for this package.

Have the publisher complete attestations. Draft upload, submission for review,
and publication after approval are separate actions.

References: [OpenAI submission requirements](https://developers.openai.com/plugins/deploy/submission),
[remote MCP review](https://developers.openai.com/plugins/deploy/app-review), and
[plugin guidelines](https://developers.openai.com/plugins/plugin-guidelines).
