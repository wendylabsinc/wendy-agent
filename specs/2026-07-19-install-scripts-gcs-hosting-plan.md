# Move install.wendy.dev scripts to Google Cloud — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve the `agent.sh`/`cli.sh` install scripts (and a version manifest) from a dedicated public GCS bucket behind the existing docs HTTPS load balancer, keeping `install.wendy.dev` unchanged, and make script version-resolution GitHub-free so a workshop sharing one IP no longer hits GitHub 403s.

**Architecture:** A new CI job uploads the two scripts plus a small `manifest.json` (`{latest, latest_nightly}`) to `gs://wendy-install-public`. Both scripts gain a GitHub-free `resolve_version` that reads `https://install.wendy.dev/manifest.json` first and falls back to the GitHub API. `cli.sh` stops resolving a version on the mainstream Homebrew/apt paths. The GitHub Pages workflow (`static.yml`) is retired. The load-balancer / managed-cert / DNS cutover is documented as a maintainer runbook (no GCP or DNS credentials exist in the build environment).

**Tech Stack:** Bash (POSIX-ish, `set -euo pipefail`), GitHub Actions, Google Cloud Storage + external HTTPS load balancer, `jq`, `gcloud`, `shellcheck`.

## Global Constraints

- The public install commands MUST stay byte-for-byte identical: `curl -fsSL https://install.wendy.dev/cli.sh | bash` and `curl -fsSL https://install.wendy.dev/agent.sh | bash`. Do not rename the scripts, the host, or the paths.
- Scripts are consumed via `curl | bash`; they must remain **standalone** (no `source`ing external files at runtime) and run under `bash` (they re-exec under bash if started by `sh`).
- Scripts may only assume `curl` **or** `wget`, plus coreutils; never assume `jq` is present on the end-user machine.
- Version string format (from `build.yml` `determine-version`): a bare timestamp like `2026.07.19-143000` (no `v` prefix); `tag_name == version`. Release artifacts are named `wendy-{cli,agent}-<os>-<arch>-<version>.tar.gz` / `.zip`.
- GCS bucket: `wendy-install-public`. GCS base for the manifest as fetched by scripts: `https://install.wendy.dev/manifest.json`. Agent-artifact GCS base already in use: `https://storage.googleapis.com/wendyos-images-public`.
- CI auth reuses the existing Workload Identity Federation variables: `vars.GCP_WORKLOAD_IDENTITY_PROVIDER`, `vars.GCP_SERVICE_ACCOUNT`, `vars.GCP_PROJECT_ID`. Pin any new actions to the same commit SHAs already used elsewhere in `build.yml`.
- Fallback binary downloads stay on GitHub (out of scope to mirror). Only scripts + manifest go to GCS.
- The scripts and helper scripts live at `go/internal/cli/assets/docs/{agent,cli}.sh` (the repo-root `docs/` is a symlink to `go/internal/cli/assets/docs`). These two `.sh` files are **not** `go:embed`ed, so edits do not affect the embedded CLI assets.
- Commit after every task. Use short, conventional-commit messages.

---

## File Structure

- `go/internal/cli/assets/docs/cli.sh` — MODIFY: GitHub-free shared resolver block; defer `resolve_version` off the mainstream paths.
- `go/internal/cli/assets/docs/agent.sh` — MODIFY: identical GitHub-free shared resolver block.
- `.github/scripts/publish-install-scripts.sh` — CREATE: uploads the two scripts + `manifest.json` to the bucket.
- `.github/scripts/install-manifest-merge.jq` — CREATE: read-modify-write splice of `latest`/`latest_nightly`.
- `.github/scripts/install-manifest-merge_test.sh` — CREATE: unit tests for the jq filter.
- `.github/scripts/install-scripts_test.sh` — CREATE: tests for the shared resolver block + `cli.sh` deferral.
- `.github/workflows/build.yml` — MODIFY: add the `publish-install-scripts` job.
- `.github/workflows/static.yml` — DELETE: retire GitHub Pages.
- `.github/workflows/go-tests.yml` — MODIFY: run the two shell test scripts + shellcheck in CI.
- `go/internal/cli/assets/docs/development/install-hosting.md` — CREATE: hosting doc + maintainer LB/cert/DNS runbook.

---

## Task 1: GitHub-free shared resolver block in both scripts

Introduce one identical, marker-delimited block in `cli.sh` and `agent.sh` that resolves the latest version from the GCS manifest first and falls back to the GitHub API. The block also carries the shared `download()` helper. Both scripts must contain the **byte-identical** block (a test enforces this).

