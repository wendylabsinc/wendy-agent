# Move `install.wendy.dev` install scripts to Google Cloud

**Date:** 2026-07-17
**Status:** Design (approved for planning)
**Branch:** `jo/install-scripts-gcs`

## Problem

The public install one-liners

```bash
curl -fsSL https://install.wendy.dev/cli.sh   | bash
curl -fsSL https://install.wendy.dev/agent.sh | bash
```

return `403` when many people install simultaneously from a single shared IP
(e.g. a workshop behind one NAT). There are **two** distinct GitHub touchpoints,
both rate-limited per IP:

1. **Fetching the script itself.** `install.wendy.dev` is a GitHub Pages custom
   domain. The `.github/workflows/static.yml` workflow publishes the entire
   `docs/` tree (a repo-root symlink to `go/internal/cli/assets/docs/`, which
   contains `agent.sh` and `cli.sh`) to GitHub Pages. GitHub Pages soft-throttles
   requests from one IP and returns `403` under workshop load. **This is the
   reported failure.**

2. **Version resolution inside the scripts.** Both scripts resolve the latest
   release via `curl https://api.github.com/repos/wendylabsinc/wendy-agent/releases/latest`,
   which GitHub caps at **60 unauthenticated requests/hour per IP**. Critically,
   `cli.sh` calls `resolve_version` **unconditionally near the top**
   (`go/internal/cli/assets/docs/cli.sh:155`), so *every* CLI install — including
   the mainstream Homebrew and apt/dnf/yum paths — hits `api.github.com`. A room
   of >60 people trips this even after the script itself is served from a CDN.
   `agent.sh` only calls it on the no-package-manager / no-Homebrew fallback
   paths.

## Goal

Host the two install scripts on Google Cloud and make the entire install flow
GitHub-free on the mainstream paths, while keeping the public URL
`https://install.wendy.dev/{agent,cli}.sh` **byte-for-byte identical** so no
documentation, README, release note, or user muscle memory changes.

## Existing infrastructure we build on

The "serve static files from GCS behind an HTTPS load balancer" pattern already
exists twice in this repo:

- **`docs.wendy.dev`** is served from the public bucket `gs://wendy-docs-public`
  through a global external HTTP(S) load balancer that terminates TLS and adds
  security headers (`go/internal/cli/assets/docs/development/docs-site.md`,
  `.github/workflows/fumadocs.yml`). It authenticates to GCP with Workload
  Identity Federation (`vars.GCP_WORKLOAD_IDENTITY_PROVIDER`,
  `vars.GCP_SERVICE_ACCOUNT`, `vars.GCP_PROJECT_ID`).
- **`gs://wendyos-images-public`** (served at
  `https://storage.googleapis.com/wendyos-images-public`) already hosts
  wendy-agent linux tarballs and an `agent/manifest.json`
  (`.github/scripts/publish-agent-gcs.sh`, `build.yml` job `publish-agent-gcs`).
  The Go CLI already resolves the agent GCS-first with GitHub fallback
  (`go/internal/cli/commands/agent_source.go`; base URL constant
  `gcsBaseURL = "https://storage.googleapis.com/wendyos-images-public"` in
  `manifest.go:12`).

## Design

### A. Serve the scripts from GCS, keep `install.wendy.dev`

1. **Bucket.** New public bucket **`wendy-install-public`** with the scripts at
   the bucket root: `agent.sh`, `cli.sh` (plus `manifest.json`, see §B). Root
   objects map cleanly to the bare paths `install.wendy.dev/agent.sh`.
   - Uploaded with `Content-Type: text/x-shellscript`.
   - `Cache-Control: public, max-age=300` so script/manifest updates propagate
     within minutes.
   - Public read (uniform bucket-level access + `allUsers:objectViewer`), same
     posture as `wendy-docs-public`.

2. **CI publish step.** A new job/step in `.github/workflows/build.yml` (adjacent
   to `publish-agent-gcs`, reusing the same WIF auth + Cloud SDK setup) uploads
   `go/internal/cli/assets/docs/{agent,cli}.sh` and the generated
   `manifest.json` to `gs://wendy-install-public/` on `main` push and on
   release. A backing script under `.github/scripts/` (mirroring
   `publish-agent-gcs.sh`) does the upload + sets content-type/cache-control.
   Only the two scripts and the manifest are uploaded — release binaries are not
   mirrored (see §B.4).

3. **Load balancer.** Add `install.wendy.dev` to the **existing docs HTTPS load
   balancer**:
   - New backend bucket pointing at `wendy-install-public`.
   - URL-map host rule: `install.wendy.dev` → that backend bucket.
   - Add `install.wendy.dev` to the managed SSL certificate (or attach a new
     managed cert to the existing target proxy).

4. **DNS cutover.** Repoint `install.wendy.dev` from the GitHub Pages target
   (`wendylabsinc.github.io`) to the load balancer's global anycast IP
   (A/AAAA). Remove the GitHub Pages custom-domain binding.

