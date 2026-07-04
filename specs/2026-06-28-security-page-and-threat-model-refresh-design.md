# Security Page Rebuild + Threat Model Refresh — Design

_Created 2026-06-28._

## Goal

Make the public security page convince a **technical buyer / CISO** audience that WendyOS is
as secure as it can credibly be. For this audience, superlatives ("most secure OS") read as
marketing and invite teardown. What converts skeptical security buyers is **demonstrated rigor
plus radical transparency**: a published threat model, control-by-control status, and visible
proof-points (disclosure policy, signed releases, SBOM, security changelog).

The persuasive message is: _"We threat-modeled ourselves harder than you will, we publish it,
and here's exactly what's shipped and what's next."_

This is two coupled deliverables. Deliverable A (threat model refresh) is the factual
foundation for Deliverable B (the page) — do A first.

## Source of truth for status

**Shipped code + git history is authoritative, NOT Linear.** Linear states are stale/misleading
here — e.g. WDY-1006/1008/1092 show "Canceled" but the fixes actually merged (Phase 0 / oci-isolation
commits). Every status claim in both deliverables must be verified against merged code, not ticket state.

### Known-shipped (verify each against code before marking ✅)

| Control | Threat | Evidence (commit/PR) |
|---------|--------|----------------------|
| D-Bus hard-fail when `xdg-dbus-proxy` absent | TM-E-01 | WDY-1093 (Phase 0 hardening) |
| Deny `/run/containerd/*` bind mounts | TM-E-02 | WDY-1102 (Phase 0 hardening) |
| `CAP_NET_ADMIN` split from host networking | TM-E-03 | WDY-1094 |
| Baseline seccomp profile | TM-I-05 (partial) | WDY-1012 |
| Default CPU/mem/PID cgroup limits | TM-D-02 | WDY-1101 |
| Hook command-injection fix (drop `sh -c`) | (relates TM-*) | WDY-1009 |
| debugpy bound to loopback + gated | — | WDY-1010 |
| CLI pins device org + cloud host on set-default | TM-S (CLI trust) | WDY-1149 |
| Pre-provisioning surface reduced | TM-S-01 / TM-I-03 | WDY-1006 |
| Foxglove DoS hardening | — | foxglove serve PR |

### Known-open (keep honest; map to remediation phases)

- Image signature verification — TM-T-01 (WDY-1088)
- Agent binary signing — TM-T-02 (WDY-1089), depends on WDY-1001
- OS/Mender artifact signing — TM-T-03 (WDY-1090)
- Cloud API authz — WS1 (WDY-1189/1190/1191)
- Cert/image revocation (CRL/OCSP) — TM-S-03 (WDY-1087/1192)
- Cloud CA pinning in firmware — TM-S-02 (WDY-1086)
- Container user namespaces / UID remap — TM-T-04, WDY-1011
- Default bridge networking — WDY-1014
- Audit logging — TM-R-01 (WDY-1096)
- BLE replay / session-resumption hardening — TM-I-04 (WDY-1098)
- Reproducible builds / SBOM / SLSA provenance / signing keys — WDY-1001
- systemd sandboxing + CI gate — WDY-997

## Decisions (locked with user)

- **Audience:** Technical buyers / CISOs.
- **Transparency:** Maximum — publish threat model + architecture/controls deep dive + trust/compliance layer.
- **Open items:** Show per-control status with **target quarters** (not exact months, not date-free).
- **Threat model surfacing:** **Dedicated public docs page** adapted from `THREAT_MODEL.md`, linked from the security page.

## Deliverable A — Refresh `security/THREAT_MODEL.md`

It is dated 2026-05-01 and predates the merged fixes. Work:

1. Bump version (1.0 → 1.1) and date (2026-06-28); add a **changelog** section noting what moved to mitigated.
2. For each shipped control above: move it from "Recommended controls" → "Existing mitigations" and add a
   **Status** line to the threat (`✅ Mitigated` / `🛠 In progress` / `📋 Planned`).
3. Keep open items honest — do not overstate. Open HIGH/CRITICAL items remain clearly marked.
4. Re-verify the "Network Ports" table against current agent behavior (port 50051 gating changed with WDY-1006).
5. Update the Review Schedule "last reviewed" stamp.

This stays an internal-canonical artifact; the public page derives a polished subset from it.

## Deliverable B — Public security page (`docs/content/docs/security.mdx`)

Layered, depth-increasing structure:

1. **Posture statement** — no superlatives. "Security-first edge OS: mTLS everywhere, post-quantum
   data-in-transit, zero-trust cloud enrollment, sandboxed apps with explicit entitlements, and a
   published threat model."
2. **Security architecture** — trust-boundary diagram, unprovisioned→mTLS transition, container
   isolation + entitlement model, signed-update direction. Adapted from the threat model's
   architecture section.
3. **Control-status table** — every control as ✅ shipped / 🛠 in progress / 📋 planned, with a
   **target quarter** for non-shipped items. Derived from the refreshed threat model + remediation
   phases (Phase 0 done; Phase 1 signing/trust; Phase 2 isolation; Phase 3 forensics).
4. **Link to the public Threat Model page** (Deliverable C).
5. **Trust & compliance proof-points** — vulnerability disclosure policy, `security.txt`, SBOM +
   signed releases (as they land, tied to WDY-1001), and a **security changelog** listing shipped
   fixes (the merged WDY-* work is concrete evidence).
6. Keep the existing **Reporting Security Issues** section (update email to `security@wendy.dev` —
   already done) and **Stay Updated** links.

## Deliverable C — Public Threat Model docs page

New page under `docs/content/docs/` (e.g. `security/threat-model.mdx`), adapted from the refreshed
`THREAT_MODEL.md`:
- Architecture + trust boundaries + assets.
- STRIDE summary with **mitigations-first framing** (lead with the existing control, then note
  hardening underway) and per-threat status badges.
- Linked from the security page's section 4.

## Trust & compliance proof-points (page section 5 detail)

- **Vulnerability disclosure policy** — content (responsible disclosure, scope, response SLA). Content-only.
- **`security.txt`** — `/.well-known/security.txt` on the marketing/docs site. Needs site config; flag as such.
- **SBOM + signed releases** — depends on WDY-1001 infra; present as 🛠/📋 with target quarter, do not claim shipped.
- **Security changelog** — list of merged security fixes; pure content, high-credibility, do now.

## Sequence

1. Verify each known-shipped control against merged code; finalize status map.
2. Refresh `THREAT_MODEL.md` (Deliverable A).
3. Write the public Threat Model page (Deliverable C).
4. Rebuild `security.mdx` with the layered structure + control-status table (Deliverable B).
5. Add proof-point content (disclosure policy + security changelog now; SBOM/security.txt flagged as infra-dependent).

## Out of scope

- Implementing any of the open security controls themselves (that's the remediation plan).
- Standing up SBOM/signing infra (WDY-1001) — the page only references it with honest status.
- Cloud backend threat model (separate doc).

## Success criteria

- A CISO can, from the page alone: understand the architecture and trust boundaries, see exactly
  which controls are shipped vs. in-progress with timelines, read the full threat model, and find
  a disclosure channel + evidence of shipped fixes.
- Zero unsupported superlatives. Every claim traceable to shipped code or an honest status badge.
- `THREAT_MODEL.md` accurately reflects merged code as of 2026-06-28.