**Files:**
- Modify: `go/internal/cli/assets/docs/cli.sh` (replaces the existing `resolve_version` + `download` region, currently lines 62–89)
- Modify: `go/internal/cli/assets/docs/agent.sh` (replaces the existing `resolve_version` + `download` region, currently lines 74–101)
- Test: `.github/scripts/install-scripts_test.sh` (create)

**Interfaces:**
- Produces (available to the rest of each script, unchanged call sites):
  - `resolve_version() -> stdout: version string` — honors `WENDY_VERSION`; else GCS manifest `.latest`; else GitHub API `tag_name`.
  - `download <url> <dest>` — writes URL to file via curl or wget.
- Consumes: `REPO` (defined at the top of each script as `wendylabsinc/wendy-agent`).

- [ ] **Step 1: Write the failing test**

Create `.github/scripts/install-scripts_test.sh`:

```bash
#!/usr/bin/env bash
# .github/scripts/install-scripts_test.sh
# Tests the shared resolver block (Task 1) and cli.sh deferral (Task 2).
set -euo pipefail
cd "$(dirname "$0")"

REPO_ROOT="$(cd ../.. && pwd)"
CLI="${REPO_ROOT}/go/internal/cli/assets/docs/cli.sh"
AGENT="${REPO_ROOT}/go/internal/cli/assets/docs/agent.sh"
BEGIN='# >>> wendy-install-shared'
END='# <<< wendy-install-shared'

fail=0
check() { if [ "$2" != "$3" ]; then echo "FAIL $1: expected [$2] got [$3]"; fail=1; else echo "ok $1"; fi; }
contains() { case "$2" in *"$3"*) echo "ok $1";; *) echo "FAIL $1: [$2] does not contain [$3]"; fail=1;; esac; }
absent()  { case "$2" in *"$3"*) echo "FAIL $1: [$2] unexpectedly contains [$3]"; fail=1;; *) echo "ok $1";; esac; }

# Extract the marked block from a script (exclusive of the marker lines).
extract_block() { awk "/${BEGIN}/{f=1;next} /${END}/{f=0} f" "$1"; }

# --- Test A: both scripts carry a byte-identical shared block ---
cli_block="$(extract_block "$CLI")"
agent_block="$(extract_block "$AGENT")"
check "block.nonempty" "yes" "$([ -n "$cli_block" ] && echo yes || echo no)"
check "block.identical" "yes" "$([ "$cli_block" = "$agent_block" ] && echo yes || echo no)"

# --- Harness: fake curl/wget servable from a table of url->file, logging calls ---
setup_net() { # $1 = dir with manifest.json / github.json (optional)
  BIN="$(mktemp -d)"; REQ_LOG="$(mktemp)"; SERVE_DIR="$1"
  cat > "$BIN/curl" <<EOF
#!/usr/bin/env bash
url=""; out=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -o) out="\$2"; shift 2;;
    http*|https*) url="\$1"; shift;;
    *) shift;;
  esac
done
echo "\$url" >> "$REQ_LOG"
case "\$url" in
  *install.wendy.dev/manifest.json) src="$SERVE_DIR/manifest.json";;
  *api.github.com/*) src="$SERVE_DIR/github.json";;
  *) src="";;
esac
[ -n "\$src" ] && [ -f "\$src" ] || exit 22   # mimic curl -f on missing/non-2xx
if [ -n "\$out" ]; then cat "\$src" > "\$out"; else cat "\$src"; fi
EOF
  cp "$BIN/curl" "$BIN/wget" 2>/dev/null || true  # not used, but present
  chmod +x "$BIN/curl" "$BIN/wget"
}

# Build a script that sources ONLY the shared block, then calls resolve_version.
run_resolver() { # env: WENDY_VERSION optional
  local tmp; tmp="$(mktemp)"
  # Mirror the real scripts' shell options so the test catches errexit bugs
  # (a failing command substitution under `set -e` must NOT abort the fallback).
  { echo 'set -euo pipefail'; echo 'REPO="wendylabsinc/wendy-agent"'; extract_block "$CLI"; echo 'resolve_version'; } > "$tmp"
  PATH="$BIN:$PATH" bash "$tmp"
}

# --- Test B: WENDY_VERSION override wins ---
D="$(mktemp -d)"; setup_net "$D"
printf '{"latest":"2026.01.01-000000"}\n' > "$D/manifest.json"
export WENDY_VERSION=9.9.9
out="$(run_resolver)"
unset WENDY_VERSION
check "resolve.override" "9.9.9" "$out"
absent "resolve.override.no_net" "$(cat "$REQ_LOG")" "manifest.json"

# --- Test C: GCS manifest latest is preferred ---
D="$(mktemp -d)"; setup_net "$D"
printf '{"latest":"2026.07.19-143000","latest_nightly":"2026.07.20-010101"}\n' > "$D/manifest.json"
printf '{"tag_name":"2000.00.00-000000"}\n' > "$D/github.json"
out="$(run_resolver)"
check "resolve.gcs" "2026.07.19-143000" "$out"
contains "resolve.gcs.hit_manifest" "$(cat "$REQ_LOG")" "install.wendy.dev/manifest.json"

# --- Test D: falls back to GitHub when manifest is missing ---
D="$(mktemp -d)"; setup_net "$D"    # no manifest.json in dir
printf '{"tag_name":"2026.07.18-120000"}\n' > "$D/github.json"
out="$(run_resolver)"
check "resolve.fallback" "2026.07.18-120000" "$out"
contains "resolve.fallback.hit_github" "$(cat "$REQ_LOG")" "api.github.com"

exit $fail
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash .github/scripts/install-scripts_test.sh`
Expected: FAIL — `block.nonempty` fails (markers not present yet), and `resolve.gcs` fails because the current `resolve_version` never reads the manifest.

