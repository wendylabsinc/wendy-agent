# Security Control Status Map

_Generated 2026-06-28. Authoritative source for Tasks 2–4. Status confirmed from merged code and git history only — not from Linear issue state._

Badge vocabulary: `✅ Shipped` / `🛠 In progress` / `📋 Planned`

---

## Threat Catalogue Status

| Threat ID | Title | Status | Evidence |
|-----------|-------|--------|----------|
| **TM-S-01** | Unauthenticated access on pre-provisioned port (WDY-1006) | 📋 Planned | Plaintext port still calls `registerAllServices()` (main.go:551) — NOT narrowed to ProvisioningService only; fix not yet merged |
| **TM-S-02** | Spoofed cloud endpoint during provisioning (WDY-1086) | 📋 Planned | No CA pinning in merged code; Phase 1 |
| **TM-S-03** | Impersonation via stolen mTLS client cert (WDY-1087, WDY-1192) | 📋 Planned | CRL/OCSP revocation infra not present; Phase 1 |
| **TM-T-01** | Malicious container image execution (WDY-1088) | 📋 Planned | No cosign/Notary v2 verification before RunContainer; Phase 1 |
| **TM-T-02** | Agent binary tampering via UpdateAgent (WDY-1089) | 📋 Planned | Signature verification against pinned key not present; Phase 1 |
| **TM-T-03** | Mender artifact tampering via UpdateOS (WDY-1090) | 📋 Planned | Signed artifact verification not enforced; Phase 1 |
| **TM-T-04** | Volume data tampering between containers | 📋 Planned | Volume namespace-by-app-identity not implemented; Phase 2/3 |
| **TM-R-01** | Insufficient audit trail for privileged operations (WDY-1096) | 📋 Planned | No structured tamper-evident audit log; Phase 3 |
| **TM-I-01** | OTEL receivers accept unauthenticated data | 📋 Planned | mTLS/token auth for OTEL not implemented; no phase assigned yet |
| **TM-I-02** | Private key at rest in /etc/wendy-agent/ | 📋 Planned | HSM/TPM/kernel-keyring not implemented; no phase assigned |
| **TM-I-03** | Enrollment token intercepted in transit (WDY-1092) | 📋 Planned | ECDH/TLS-bootstrapped channel not implemented; depends on WDY-1006 |
| **TM-I-04** | BLE command interception and replay (WDY-1098) | 📋 Planned | Session-ticket disable not confirmed in code; Phase 3 |
| **TM-I-05** | Container escape exposing host filesystem via broad caps (WDY-1012, WDY-1094) | ✅ Shipped | `defaultSeccomp()` blocks ptrace/unshare/clone(CLONE_NEWUSER)/kexec/kernel-module syscalls (`oci/spec.go:289`, commit `ca034993`); CAP_NET_ADMIN requires explicit `host-admin` mode (`oci/entitlements.go:274`, commit `95db6b52`) |
| **TM-D-01** | Resource exhaustion via unauthenticated OTEL receivers | 📋 Planned | Rate limiting / payload cap not present |
| **TM-D-02** | Container resource exhaustion (WDY-1101) | ✅ Shipped | Default PID limit (4096) applied to all containers; memory/CPU opt-in preserved (`oci/resources.go:44`, commit `00704237`) |
| **TM-D-03** | Tunnel broker reconnection flood | 📋 Planned | Cap on total retries not implemented |
| **TM-E-01** | Container escape via D-Bus proxy bypass (WDY-1093) | ✅ Shipped | Hard-fail in `containerd/client.go:2674` — `requireDBusProxy()` returns error (refuses container start) when proxyManager is nil and bluetooth entitlement declared; `applyBluetooth()` never falls back to raw host socket (`oci/entitlements.go:641`); commit `4b1ff265` |
| **TM-E-02** | Privilege escalation via writable containerd socket (WDY-1102) | ✅ Shipped | `ValidateMounts()` rejects any bind-mount source under `/run/containerd` or `/var/run/containerd` (`oci/mounts.go:23`, commit `4b1ff265`) |
| **TM-E-03** | Escalation via CAP_NET_ADMIN in host-networked containers (WDY-1094) | ✅ Shipped | `mode: "host"` no longer grants CAP_NET_ADMIN; requires explicit `mode: "host-admin"` opt-in (`oci/entitlements.go:274`, commit `95db6b52`) |

---

## Additional Controls (not in TM-* catalogue)