5. **Retire GitHub Pages.** Delete `.github/workflows/static.yml`. Its only
   consumer is `install.wendy.dev`; `docs.wendy.dev` is already served from GCS.

**Division of labor.** All repo changes (CI publish script + job, script edits,
`static.yml` removal, docs note) are made in this branch. The load-balancer,
managed-certificate, and DNS steps are **executed by the maintainer** using an
exact `gcloud` + DNS runbook included in the implementation plan — the coding
environment has no credentials for the GCP project or DNS zone.

### B. Make the scripts GitHub-free

1. **Install manifest.** Publish a small `manifest.json` to
   `gs://wendy-install-public/manifest.json` in the same CI step:

   ```json
   { "latest": "0.19.0", "latest_nightly": "0.20.0-nightly.20260717" }
   ```

   CLI and agent share a single release version (both built from one tag by the
   release workflow), so one manifest serves both scripts. The version string
   format is reconciled with the scripts' existing `TAG`/`VERSION` handling
   (`VERSION="${TAG#v}"`); the plan pins whether the manifest stores `v`-prefixed
   tags or bare versions and adjusts the scripts accordingly.

2. **`resolve_version` rewrite (both scripts).** Try
   `https://install.wendy.dev/manifest.json` first; fall back to
   `api.github.com/.../releases/latest` only on miss/parse-failure. `WENDY_VERSION`
   override behavior is unchanged.

3. **`cli.sh` — stop the unconditional GitHub call.** Move the `resolve_version`
   invocation out of the top-level flow and into only the branches that actually
   need a version tag (the darwin-no-Homebrew tarball, the linux-no-package-
   manager tarball, and the Windows zip paths). The Homebrew / apt / dnf / yum /
   pacman paths then make **zero** GitHub requests.

4. **Binary-download fallbacks stay on GitHub (deferred).** Mirroring release
   binaries to GCS is **out of scope** for this change. The fallback paths
   (macOS without Homebrew, Linux without a package manager, Windows) continue to
   download release assets from `github.com/.../releases/download/...`. This is
   acceptable because:
   - Release *asset downloads* are **not** subject to the 60-req/hr
     `api.github.com` limit that causes the workshop 403; that limit applies to
     the REST API call in `resolve_version`, which §B.2 moves to GCS.
   - These are tail paths — the mainstream flows (Homebrew CDN; apt/yum via
     Google Artifact Registry at `us-central1-{apt,yum}.pkg.dev`) already never
     touch GitHub at all.

   A follow-up can mirror CLI/Windows/macOS-agent binaries to GCS if the fallback
   paths later prove to matter at workshop scale.

## Testing

- **Static analysis:** `shellcheck` on both scripts.
- **Local harness:** run each script with `brew`/`apt-get`/`curl`/`wget` stubbed
  on `PATH` and a fake manifest/download server; assert:
  - no `api.github.com` or `github.com` calls on the Homebrew/apt/dnf/yum paths,
  - version resolution reads the GCS manifest and falls back to GitHub when it is
    missing,
  - fallback binary downloads target the GCS URLs and fall back to GitHub URLs.
- **CI health check:** after upload, `curl -fsSL https://install.wendy.dev/agent.sh | head`
  (and `cli.sh`, `manifest.json`) — mirrors the existing docs-deploy smoke check.
- **Manual post-cutover:** confirm `install.wendy.dev/agent.sh` is served by the
  load balancer (response headers / IP), and both one-liners still install
  end-to-end.

## Scope boundaries

- **In scope:** dedicated `wendy-install-public` bucket + CI publish of the two
  scripts and the install manifest; GitHub-free `resolve_version`; `cli.sh` no
  longer calling GitHub on mainstream paths; removal of `static.yml`; a
  maintainer runbook for LB/cert/DNS.
- **Out of scope / maintainer-executed:** the actual load-balancer, managed-cert,
  and DNS changes (repo cannot reach GCP/DNS). Mirroring release binaries to GCS
  (§B.4) — fallback paths keep downloading assets from GitHub. No changes to
  package-manager install logic beyond de-GitHub-ing version resolution. No new
  hostname — `install.wendy.dev` is preserved.

## Risks & mitigations

- **DNS cutover downtime.** Provision the LB backend + cert and confirm it serves
  the scripts on a temporary hostname/IP *before* repointing DNS; keep Pages live
  until the LB is verified.
- **Managed cert provisioning delay.** Google-managed certs can take minutes-to-
  tens-of-minutes to become ACTIVE; runbook sequences cert-first, DNS-last.
- **Manifest/version drift.** Short `max-age=300` cache; CI republishes manifest
  every release; `WENDY_VERSION` and GitHub fallback remain as escape hatches.
- **Public bucket hygiene.** Only the three intended objects are world-readable;
  uniform bucket-level access; no listing exposure needed (objects fetched by
  exact path).