- [ ] **Step 3: Add the shared block to `cli.sh`**

In `go/internal/cli/assets/docs/cli.sh`, replace the existing region (the `# --- Resolve latest release tag ---` comment through the end of the `download()` function — current lines 62–89) with exactly:

```bash
# >>> wendy-install-shared
# Shared installer helpers. This block MUST be byte-identical in cli.sh and
# agent.sh (enforced by .github/scripts/install-scripts_test.sh). It resolves
# the latest version from the GCS-hosted manifest first, so the mainstream
# install paths never call the rate-limited GitHub API.
MANIFEST_URL="https://install.wendy.dev/manifest.json"

# Fetch a raw URL to stdout using curl or wget.
fetch_stdout() {
  local url="$1"
  if command -v curl &>/dev/null; then
    curl -fsSL "$url"
  elif command -v wget &>/dev/null; then
    wget -qO- "$url"
  else
    return 1
  fi
}

# Print the manifest's stable "latest" version, or nothing on any failure.
# Matches the "latest" key only (not "latest_nightly").
manifest_latest() {
  fetch_stdout "$MANIFEST_URL" 2>/dev/null \
    | grep -oE '"latest"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | head -1 \
    | sed -E 's/.*"([^"]*)"$/\1/'
}

# Print the newest GitHub release tag, or nothing on failure.
github_latest() {
  fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
}

# Resolve the version to install: explicit override, else GCS manifest, else GitHub.
resolve_version() {
  if [[ -n "${WENDY_VERSION:-}" ]]; then
    echo "$WENDY_VERSION"
    return
  fi
  # `|| true` keeps a failed fetch (e.g. missing manifest) from tripping the
  # script's `set -e` inside the command substitution, so we can fall through.
  local v
  v="$(manifest_latest || true)"
  if [[ -n "$v" ]]; then
    echo "$v"
    return
  fi
  v="$(github_latest || true)"
  if [[ -n "$v" ]]; then
    echo "$v"
    return
  fi
  echo "Error: could not resolve the latest version from GCS or GitHub." >&2
  return 1
}

# --- Download helper ---
download() {
  local url="$1" dest="$2"
  if command -v curl &>/dev/null; then
    curl -fsSL -o "$dest" "$url"
  elif command -v wget &>/dev/null; then
    wget -qO "$dest" "$url"
  fi
}
# <<< wendy-install-shared
```

- [ ] **Step 4: Add the byte-identical block to `agent.sh`**

In `go/internal/cli/assets/docs/agent.sh`, replace the existing region (the `# --- Resolve latest release tag ---` comment through the end of the `download()` function — current lines 74–101) with the **exact same** block from Step 3 (copy it verbatim, including the `# >>> wendy-install-shared` / `# <<< wendy-install-shared` markers). Do not alter a single character — the test compares the two blocks for equality.

- [ ] **Step 5: Run the test to verify it passes**

Run: `bash .github/scripts/install-scripts_test.sh`
Expected: PASS — all `block.*` and `resolve.*` checks print `ok`.

- [ ] **Step 6: Lint both scripts**

