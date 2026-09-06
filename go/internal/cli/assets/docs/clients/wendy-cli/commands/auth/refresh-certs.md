# `wendy auth refresh-certs`

Renews the mTLS certificate of every stored auth session directly against
[pki-core](../../../../pki/)'s renew frontend. Cloud is not in this path.

A renewal is a re-issue, not a re-key of the same certificate: the CLI
generates a new key pair, builds a CSR carrying the same authoritative identity
URN as the certificate it replaces (`urn:wendy:org:‹org›:user:‹userID›` for
user sessions, `urn:wendy:org:‹org›:asset:‹assetID›` for device sessions,
taken from `~/.wendy/config.json`), and posts it to the renew frontend over
mTLS. Presenting the certificate being renewed *is* the proof of possession —
the request body carries nothing but the CSR.

## Usage

```sh
wendy auth refresh-certs
```

## The renew frontend

`WENDY_PKI_RENEW_ENDPOINT` names pki-core's renew frontend, e.g.
`https://renew.pki.example:8451/v1/renew`. Nothing is derived from the cloud
endpoint. When it is unset there is no renew frontend configured — a supported
state, in which the CLI simply never renews on its own.

## It also runs by itself

The CLI renews ahead of expiry without being asked: when an auth session is
resolved and its certificate has less than 15 minutes of validity left, the
renewal happens before the connection is attempted. This command is the manual
trigger for the same path, and is useful when a certificate is close to expiry
and you would rather not discover it mid-command.

## What renewal will not do

- **No roll-forward from an expired certificate.** Renewal requires a
  currently-valid certificate to present. Once yours has expired the renew
  frontend needs an approved grant, which the CLI cannot mint — log in again
  with [`wendy cloud login`](../cloud/login.md), which issues a fresh
  certificate outright.
- **Renewals are budgeted per lineage, and the budget is inherited rather than
  requested.** A plain operator certificate renews a fixed number of times and
  then has to be re-minted.
- **Entitlement-bearing and over-duration certificates are not renewable at
  all** unless their tenant has opted in.

Each of these arrives as an explicit refusal and is reported as itself, not
retried as a generic failure.

## Related

- [`wendy cloud login`](../cloud/login.md) — issue a fresh operator certificate.
- [`wendy auth use`](./use.md) — choose which stored session is the default.
- [PKI](../../../../pki/) — the three authorized certificate paths.
