# Security Issues Remediation Plan

_Created 2026-06-27. Covers the 26 security issues assigned to Joannis in the `Security: Wendy Agent` Linear project._

## How this is organized

Issues are grouped into **workstreams by shared code surface** (so fixes batch into coherent PRs and get tested together), then sequenced into **phases by dependency and risk**. Severity is from the ticket; ⚠️ marks changes that can break already-deployed apps and therefore need a migration path + on-hardware validation (Jetson/Pi/Thor).

Three codebases are in play:
- **agent/cli** — this monorepo, `go/internal/agent/`, `go/cmd/wendy`
- **os** — Yocto/packaging, `packaging/linux/systemd/`, meta-wendyos layers
- **cloud** — separate cloud repo (protos live here in `Proto/cloud`)

---

## Workstreams

### WS1 — Cloud API authorization (cloud repo) — highest ROI
Small, surgical server-side authz checks; independent of all agent work, can start immediately and in parallel.

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1189 | HIGH | `IssueCertificate`: require enrollment-token JWT to embed a specific `asset_id`; reject org-only tokens; verify request `asset_id` == token claim |
| WDY-1190 | HIGH | `SubscribeToMapEvents`: derive org from session + Casbin ownership check on the map record (or add+validate `organization_id`) |
| WDY-1191 | HIGH | `UpdateOAuthClient`: verify caller's org owns `client_id` before applying changes (esp. `redirect_uris`) |
| WDY-1192 | HIGH | Revocation: agent consults org CRL/OCSP during signature verify, and/or cloud-pushed digest quarantine list (cross-cuts WS4) |

### WS2 — OCI container isolation (`go/internal/agent/oci/spec.go`, `oci/entitlements.go`)
All touch the same 2 files. Several are ⚠️ breaking for existing apps — gate behind validation.

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1008 | HIGH | `applyCamera`: stop rbind-mounting host `/dev`; enumerate only `/dev/video*`,`/dev/media*` into `spec.Linux.Devices` |
| WDY-1093 | HIGH | D-Bus: hard-fail container start when `xdg-dbus-proxy` absent and a D-Bus entitlement is declared (no silent unfiltered fallback) |
| WDY-1094 | HIGH | Drop `CAP_NET_ADMIN` from host-net containers unless a separate explicit entitlement is declared |
| WDY-1011 | HIGH ⚠️ | Add user namespace + UID/GID remap (container root → 100000+); non-root default process user |
| WDY-1012 | MED | Apply Docker-default seccomp baseline; layer additions per-entitlement |
| WDY-1102 | MED | Deny any bind mount matching `/run/containerd/*` in the spec builder |
| WDY-1014 | MED ⚠️ | Default network mode `bridge`/`cni`, not `host`; require `host` opt-in |
| WDY-1101 | MED ⚠️ | Default CPU/mem cgroup limits when manifest omits them — **coordinate with WDY-1729 (resource limits, already In Review)** |

### WS3 — Agent RPC attack surface (`go/cmd/wendy-agent/main.go`, `services/provisioning_service.go`, `containerd/client.go`)

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1009 | HIGH | `startPostStartAgentHook`: drop `sh -c`; exec allowlisted hook names directly. **Relates to WDY-1197 (deprecating JSON app_config), In Progress** |
| WDY-1010 | HIGH | debugpy: bind `127.0.0.1`, gate behind build tag impossible to set in prod, optional per-session token |
| WDY-1006 | HIGH | Pre-provisioning plaintext server registers **only** `ProvisioningService`; gate all other services behind mTLS; bind narrowly not `[::]` |
| WDY-1092 | HIGH | Protect enrollment token in transit (single-use+short-TTL confirm at cloud, or ECDH/TLS-bootstrapped channel). Builds on WDY-1006 |

### WS4 — Update & artifact integrity / code signing (`services/agent_update_service.go`, `os_update_service.go`, OCI verify path)
Depends on signing-key infrastructure from WS6/WDY-1001 existing first.

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1088 | HIGH | Verify OCI image signature (cosign/Notary v2) before `RunContainer`; pin by digest |
| WDY-1089 | HIGH | `UpdateAgent`: verify binary signature against pinned key + URL allowlist (hash alone is insufficient) |
| WDY-1090 | HIGH | `UpdateOS`: HTTPS-only + strict TLS, require signed Mender artifact, restrict RPC to high-priv role |
| WDY-1192 | HIGH | Revocation check (shared with WS1) |