Run: `shellcheck go/internal/cli/assets/docs/cli.sh go/internal/cli/assets/docs/agent.sh`
Expected: no errors. (If shellcheck flags pre-existing unrelated warnings, do not fix them here; ensure no **new** warnings from the added block.)

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/assets/docs/cli.sh go/internal/cli/assets/docs/agent.sh .github/scripts/install-scripts_test.sh
git commit -m "feat(install): resolve version from GCS manifest, GitHub fallback"
```

---

## Task 2: Stop `cli.sh` resolving a version on mainstream paths

Today `cli.sh` calls `resolve_version` unconditionally near the top, so every CLI install — including Homebrew and apt — hits GitHub. Move the call into only the fallback branches that actually download a tarball/zip.

**Files:**
- Modify: `go/internal/cli/assets/docs/cli.sh` (remove top-level resolution ~lines 155–172; add resolution inside the three fallback branches)
- Test: `.github/scripts/install-scripts_test.sh` (append a deferral test)

**Interfaces:**
- Consumes: `resolve_version` (Task 1).
- Produces: no new symbols; `TAG`/`VERSION` become branch-local to the fallback paths.

- [ ] **Step 1: Append the failing deferral test**

Append to `.github/scripts/install-scripts_test.sh`, immediately before the final `exit $fail` line:

```bash
# --- Test E: cli.sh Homebrew path makes zero GitHub/manifest calls (deferral) ---
D="$(mktemp -d)"; setup_net "$D"           # curl fails on every URL and logs it
printf '{"latest":"2026.07.19-143000"}\n' > "$D/manifest.json"
STUB="$(mktemp -d)"
# uname stub: pretend Apple Silicon macOS so the darwin/brew branch is taken.
cat > "$STUB/uname" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  -s) echo "Darwin";;
  -m) echo "arm64";;
  *) echo "Darwin";;
esac
EOF
# brew stub: present, but "brew help trust" fails so the trust steps are skipped;
# every other subcommand is a successful no-op.
cat > "$STUB/brew" <<'EOF'
#!/usr/bin/env bash
[ "$1" = "help" ] && exit 1
exit 0
EOF
chmod +x "$STUB/uname" "$STUB/brew"
: > "$REQ_LOG"
PATH="$STUB:$BIN:$PATH" bash "$CLI" -y >/dev/null 2>&1 || true
absent "defer.no_github"   "$(cat "$REQ_LOG")" "api.github.com"
absent "defer.no_manifest" "$(cat "$REQ_LOG")" "install.wendy.dev/manifest.json"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash .github/scripts/install-scripts_test.sh`
Expected: FAIL on `defer.no_manifest` (and possibly `defer.no_github`) — the current top-level `resolve_version` calls the manifest/GitHub before the brew branch.

- [ ] **Step 3: Remove the top-level version resolution**

In `go/internal/cli/assets/docs/cli.sh`, delete these lines (currently 155–172), i.e. the block from `TAG=$(resolve_version)` through the `echo "Version:  ${TAG}"` line:

```bash
TAG=$(resolve_version)
if [[ -z "$TAG" ]]; then
  echo "Error: Could not determine latest version."
  exit 1
fi

# Strip leading 'v' for the version used in artifact filenames.
VERSION="${TAG#v}"

