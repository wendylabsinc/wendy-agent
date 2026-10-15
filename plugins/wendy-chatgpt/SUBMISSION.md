# Wendy marketplace submission

The public package targets standalone Wendy Cloud access at
`https://mcp.dev.wendy.dev`. Users link their account and operate permitted
devices without running a local CLI. The publisher is Wendy Labs Inc.; the
package allows all countries and declares no payments or purchases.

## Current release gate

**Do not submit this draft against the currently deployed endpoint.** The
2026-10-14 audit found that it still serves the session relay, with the
`mcp:relay` scope and CLI pairing. The intended service is the standalone
gateway's `http.cloud_link` mode, with `robots:read`, `cameras:capture`,
`apps:control`, and `preferences:write`. Deploy and test that service first.

The supplied configuration and OAuth registrations are deployment inputs.
They do not establish that the service, clients, or domain verification exist.

## Package checks

From the repository root:

```sh
python3 scripts/sync-agent-skills.py --check
python3 scripts/test-package-chatgpt-plugin.py
python3 scripts/package-chatgpt-plugin.py --check
python3 scripts/package-chatgpt-plugin.py --output /tmp/wendy-robots-marketplace-draft.zip
```

The exporter checks the public file inventory, images, publisher, hosted MCP
connection and required review fields. It preserves the review cases in the
ZIP and reports missing metadata. When the actual recording is hosted and
verified, add `extensions.com.openai.review.demo_recording_url`, then run:

```sh
python3 scripts/package-chatgpt-plugin.py --require-submission-metadata --output /tmp/wendy-robots-marketplace.zip
```

This stricter check verifies field presence and structure. It cannot verify
policy coverage, recording playback, test-account access, or portal approval.
The upload includes only `plugin.json`, `mcp.json`, `skills/`, and `assets/`.
Deployment configuration, reviewer credentials and private state stay outside it.

## Standalone service deployment

Use [gateway.marketplace.example.json](gateway.marketplace.example.json) for
the requested development host. Its Wendy service URLs were checked against
the current auth/cloud source. Keep its dedicated upstream client and the
downstream ChatGPT client separate:

- `wendy-chatgpt-gateway` is a public, DPoP-bound Wendy OAuth client. Register
  [oauth-client-registration.json](oauth-client-registration.json) through
  the existing platform-admin client flow, or its authenticated
  `POST /api/realms/system/clients` endpoint. The exact upstream callback is
  `https://mcp.dev.wendy.dev/auth/callback`. The live audit found this client
  was not registered. Do not substitute the browser's `cloud-login` client,
  whose registered callbacks do not include this gateway.
- `wendy-chatgpt` is the public client registered in the gateway configuration
  and declared in `mcp.json`. It uses PKCE and the stable ChatGPT callback
  `https://chatgpt.com/connector_platform_oauth_redirect`. The gateway's
  authorization responses identify their exact issuer, including errors.

Provision a private persistent directory at `/var/lib/wendy-chatgpt`, mode
`0700`, and supply `WENDY_CHATGPT_STATE_KEY` from the deployment secret store.
It must be base64 for a random 32-byte key. Do not put it in the configuration,
package, command history, or review notes. Run one gateway process per state
file; the current encrypted file store does not support multiple writers.
Keep its key and encrypted login file across service restarts.

Build the current MCP UI assets and CLI using the repository build process,
then run the service behind HTTPS with the chosen policy:

```sh
wendy mcp gateway --config /etc/wendy/gateway.json --listen 127.0.0.1:8788
```

Route the exact root MCP endpoint, `/.well-known/oauth-protected-resource`,
`/.well-known/oauth-authorization-server`, `/oauth/authorize`, `/auth/callback`,
`/oauth/token`, `/oauth/revoke`, and `/healthz` to that process. The existing
cloud tunnel MIG routes this hostname to the Swift session relay; it needs an
explicit infrastructure cutover. A successful `/healthz` alone is insufficient.
Camera capture needs the gateway host's GStreamer runtime and capture plugins.

Serve the portal's actual domain challenge token as plain text at
`https://mcp.dev.wendy.dev/.well-known/openai-apps-challenge`. Do not invent a
token or replace another plugin's challenge. Apply authorization/token rate
limits at ingress. Do not log MCP request or response bodies, device results,
camera data, or authorization credentials. Hosted display preferences are
temporary; encrypted account-link state remains persistent.

## Listing and policy evidence

All four listing URLs are public pages on `wendy.dev`. The support page provides
contact and help links. The privacy page needs to describe requested device
results passing through Wendy to ChatGPT, no retention of MCP session data,
and the separate encrypted login state. Its existing blanket statement about
not collecting device contents does not explain this flow. Keep terms and
support copy consistent with the Wendy plugin. Publish and inspect the final
pages before checking off policy coverage.

The icon is the existing 1024 × 1024 PNG and remains legible at small sizes.
The listing describes standalone device inspection, telemetry, requested camera
images, permitted app controls, and temporary workspace preferences. It does
not promise laptop files, simulators, builds, deployments or app web proxies.

## Review fixtures and test evidence

Five positive and three negative cases are embedded in `plugin.json`. They
are **not run** against the submitted service. Prepare a dedicated test account
whose realm allows MCP and hosted MCP, with:

- An online enrollment named `Review Device` and an offline one named
  `Review Offline`.
- A disposable running `review-logger` app emitting synthetic INFO/WARN logs,
  another harmless running app, and a stopped sample app.
- A working test camera aimed at a staged scene without people or private data.

The positive cases cover inventory, explicit device/app inspection, bounded
logs, one camera still, and an explicitly requested app stop with read-back.
The negative cases cover unsupported laptop simulation, local files/builds,
and purchases. Read each case's exact permissions and expected arguments.
Record Passed/Failed/Blocked with evidence outside the public manifest.

Verify two accounts cannot access each other's devices. Test revoked and
expired logins, token refresh, reconnect, denied camera/app permissions and
disabled hosted-MCP realm policy. Run the UI on desktop and mobile. No local
test result substitutes for these live account and device checks.

## Demo and final portal work

After a development connection to the standalone service works, rehearse the
positive cases and the unsupported laptop-simulator case in ChatGPT. Record
account linking and the actual prompts/results using the test fixtures. Keep
credentials off screen, play back the recording, then host it where reviewers
can watch without requesting access. Write the verified URL into the manifest.

Supply the test account, exact login URL, tenant and sign-in instructions only
in the secure reviewer-access fields. Reviewers must not need the developer's
phone, mailbox, terminal or a private network. Keep that account and fixtures
available for later reviews.

Once preparation is complete, use the intended OpenAI organization/project and
verified Wendy Labs Inc. identity to upload the ZIP as a draft. Complete domain
verification, OAuth connection, tool/skill scans and live cases on that saved
version. Have the publisher complete attestations, submit for review, and
publish only after approval. These are separate actions.

References: [OpenAI submission requirements](https://developers.openai.com/plugins/deploy/submission),
[authentication](https://developers.openai.com/plugins/build/auth), and
[plugin guidelines](https://developers.openai.com/plugins/plugin-guidelines).
