# Cloud-owned UUID device unenrollment

Implements [WDY-3313 — Cloud v2 device enrollment leaves Jetson unprovisioned after reserving an asset](https://linear.app/wendylabsinc/issue/WDY-3313/cloud-v2-device-enrollment-leaves-jetson-unprovisioned-after-reserving) and [WDY-3534 — Preserve deleted Cloud asset bindings for stateless unenrollment recovery](https://linear.app/wendylabsinc/issue/WDY-3534/preserve-deleted-cloud-asset-bindings-for-stateless-unenrollment).

> **Development status:** New UUID/direct PKI local resets are disabled. The Agent advertises `CloudUnenrollmentSupported=false` and rejects direct PKI `Unprovision` without changing credentials. The CLI rejects this workflow before Cloud access, including `--yes` and `--check`, even when an older Agent advertises support. Enablement requires fresh FIDO2 approval bound to the exact signed reset instruction, verified durable device preparation through the signed command queue before revocation, and Cloud-authenticated deletion evidence. These integrations are not implemented. No deployment or live-reset readiness is claimed.

A serialized `DeletedAsset` supplied by an operator is not authenticated Cloud evidence. Matching its tenant, device, asset and timestamp fields cannot prove Cloud deletion or authorize credential erasure. Same-tenant operator mTLS and a valid issuer-signed OCSP response do not supply that missing authority. The unsafe new-authorization handler has been removed. Cloud-only unenrollment and numeric/Lite flows retain their existing behavior.

## One business operation

Cloud owns unenrollment for CLI and console callers. Its signed DeleteAsset path checks device:delete, relays PKI RevokeByPrincipal, then atomically records the tombstone and deletion audit. A relay failure retains the active asset; revocation already completed before an audit/database failure is irreversible and retry finishes deletion. The CLI and Agent do not perform a second ACME revocation. The old prerelease RevokeACMECertificate and CheckACMERevocation RPCs return Unimplemented.

The CLI signs the Cloud request and its exact tenant/device revoke_principal management authority with the same operator leaf. Existing kid/x5c behavior is retained, with a fresh PKI replay identity on each retry. Numeric Cloud v1 checks the complete certificate inventory before reset, then repeats the inventory and performs best-effort revocation and deletion after reset. This does not add a numeric fallback for PKI identities.

## Requirements before enabling new resets

The new reset workflow must authenticate the installed device and exact Cloud binding before any mutation. It must obtain fresh approval for the exact reset instruction and deliver that instruction through an authenticated signed command queue. The device must durably prepare for reset before Cloud revokes its credentials.

The Agent must verify Cloud-authenticated deletion evidence bound to the tenant, principal, asset UUID, installed certificate fingerprint and reset operation. Plain caller-supplied protobuf fields cannot satisfy this check. Issuer-signed OCSP evidence can establish certificate revocation, but cannot establish Cloud authorization or deletion. Fresh authenticated time and a durable anti-rollback floor must bound evidence and operator-certificate validity.

`--check` currently returns the disabled-workflow error. It must not claim that reset support is ready based on the old capability flag. Neither `--yes` nor an older Agent advertising `CloudUnenrollmentSupported=true` can bypass the CLI refusal.

## Historical reset records

New requests cannot create pending reset records. A development-version pending
receipt proves only that the device signed the caller's deletion fields. It does
not prove Cloud authorization or fresh-factor approval. Startup therefore blocks
pending recovery without deleting any remaining credentials or changing the
record. New enrollment stays blocked until explicit local recovery. The Agent
cannot complete an old pending reset through either version of `Unprovision`.

A previously completed receipt remains available through
`NotProvisionedResponse.unenrollment_completion`. This read-only report describes
the prior principal's operation, not a newly authenticated device identity. The
CLI must still anchor the historical certificate to configured roots, verify its
PKI evidence, and confirm the exact current authorized Cloud tombstone before
reporting completion. Plaintext transport and mDNS provide no mutation authority.

There is no CLI journal, saved phase, reusable enrollment credential or one-use
response token. Historical development journals and proof files remain untouched
and supply no reset authority. Normal enrollment replaces an existing completed
record. No importer accepts separate pending or completion files.

## Rollout and limits

Flashing is an explicit local reset, not an unenrollment transaction. Valid first-boot provisioning may replace previous pending/completed recovery state. An ordinary restart blocks pending reset recovery and preserves remaining credentials. Flashing does not implicitly revoke certificates or delete Cloud records; the operator must clean up the old Cloud identity separately. No automatic Cloud-cleanup prompt or workflow is introduced here.

Land [service-protos #96 — Typed deletion evidence through GetAsset](https://github.com/wendylabsinc/service-protos/pull/96), compatible [Cloud #754 — Lifecycle tombstones for safe unenrollment](https://github.com/wendylabsinc/cloud/pull/754) (including Sem's fixes), and the reset authorization/preparation protocol before enabling this consumer. Compatible schema versions alone do not enable reset. Unreleased development servers/agents are not a compatibility promise. Migration numbers and cumulative landing order must be reconciled before main deployment; source maximum is not a live migration cursor.

Cloud-only unenrollment can finish without a reachable device, but cannot erase an offline device's local credentials. Missing/purged Cloud evidence is unknown and fails closed. Missing/unavailable OCSP evidence preserves local keys even after Cloud completes. A receipt proves the earlier operation, not current physical-device identity or reachability. No live acceptance, installation or deployment is implied by source tests; the earlier production user-run acceptance used a different build.