# --- Determine sudo prefix for Linux (macOS uses sudo selectively, Windows doesn't need it) ---
SUDO=""
if [[ "$OS" == "linux" && "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

echo "Detected: OS=${OS} Arch=${ARCH}"
echo "Version:  ${TAG}"
echo ""
```

Replace it with (keep the SUDO setup and the OS/Arch banner, drop the version banner):

```bash
# --- Determine sudo prefix for Linux (macOS uses sudo selectively, Windows doesn't need it) ---
SUDO=""
if [[ "$OS" == "linux" && "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

# resolve_and_set_version populates TAG and VERSION for the binary-download
# fallback paths only. The Homebrew and apt/dnf/yum/pacman paths install from
# package sources and never need a version, so they never call this.
resolve_and_set_version() {
  TAG=$(resolve_version) || exit 1
  if [[ -z "$TAG" ]]; then
    echo "Error: Could not determine latest version."
    exit 1
  fi
  VERSION="${TAG#v}"
}

echo "Detected: OS=${OS} Arch=${ARCH}"
echo ""
```

- [ ] **Step 4: Call `resolve_and_set_version` in the three fallback branches**

In the same file, add `resolve_and_set_version` as the first statement inside each binary-download fallback branch, immediately before the branch's `ARTIFACT=...` assignment:

1. macOS, no Homebrew (the `else` after the `command -v brew` check — the branch that sets `ARTIFACT="wendy-cli-darwin-${ARCH}-${VERSION}.tar.gz"`):

```bash
    resolve_and_set_version
    ARTIFACT="wendy-cli-darwin-${ARCH}-${VERSION}.tar.gz"
```

2. Linux, no package manager (the final `else` that sets `ARTIFACT="wendy-cli-linux-${ARCH}-${VERSION}.tar.gz"`):

```bash
    resolve_and_set_version
    ARTIFACT="wendy-cli-linux-${ARCH}-${VERSION}.tar.gz"
```

3. Windows (the `elif [[ "$OS" == "windows" ]]` branch that sets `ARTIFACT="wendy-cli-windows-${ARCH}-${VERSION}.zip"`):

```bash
  resolve_and_set_version
  ARTIFACT="wendy-cli-windows-${ARCH}-${VERSION}.zip"
```

(Match the existing indentation of each branch.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `bash .github/scripts/install-scripts_test.sh`
Expected: PASS — `defer.no_github` and `defer.no_manifest` both `ok`, and all Task 1 checks still `ok`.

- [ ] **Step 6: Lint**

Run: `shellcheck go/internal/cli/assets/docs/cli.sh`
Expected: no new warnings.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/assets/docs/cli.sh .github/scripts/install-scripts_test.sh
git commit -m "fix(install): cli.sh resolves version only on binary-fallback paths"
```

---

## Task 3: CI publish of scripts + manifest to GCS

Publish the two scripts and a `manifest.json` to `gs://wendy-install-public` from CI, preserving the opposite channel pointer via a read-modify-write merge.

**Files:**
- Create: `.github/scripts/install-manifest-merge.jq`
- Create: `.github/scripts/install-manifest-merge_test.sh`
- Create: `.github/scripts/publish-install-scripts.sh`
- Modify: `.github/workflows/build.yml` (add the `publish-install-scripts` job)

**Interfaces:**
- Produces: objects `agent.sh`, `cli.sh`, `manifest.json` at the root of `gs://wendy-install-public`. `manifest.json` shape: `{ "latest": "<ver>", "latest_nightly": "<ver>" }`.
- Consumes: `build.yml` outputs `needs.determine-version.outputs.version` and `.is_release`; env `BUCKET`, `PROJECT`.

- [ ] **Step 1: Write the failing jq test**

Create `.github/scripts/install-manifest-merge_test.sh`:

```bash
#!/usr/bin/env bash
# .github/scripts/install-manifest-merge_test.sh
set -euo pipefail
cd "$(dirname "$0")"

FILTER=install-manifest-merge.jq
fail=0
check() { if [ "$2" != "$3" ]; then echo "FAIL $1: expected [$2] got [$3]"; fail=1; else echo "ok $1"; fi; }

# Case 1: empty manifest, nightly publish sets latest_nightly, leaves latest null
OUT=$(echo '{}' | jq -f "$FILTER" --arg version 2026.07.19-1 --argjson is_release false)
check "nightly.latest_nightly" "2026.07.19-1" "$(echo "$OUT" | jq -r .latest_nightly)"
check "nightly.latest_absent"  "null"          "$(echo "$OUT" | jq -r '.latest // "null"')"

# Case 2: stable publish sets latest and preserves the prior nightly pointer
PRIOR='{"latest_nightly":"2026.07.19-1"}'
OUT=$(echo "$PRIOR" | jq -f "$FILTER" --arg version 2026.07.20-2 --argjson is_release true)
check "stable.latest"        "2026.07.20-2" "$(echo "$OUT" | jq -r .latest)"
check "stable.keeps_nightly" "2026.07.19-1" "$(echo "$OUT" | jq -r .latest_nightly)"

exit $fail
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash .github/scripts/install-manifest-merge_test.sh`
Expected: FAIL — `install-manifest-merge.jq` does not exist yet (`jq: error: Could not open ...`).

- [ ] **Step 3: Create the jq filter**

Create `.github/scripts/install-manifest-merge.jq`:

```jq
# Update the channel pointer for the install manifest.
# Inputs: $version (string), $is_release (bool).
if $is_release then .latest = $version else .latest_nightly = $version end
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash .github/scripts/install-manifest-merge_test.sh`
Expected: PASS — all four checks print `ok`.

- [ ] **Step 5: Create the publish script**

Create `.github/scripts/publish-install-scripts.sh`:

```bash
#!/usr/bin/env bash
# .github/scripts/publish-install-scripts.sh
# Uploads the install scripts to gs://$BUCKET/{agent,cli}.sh and merges the
# channel pointer into gs://$BUCKET/manifest.json (read-modify-write with a
# generation-match precondition, retried once).
set -euo pipefail

: "${VERSION:?}" "${IS_RELEASE:?}" "${BUCKET:?}" "${PROJECT:?}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCS_DIR="${REPO_ROOT}/go/internal/cli/assets/docs"
gcloud config set project "$PROJECT" >/dev/null

# 1) Upload both scripts with the correct content-type and a short TTL so
#    updates propagate quickly.
for name in agent.sh cli.sh; do
  echo "Uploading ${DOCS_DIR}/${name} -> gs://${BUCKET}/${name}"
  gcloud storage cp "${DOCS_DIR}/${name}" "gs://${BUCKET}/${name}" \
    --content-type="text/x-shellscript" \
    --cache-control="public, max-age=300"
done

# 2) Read-modify-write manifest.json with a generation-match precondition.
merge_and_upload() {
  local gen merged
  if gcloud storage cp "gs://${BUCKET}/manifest.json" current.json 2>/dev/null; then
    gen="$(gcloud storage objects describe "gs://${BUCKET}/manifest.json" --format='value(generation)')"
  else
    echo '{}' > current.json
    gen=0
  fi
  merged="$(jq -f "${SCRIPT_DIR}/install-manifest-merge.jq" \
    --arg version "$VERSION" \
    --argjson is_release "$([ "$IS_RELEASE" = "true" ] && echo true || echo false)" \
    current.json)"
  if [ -z "$merged" ] || ! printf '%s' "$merged" | jq empty 2>/dev/null; then
    echo "manifest merge produced empty or invalid JSON" >&2
    return 1
  fi
  echo "$merged" > manifest.json
  gcloud storage cp manifest.json "gs://${BUCKET}/manifest.json" \
    --content-type="application/json" \
    --cache-control="public, max-age=300" \
    --if-generation-match="$gen"
}

if ! merge_and_upload; then
  echo "manifest write conflicted; retrying once..." >&2
  merge_and_upload
fi
echo "Published install scripts + manifest (is_release=${IS_RELEASE}) to gs://${BUCKET}/"
```

Make it executable:

```bash
chmod +x .github/scripts/publish-install-scripts.sh
```

- [ ] **Step 6: Add the `publish-install-scripts` job to `build.yml`**

In `.github/workflows/build.yml`, insert a new job immediately after the `publish-agent-gcs:` job (after its final step, before `integration-tests:`). Copy the auth/setup step SHAs verbatim from `publish-agent-gcs`:

```yaml
  publish-install-scripts:
    name: Publish install scripts to GCS
    runs-on: ubuntu-latest
    needs: [determine-version, build]
    if: |
      always() &&
      needs.determine-version.result == 'success' &&
      needs.build.result == 'success' &&
      (github.event_name == 'push' ||
       (github.event_name == 'workflow_dispatch' && inputs.publish == true))
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0

      - name: Authenticate to GCP
        uses: google-github-actions/auth@7c6bc770dae815cd3e89ee6cdf493a5fab2cc093 # v3
        with:
          workload_identity_provider: ${{ vars.GCP_WORKLOAD_IDENTITY_PROVIDER }}
          service_account: ${{ vars.GCP_SERVICE_ACCOUNT }}

      - name: Set up Cloud SDK
        uses: google-github-actions/setup-gcloud@aa5489c8933f4cc7a4f7d45035b3b1440c9c10db # v3

      - name: Upload install scripts and update manifest
        env:
          VERSION: ${{ needs.determine-version.outputs.version }}
          IS_RELEASE: ${{ needs.determine-version.outputs.is_release }}
          BUCKET: wendy-install-public
          PROJECT: ${{ vars.GCP_PROJECT_ID }}
        run: ./.github/scripts/publish-install-scripts.sh

      - name: Verify scripts are reachable
        run: |
          set -euo pipefail
          for name in agent.sh cli.sh manifest.json; do
            curl -fsS --max-time 30 "https://install.wendy.dev/${name}" >/dev/null
            echo "ok: https://install.wendy.dev/${name}"
          done
```

Note: the `Verify scripts are reachable` step only succeeds after the maintainer completes the DNS/LB cutover (Task 5). Until then it will fail on `install.wendy.dev`; this is intentional and flags an incomplete cutover. If merging repo changes before cutover, temporarily point the verify at `https://storage.googleapis.com/wendy-install-public/${name}` and switch to `install.wendy.dev` once DNS is live.

- [ ] **Step 7: Commit**

```bash
git add .github/scripts/install-manifest-merge.jq .github/scripts/install-manifest-merge_test.sh .github/scripts/publish-install-scripts.sh .github/workflows/build.yml
git commit -m "ci(install): publish install scripts + manifest to GCS"
```

---

## Task 4: Run the shell tests in CI

Wire the two shell test scripts and `shellcheck` into CI so regressions are caught.

**Files:**
- Modify: `.github/workflows/go-tests.yml` (add a `shell-tests` job)

**Interfaces:**
- Consumes: `.github/scripts/install-scripts_test.sh`, `.github/scripts/install-manifest-merge_test.sh`.

- [ ] **Step 1: Inspect the workflow to find the jobs block**

Run: `sed -n '1,40p' .github/workflows/go-tests.yml`
Expected: shows `name:`, `on:`, and a `jobs:` key. Note the indentation of existing jobs.

- [ ] **Step 2: Add a `shell-tests` job**

Under `jobs:` in `.github/workflows/go-tests.yml`, add:

```yaml
  shell-tests:
    name: Install script tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0
      - name: shellcheck install scripts
        run: shellcheck go/internal/cli/assets/docs/agent.sh go/internal/cli/assets/docs/cli.sh
      - name: resolver + deferral tests
        run: bash .github/scripts/install-scripts_test.sh
      - name: manifest merge tests
        run: bash .github/scripts/install-manifest-merge_test.sh
```

(`shellcheck` and `jq` are preinstalled on `ubuntu-latest` GitHub runners.)

- [ ] **Step 3: Verify the tests pass locally one more time**

Run: `bash .github/scripts/install-scripts_test.sh && bash .github/scripts/install-manifest-merge_test.sh`
Expected: every line prints `ok`; exit code 0.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/go-tests.yml
git commit -m "ci(install): run install-script shell tests + shellcheck"
```

---

## Task 5: Retire GitHub Pages + document hosting and the cutover runbook

Remove the GitHub Pages deploy and add a maintainer-facing doc that both explains the hosting and gives the exact LB/cert/DNS commands. **Ordering:** the maintainer completes the runbook's LB verification and DNS cutover around the time this branch merges; Pages content is static and keeps serving until DNS is repointed.

**Files:**
- Delete: `.github/workflows/static.yml`
- Create: `go/internal/cli/assets/docs/development/install-hosting.md`

- [ ] **Step 1: Delete the GitHub Pages workflow**

Run: `git rm .github/workflows/static.yml`
Expected: `rm '.github/workflows/static.yml'`.

- [ ] **Step 2: Create the hosting + runbook doc**

Create `go/internal/cli/assets/docs/development/install-hosting.md`:

````markdown
# Install Script Hosting

The public install one-liners are served from Google Cloud, not GitHub Pages:

```bash
curl -fsSL https://install.wendy.dev/cli.sh   | bash
curl -fsSL https://install.wendy.dev/agent.sh | bash
```

## Why

`install.wendy.dev` was previously a GitHub Pages custom domain (deployed by the
now-removed `.github/workflows/static.yml`). GitHub Pages soft-throttles a single
IP and returns `403` when many people install at once from one network (e.g. a
workshop behind one NAT). The scripts also resolved their version from the
GitHub REST API (60 requests/hour per IP), which failed the same way. Both are
now served from GCS.

## What is hosted

| Object | Served at | Content-Type |
|---|---|---|
| `agent.sh` | `https://install.wendy.dev/agent.sh` | `text/x-shellscript` |
| `cli.sh` | `https://install.wendy.dev/cli.sh` | `text/x-shellscript` |
| `manifest.json` | `https://install.wendy.dev/manifest.json` | `application/json` |

`manifest.json` carries `{ "latest": "<version>", "latest_nightly": "<version>" }`.
The scripts read `latest` to resolve the install version and fall back to the
GitHub API only if the manifest is unreachable.

## Publishing

The `publish-install-scripts` job in `.github/workflows/build.yml` uploads the
two scripts and merges the channel pointer into `manifest.json` on every `main`
push and on stable release, using `.github/scripts/publish-install-scripts.sh`.
It authenticates with the same Workload Identity Federation identity as the docs
and agent-mirror publishers.

Objects are uploaded with `Cache-Control: public, max-age=300`, so changes take
effect within about five minutes.

## Infrastructure

Static files live in the public bucket `wendy-install-public` and are served
through the **same** global external HTTP(S) load balancer that fronts
`docs.wendy.dev` (see `docs-site.md`). The load balancer terminates HTTPS; a host
rule routes `install.wendy.dev` to a backend bucket for `wendy-install-public`.

### One-time maintainer runbook (LB + cert + DNS)

Run these once, from a shell authenticated to the Wendy GCP project. Replace
`<PROJECT>` with the project id, `<URL_MAP>` / `<TARGET_PROXY>` / `<CERT>` with
the existing docs load-balancer resource names (find them with
`gcloud compute url-maps list`, `gcloud compute target-https-proxies list`,
`gcloud compute ssl-certificates list`).

```bash
# 1. Create the public bucket and make its objects world-readable.
gcloud storage buckets create gs://wendy-install-public \
  --project=<PROJECT> --location=us --uniform-bucket-level-access
gcloud storage buckets add-iam-policy-binding gs://wendy-install-public \
  --member=allUsers --role=roles/storage.objectViewer

# 2. Seed the bucket so the LB has objects to serve before DNS cutover.
#    (CI republishes these on every build; this is just for verification.)
gcloud storage cp go/internal/cli/assets/docs/agent.sh gs://wendy-install-public/agent.sh \
  --content-type=text/x-shellscript --cache-control="public, max-age=300"
gcloud storage cp go/internal/cli/assets/docs/cli.sh gs://wendy-install-public/cli.sh \
  --content-type=text/x-shellscript --cache-control="public, max-age=300"

# 3. Back the bucket with a backend bucket and add a host rule to the docs LB.
gcloud compute backend-buckets create wendy-install-backend \
  --gcs-bucket-name=wendy-install-public --enable-cdn --project=<PROJECT>
gcloud compute url-maps add-path-matcher <URL_MAP> \
  --path-matcher-name=install \
  --default-backend-bucket=wendy-install-backend \
  --new-hosts=install.wendy.dev \
  --project=<PROJECT>

# 4. Add install.wendy.dev to a Google-managed certificate and attach it.
#    Managed certs take minutes-to-tens-of-minutes to become ACTIVE.
gcloud compute ssl-certificates create wendy-install-cert \
  --domains=install.wendy.dev --global --project=<PROJECT>
gcloud compute target-https-proxies update <TARGET_PROXY> \
  --ssl-certificates=<CERT>,wendy-install-cert --global --project=<PROJECT>

# 5. Find the LB's global anycast IP.
gcloud compute forwarding-rules list --global --project=<PROJECT>
```

Then, in the DNS zone for `wendy.dev`:

- **Remove** the existing `install.wendy.dev` record pointing at GitHub Pages
  (`CNAME -> wendylabsinc.github.io`) and the GitHub Pages custom-domain binding
  in the repository's Pages settings.
- **Add** `install.wendy.dev` `A` (and `AAAA` if the LB has IPv6) records → the
  load balancer's global IP from step 5.

Verify before announcing:

```bash
curl -fsSL https://install.wendy.dev/manifest.json
curl -fsSL https://install.wendy.dev/agent.sh | head -1   # -> #!/usr/bin/env bash
```
````

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli/assets/docs/development/install-hosting.md
git commit -m "docs(install): host scripts on GCS; retire Pages; add cutover runbook"
```

---

## Self-Review Notes (author checklist — not an execution step)

- **Spec coverage:** §A hosting → Tasks 3 (publish), 5 (LB/DNS runbook, retire Pages). §B GitHub-free → Task 1 (GCS manifest + resolver), Task 2 (`cli.sh` deferral). Testing section → Tasks 1–4 (unit + integration + shellcheck in CI + health check). Scope boundaries → Global Constraints + Task 3 (only scripts+manifest; binaries stay on GitHub).
- **Placeholder scan:** runbook uses explicit `<PROJECT>`/`<URL_MAP>` placeholders that are *maintainer inputs*, not plan gaps; every code/test step has complete content.
- **Type/name consistency:** `resolve_version`, `manifest_latest`, `github_latest`, `fetch_stdout`, `download`, `resolve_and_set_version`, `MANIFEST_URL`, bucket `wendy-install-public`, manifest keys `latest`/`latest_nightly`, marker strings `# >>> wendy-install-shared` / `# <<< wendy-install-shared` are used identically across all tasks and tests.
````
