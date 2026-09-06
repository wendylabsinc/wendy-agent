# `wendy device enroll`

Obtains a single-use enrollment credential for the connected device and stages
it on the device, which then enrols itself directly against
[pki-core](../../../../pki/) over **ACME** — or **EST** on constrained hardware
that cannot run an ACME client.

> **Note:** `wendy device enroll` is an advanced command and is not listed in
> `wendy device --help`. It remains fully functional. For most setups, use
> [`wendy device setup`](./setup.md) instead.

## Usage

```sh
wendy device enroll [--name <name>] [--org <id>] [--cloud-grpc <endpoint>]
```

## Description

Run [`wendy cloud login`](../cloud/login.md) first — the request for the
credential is signed with your operator certificate, and an authenticated
operator is the root of trust at first touch. Wendy ships no factory birth key.

1. The CLI asks for an enrollment credential for the connected device. Cloud
   relays the signed request to pki-core, which mints a credential bound to
   that device and to your tenant, single-use and short-lived. Cloud can
   withhold the request; it cannot issue a certificate.
2. The CLI stages the credential on the connected agent and registers the
   device under a human-readable **name** (see below).
3. The agent generates its own key pair — the private key never leaves the
   device — and redeems the credential against pki-core: an ACME account bound
   by the external-account-binding (EAB) credential, then an order whose only
   identifier is a `permanent-identifier` carrying the device's DeviceID. On
   EST hardware it is a simple enrolment with the same credential.
4. pki-core stamps the device identity
   (`spiffe://wendy.sh/tenant/‹uuid›/device/‹DeviceID›`) into the issued leaf.
   The device asserts no identity of its own: any CN or URI SAN in its CSR is
   replaced server-side, and only the CSR public key is honoured.

No certificate and no private key ever travels through this command.

> **The credential is single-use.** The agent persists its ACME account key
> before spending the credential, so a re-run after a crash re-registers the
> same account instead of burning a second one. A credential that has already
> been redeemed cannot be reused — run `wendy device enroll` again for a fresh
> one.

The enrolled device is registered in Wendy Cloud under a human-readable **name**. The name can be changed later with `wendy device rename`, so the command resolves it as follows:

1. **`--name <name>`** — always wins when provided.
2. **Hostname default** — when `--name` is omitted and the device is reachable by hostname (e.g. `playful-reed.local`), the name defaults to that hostname with any `.local` suffix stripped (so `playful-reed.local` → `playful-reed`).
3. **Interactive prompt** — in a terminal, when no `--name` is given the command prompts for a name. When a hostname default is available it is shown in brackets and used if you press Enter without typing anything:

   ```
   Device name [playful-reed]:
   ```
4. **Bare IP / no hostname** — when the device is addressed by a bare IP (no resolvable hostname) and `--name` is omitted, there is no default. In a non-interactive environment this fails with:

   ```
   device name is required; pass --name when not running interactively
   ```

   In an interactive terminal it prompts for a name with no default and errors if you leave it blank.

> **Naming an unnamed device:** A device enrolled without a usable name shows up with an empty name in [`wendy cloud discover`](../cloud/discover.md). You can still address it by its numeric asset ID — see [`wendy cloud tunnel --device <id>`](../cloud/tunnel.md).

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | hostname (`.local` stripped) | Human-readable device name. Defaults to the device hostname when omitted; required when the device is reachable only by a bare IP address in a non-interactive environment. |
| `--org` | interactive picker | Organization to enrol the device into. Required in non-interactive or `--json` runs when you belong to more than one org. |
| `--cloud-grpc` | `""` | Cloud gRPC endpoint that relays the credential request. Overrides session selection; when omitted, the persisted default (set with `wendy auth use`) is used if available, otherwise an interactive picker appears. |

## Examples

Enroll, defaulting the name to the device hostname:

```sh
wendy device enroll --device playful-reed.local
```

Enroll with an explicit name:

```sh
wendy device enroll --device 192.168.1.11 --name lab-pi-01
```

## Renewal

Device certificates renew unattended over the same ACME or EST path, with no
operator in the loop. Renewal needs a currently-valid certificate to present:
once a device leaf has expired there is no roll-forward, and the device has to
enroll again.

## Related

- [`wendy install` → Linux Desktop](../install.md) — mint a short-lived enrollment token and embed it in the `agent.sh` one-liner so the device self-enrolls on first startup, without needing a USB connection or a running agent.
- [`wendy device setup`](./setup.md) — interactive wizard that provisions, configures WiFi, and enrolls in one flow.
- `wendy cloud enroll-device` — alias for this command, reachable through the cloud tunnel.
- `wendy device unenroll` — reverse enrollment and delete the device from Wendy Cloud.
- [PKI](../../../../pki/) — the three authorized certificate paths.