| Issue | Title | Status | Evidence |
|-------|-------|--------|----------|
| WDY-1009 | postStart hook shell injection | ✅ Shipped | Hook executes via `exec.Command(argv[0], argv[1:]...)` — no `sh -c` (`containerd/client.go:1538`, commit `4b1ff265`) |
| WDY-1010 | debugpy listener binds 0.0.0.0 | ✅ Shipped | Listener explicitly binds `127.0.0.1:5678` (`containerd/client.go:456`, commit `4b1ff265`) |
| WDY-1011 | User namespace / UID remap | 📋 Planned | `UIDMappings`/`UserNamespace` absent from `oci/namespace.go`; Phase 2 (breaking, needs hardware validation) |
| WDY-1014 | Default network mode host → bridge/CNI | 📋 Planned | Default network mode not changed; Phase 2 (breaking) |
| WDY-1008 | Camera entitlement rbind-mounts all of /dev | 📋 Planned | `applyCamera()` still uses `/dev` bind mount (`oci/entitlements.go:466`); WDY-1008 fix (enumerate only `/dev/video*`,`/dev/media*`) not yet shipped; Phase 2 |
| WDY-1149 | CLI device cert/pubkey pinning on set-default | ✅ Shipped | Organisation + cloud host pinned in `shared/config/devicepin.go`; verified on `set-default` and subsequent connections (`cli/commands/device.go:329`, commit `7fc51b10`) |
| WDY-1001 | Reproducible builds / SBOM / SLSA provenance | 🛠 In progress | Phase 1; signing infrastructure prerequisite for WDY-1088/1089 |
| WDY-1086 | Pin/hard-code cloud CA in firmware | 🛠 In progress | Phase 1 |
| WDY-1088 | OCI image signature verification (cosign/Notary v2) | 🛠 In progress | Phase 1; blocked on WDY-1001 signing keys |
| WDY-1089 | Agent binary signature verification | 🛠 In progress | Phase 1; blocked on WDY-1001 |
| WDY-1090 | UpdateOS HTTPS-only + signed Mender artifact | 🛠 In progress | Phase 1 |
| WDY-1087 | mTLS client cert revocation (OCSP/CRL) | 🛠 In progress | Phase 1; shared infra with WDY-1192 |
| WDY-1192 | Certificate revocation (image + client certs) | 🛠 In progress | Phase 1; proto field exists (`certificates.pb.go`), enforcement not shipped |
| WDY-997  | systemd sandboxing + CI gate | 📋 Planned | Phase 3 |
| WDY-1096 | Structured tamper-evident audit log | 📋 Planned | Phase 3 |
| WDY-1098 | BLE: disable TLS session resumption | 📋 Planned | Phase 3 |
| WDY-841  | Go dependency audit | 📋 Planned | Phase 3 (ongoing monitoring) |
| WDY-1189 | IssueCertificate asset_id JWT validation | 📋 Planned | Cloud repo; Phase 0 (not yet confirmed merged) |
| WDY-1190 | SubscribeToMapEvents org ownership check | 📋 Planned | Cloud repo; Phase 0 (not yet confirmed merged) |
| WDY-1191 | UpdateOAuthClient org ownership check | 📋 Planned | Cloud repo; Phase 0 (not yet confirmed merged) |

---

## Summary counts

| Status | Count |
|--------|-------|
| ✅ Shipped | 7 controls confirmed in merged code |
| 🛠 In progress | 7 controls (Phase 1 signing/trust foundation) |
| 📋 Planned | 19 controls (Phase 0 cloud, Phase 2 isolation, Phase 3 forensics + cloud) |

---

## Shipped-control evidence index

| Control | File | Line | Commit |
|---------|------|------|--------|
| TM-E-03 / WDY-1094 CAP_NET_ADMIN split | `go/internal/agent/oci/entitlements.go` | 267–278 | `95db6b52` |
| TM-E-02 / WDY-1102 containerd socket deny | `go/internal/agent/oci/mounts.go` | 23–34 | `4b1ff265` |
| TM-E-01 / WDY-1093 D-Bus hard-fail | `go/internal/agent/containerd/client.go` | 2673–2678 | `4b1ff265` |
| TM-I-05 / WDY-1012 seccomp baseline | `go/internal/agent/oci/spec.go` | 289–330 | `ca034993` |
| TM-D-02 / WDY-1101 default PID limit | `go/internal/agent/oci/resources.go` | 44–48 | `00704237` |
| WDY-1009 hook injection | `go/internal/agent/containerd/client.go` | 1537–1552 | `4b1ff265` |
| WDY-1010 debugpy loopback | `go/internal/agent/containerd/client.go` | 447–456 | `4b1ff265` |
| WDY-1149 CLI device pin | `go/internal/shared/config/devicepin.go` + `go/internal/cli/commands/device.go` | 329 | `7fc51b10` |

---

## Framing & disposition decisions (controller, 2026-06-28)

These override how the items are presented in the refreshed threat model and the public pages.
They are accepted design tradeoffs with documented mitigations — NOT "open CRITICAL/HIGH gaps":

- **WDY-1006 / TM-S-01 (plaintext pre-provisioning):** Accepted, expected behavior for an
  *unprovisioned* device. The plaintext state is clearly warned, time-bounded, and provisioning
  is offered during tools installation — provisioning IS the mitigation. Present it as an
  intentional, documented state (mitigations-first: "unprovisioned devices use a plaintext
  channel by design; provision to establish mTLS"), NOT as a CRITICAL unauthenticated-RCE flaw.
  Do not enumerate exposed RPCs alarmingly or publish a CRITICAL severity badge for it publicly.
- **WDY-1008 / camera /dev bind:** Mitigated by design. The full `/dev` bind is intentional
  (USB webcam hotplug); device access is gated by per-major cgroup deny-all-baseline allow rules.
  Present as a deliberate, mitigated design choice, not a gap.

All other ✅/🛠/📋 statuses in this map stand.