### WS5 — Provisioning trust / mTLS identity / CLI pinning

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1086 | HIGH | Pin/hard-code expected cloud CA in firmware so config can't override; host allowlist before dialing |
| WDY-1087 | HIGH | mTLS client cert revocation (OCSP/CRL — shares infra with WDY-1192) + short-lived certs w/ auto-renew |
| WDY-1149 | — | CLI: on `wendy device set-default`, pin device cert/pubkey locally; validate on subsequent connections |
| WDY-1098 | MED | BLE: disable TLS session resumption/tickets; document & restrict pre-mTLS BLE commands |

### WS6 — OS hardening & supply chain (Yocto/packaging) — foundational
WDY-1001 is a prerequisite for WS4 (it establishes the signing keys & provenance).

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1001 | — | Reproducible builds, SPDX SBOM, cosign/SLSA provenance, **agent binary signing** (unblocks WDY-1089), SRC_URI checksum CI gate |
| WDY-997 | — | systemd sandboxing block on every `.service`; per-service cap minimization; `systemd-analyze security ≥7.0` CI gate |
| WDY-841 | — | Go dep audit — mostly monitoring (opencensus, goselect, gogo/protobuf); act when upstreams drop them |

### WS7 — Forensics (cross-cuts WS3/WS4)

| Issue | Sev | Fix |
|-------|-----|-----|
| WDY-1096 | MED | Structured, tamper-evident audit event per state-changing RPC (RunContainer/UpdateOS/UpdateAgent/WiFi), forwarded to remote sink. Land alongside the RPC work |

---

## Phased sequencing

### Phase 0 — Quick wins (low risk, high severity, no app breakage)
Standalone fixes, no new infra, shippable now. Cloud and agent tracks run in parallel.
- Cloud: **WDY-1189, WDY-1190, WDY-1191** (3 small authz diffs)
- Agent: **WDY-1009** (command injection), **WDY-1010** (debugpy), **WDY-1093** (D-Bus hard-fail), **WDY-1102** (containerd socket), **WDY-1006** (pre-provisioning surface)

### Phase 1 — Signing & trust foundation
Build the infrastructure later phases depend on.
- **WDY-1001** signing keys + provenance (prereq for WS4)
- **WDY-1086** pin cloud CA
- Then **WDY-1088 / WDY-1089 / WDY-1090** signature verification
- **WDY-1192 + WDY-1087** revocation (CRL/OCSP — one mechanism serves both image and client-cert revocation)

### Phase 2 — Isolation hardening (needs on-hardware validation; some ⚠️ breaking)
Sequence riskiest last; ship behind validation on Jetson/Pi/Thor.
- Non-breaking first: **WDY-1008** (camera /dev), **WDY-1012** (seccomp), **WDY-1094** (CAP_NET_ADMIN)
- Breaking, needs migration + docs: **WDY-1011** (user namespace), **WDY-1014** (default bridge), **WDY-1101** (cgroup defaults — fold into WDY-1729)

### Phase 3 — Defense-in-depth, forensics, maintenance
- **WDY-997** systemd sandboxing + CI gate
- **WDY-1096** audit logging (pair with the RPC handlers touched in Phase 0/1)
- **WDY-1149** CLI cert pinning, **WDY-1098** BLE replay
- **WDY-841** dependency monitoring (ongoing)

---

## Cross-cutting notes
- **Shared revocation infra**: WDY-1192 (image signing certs) and WDY-1087 (mTLS client certs) should use one CRL/OCSP mechanism — design once.
- **WDY-1006 → WDY-1092**: fix the pre-provisioning surface first, then the token-in-transit protection layers on top.
- **In-flight work to coordinate**: WDY-1729 (resource limits, In Review) overlaps WDY-1101; WDY-1197 (deprecate JSON app_config, In Progress) touches the same path as WDY-1009.
- **Breaking-change discipline**: WDY-1011/1014/1101 change runtime defaults for already-deployed apps — each needs a feature flag or staged rollout + hardware soak before becoming default.
- The threat-model tickets (`[TM-*]`) trace back to `security/THREAT_MODEL.md` and PR #591 — keep that doc updated as controls land.
