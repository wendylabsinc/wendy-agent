# SBOMs + signing + SLSA provenance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate SPDX SBOMs for every WendyOS release artifact, sign them and emit SLSA build provenance via GitHub-native artifact attestations, and attach the SBOMs to the GitHub release.

**Architecture:** A shared `scripts/generate-sbom.sh` (Syft wrapper, `$SYFT_BIN`-indirected for testability) produces per-binary, Swift, and whole-repo SPDX-JSON SBOMs. In `build.yml`, a `sbom-source` job produces the Swift + source SBOMs, and a matrix `sbom-binaries` job produces one SBOM per shipped Go binary and runs `actions/attest-sbom` + `actions/attest-build-provenance` per artifact. The `release` job attaches all `*.spdx.json` as release assets.

**Tech Stack:** Bash, [Syft](https://github.com/anchore/syft) (SPDX-JSON), GitHub Actions, `actions/attest-build-provenance`, `actions/attest-sbom`, keyless Sigstore signing via GitHub OIDC.

## Global Constraints

- **Design source:** `specs/2026-07-01-sbom-signing-provenance-design.md`. Every task implicitly includes its scope/out-of-scope.
- **SBOM format:** SPDX-JSON only. No CycloneDX.
- **SBOM targets:** Go binaries `wendy` + `wendy-agent` across linux amd64/arm64, windows amd64/arm64, darwin amd64/arm64; Swift (`swift/` via `Package.resolved`); whole-repo source. **Exclude** `Examples/`, `node_modules`, `.build`, `.git`.
- **Signing:** Keyless via existing GitHub OIDC (`id-token: write`). No self-managed keys.
- **Action pinning:** Every GitHub Action MUST be pinned by full commit SHA with a `# vX.Y.Z` comment, matching the existing `build.yml` style (e.g. `actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0`). Resolve SHAs with the `gh` commands given in each task — never invent a SHA.
- **Syft version:** Pinned via `SYFT_VERSION` (set to the current latest stable syft release, e.g. `v1.18.1`; record the exact value used). Local and CI must use the same version.
- **No silent gaps:** Any Syft failure must make the script (and thus the job) exit non-zero.
- **Working dir:** All work happens in the `jo/sbom-signing-provenance` branch / `../wendyos-sbom` worktree.
- **Commit trailers:** End each commit message with the repo's standard co-author/session trailers.

---

### Task 1: SBOM script — arg parsing, subcommand routing, error paths

Build the script skeleton with a stubbable Syft (`$SYFT_BIN`) so all control flow is testable without real Syft. Scan logic is filled in Task 2.

**Files:**
- Create: `scripts/generate-sbom.sh`
- Test: `scripts/test/generate-sbom.test.sh`
- Create (test fixture): `scripts/test/fake-syft.sh`

**Interfaces:**
- Produces (CLI contract used by CI and later tasks):
  - `generate-sbom.sh binary <binary-path> <output-file>`
  - `generate-sbom.sh swift <output-file> [--repo-root <dir>]`
  - `generate-sbom.sh source <output-file> [--repo-root <dir>]`
  - `generate-sbom.sh all <binaries-dir> <out-dir> <version> [--repo-root <dir>]`
  - Env: `SYFT_BIN` (default `syft`) — the syft executable to invoke.
  - Exit codes: `2` = usage error, `1` = scan/runtime failure, `0` = success.

- [ ] **Step 1: Write the failing test**

Create `scripts/test/fake-syft.sh` (a stand-in for syft that emits a minimal valid SPDX doc, or fails on command):

```bash
#!/usr/bin/env bash
# Fake syft for tests. Emits a minimal SPDX-JSON doc to stdout.
# If FAKE_SYFT_FAIL=1, exits non-zero to simulate a scan failure.
set -euo pipefail
if [[ "${FAKE_SYFT_FAIL:-0}" == "1" ]]; then
  echo "fake-syft: simulated failure" >&2
  exit 1
fi
# Echo the resolved source (last non-flag arg after 'scan') for assertions.
printf '{"spdxVersion":"SPDX-2.3","name":"fake","packages":[]}\n'
```

Create `scripts/test/generate-sbom.test.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../generate-sbom.sh"
export SYFT_BIN="$HERE/fake-syft.sh"
chmod +x "$HERE/fake-syft.sh" "$SCRIPT"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fail=0
check() { if eval "$2"; then echo "ok - $1"; else echo "FAIL - $1"; fail=1; fi; }

# usage error when no subcommand
"$SCRIPT" >/dev/null 2>&1; check "no-args exits 2" '[ $? -eq 2 ]'

# unknown subcommand -> 2
"$SCRIPT" bogus >/dev/null 2>&1; check "unknown subcommand exits 2" '[ $? -eq 2 ]'

# binary: missing file -> 1
"$SCRIPT" binary "$TMP/nope" "$TMP/out.spdx.json" >/dev/null 2>&1
check "binary missing-file exits 1" '[ $? -eq 1 ]'

# binary: happy path writes valid SPDX
touch "$TMP/wendy"; chmod +x "$TMP/wendy"
"$SCRIPT" binary "$TMP/wendy" "$TMP/bin.spdx.json" >/dev/null 2>&1
check "binary writes output" '[ -s "$TMP/bin.spdx.json" ]'
check "binary output is SPDX" 'grep -q spdxVersion "$TMP/bin.spdx.json"'

# scan failure propagates
FAKE_SYFT_FAIL=1 "$SCRIPT" binary "$TMP/wendy" "$TMP/x.spdx.json" >/dev/null 2>&1
check "syft failure exits 1" '[ $? -eq 1 ]'

# swift + source write output
"$SCRIPT" swift "$TMP/swift.spdx.json" --repo-root "$TMP" >/dev/null 2>&1
check "swift writes output" '[ -s "$TMP/swift.spdx.json" ]'
"$SCRIPT" source "$TMP/src.spdx.json" --repo-root "$TMP" >/dev/null 2>&1
check "source writes output" '[ -s "$TMP/src.spdx.json" ]'

exit $fail
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bash scripts/test/generate-sbom.test.sh`
Expected: FAIL (script does not exist yet — non-zero exit / "No such file").

- [ ] **Step 3: Write the script skeleton**

Create `scripts/generate-sbom.sh`:

```bash
#!/usr/bin/env bash
#
# Generate SPDX-JSON SBOMs for WendyOS release artifacts using Syft.
#
# Usage:
#   generate-sbom.sh binary <binary-path> <output-file>
#   generate-sbom.sh swift  <output-file> [--repo-root <dir>]
#   generate-sbom.sh source <output-file> [--repo-root <dir>]
#   generate-sbom.sh all    <binaries-dir> <out-dir> <version> [--repo-root <dir>]
#
# Env:
#   SYFT_BIN   syft executable (default: syft)
#
set -euo pipefail

SYFT_BIN="${SYFT_BIN:-syft}"

usage() {
  sed -n '2,14p' "$0" >&2
  exit 2
}

# resolve --repo-root from trailing args; defaults to git toplevel or cwd.
repo_root() {
  local rr=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --repo-root) rr="${2:-}"; shift 2 || true;;
      *) shift;;
    esac
  done
  if [[ -n "$rr" ]]; then printf '%s\n' "$rr"; return; fi
  git rev-parse --show-toplevel 2>/dev/null || pwd
}

scan_binary() {  # <binary-path> <output-file>
  local bin="$1" out="$2"
  [[ -f "$bin" ]] || { echo "error: binary not found: $bin" >&2; return 1; }
  mkdir -p "$(dirname "$out")"
  "$SYFT_BIN" scan "file:$bin" -o spdx-json > "$out"
}

scan_swift() {   # <output-file> <repo-root>
  local out="$1" rr="$2"
  mkdir -p "$(dirname "$out")"
  "$SYFT_BIN" scan "dir:$rr/swift" -o spdx-json > "$out"
}

scan_source() {  # <output-file> <repo-root>
  local out="$1" rr="$2"
  mkdir -p "$(dirname "$out")"
  "$SYFT_BIN" scan "dir:$rr" \
    --exclude './.git/**' \
    --exclude '**/node_modules/**' \
    --exclude '**/.build/**' \
    --exclude './Examples/**' \
    -o spdx-json > "$out"
}

cmd="${1:-}"; [[ -n "$cmd" ]] || usage
shift || true

case "$cmd" in
  binary)
    [[ $# -ge 2 ]] || usage
    scan_binary "$1" "$2"
    ;;
  swift)
    [[ $# -ge 1 ]] || usage
    out="$1"; shift
    scan_swift "$out" "$(repo_root "$@")"
    ;;
  source)
    [[ $# -ge 1 ]] || usage
    out="$1"; shift
    scan_source "$out" "$(repo_root "$@")"
    ;;
  all)
    [[ $# -ge 3 ]] || usage
    bindir="$1"; outdir="$2"; version="$3"; shift 3
    rr="$(repo_root "$@")"
    mkdir -p "$outdir"
    shopt -s nullglob
    found=false
    for d in "$bindir"/*/; do
      artifact="$(basename "$d")"
      for b in "${d}wendy" "${d}wendy-agent" "${d}wendy.exe"; do
        [[ -f "$b" ]] || continue
        found=true
        scan_binary "$b" "$outdir/${artifact}-${version}.spdx.json"
      done
    done
    [[ "$found" == true ]] || { echo "error: no binaries under $bindir" >&2; exit 1; }
    scan_swift  "$outdir/wendy-swift-${version}.spdx.json"  "$rr"
    scan_source "$outdir/wendy-source-${version}.spdx.json" "$rr"
    ;;
  *)
    usage
    ;;
esac
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `chmod +x scripts/generate-sbom.sh scripts/test/*.sh && bash scripts/test/generate-sbom.test.sh`
Expected: all lines print `ok - ...`, exit 0.

- [ ] **Step 5: Commit**

```bash
git add scripts/generate-sbom.sh scripts/test/generate-sbom.test.sh scripts/test/fake-syft.sh
git commit -m "feat(sbom): SPDX SBOM generation script with unit tests"
```

---

### Task 2: Verify the script against real Syft (integration)

Confirm the Syft invocations actually produce valid SPDX for a real Go binary. This validates the `file:`/`dir:` sources and exclude globs that the fake can't.

**Files:**
- Create: `scripts/test/generate-sbom.integration.sh`

**Interfaces:**
- Consumes: `generate-sbom.sh binary|swift|source` from Task 1.

- [ ] **Step 1: Write the integration test**

Create `scripts/test/generate-sbom.integration.sh`:

```bash
#!/usr/bin/env bash
# Integration test: requires real syft + go on PATH. Skips if syft is absent.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SCRIPT="$HERE/../generate-sbom.sh"
command -v syft >/dev/null 2>&1 || { echo "SKIP: syft not installed"; exit 0; }
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

# Build a real wendy binary to catalog.
( cd "$ROOT/go" && CGO_ENABLED=0 go build -o "$TMP/wendy" ./cmd/wendy )

bash "$SCRIPT" binary "$TMP/wendy" "$TMP/wendy.spdx.json"
jq -e '.spdxVersion and (.packages | length > 0)' "$TMP/wendy.spdx.json" >/dev/null \
  || { echo "FAIL: binary SBOM missing packages"; exit 1; }

bash "$SCRIPT" source "$TMP/src.spdx.json" --repo-root "$ROOT"
jq -e '.spdxVersion' "$TMP/src.spdx.json" >/dev/null || { echo "FAIL: source SBOM invalid"; exit 1; }
# Excludes honored: no Examples/ file paths leak in.
if jq -e '[.. | .fileName? // empty] | map(select(startswith("Examples/"))) | length > 0' \
     "$TMP/src.spdx.json" >/dev/null 2>&1; then
  echo "FAIL: Examples/ not excluded"; exit 1
fi
echo "ok - integration"
```

- [ ] **Step 2: Install syft locally at the pinned version**

Run (records the version you use in `SYFT_VERSION`):

```bash
SYFT_VERSION=v1.18.1
curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b "$HOME/.local/bin" "$SYFT_VERSION"
export PATH="$HOME/.local/bin:$PATH"
syft version
```

Expected: prints `Version: 1.18.1` (or the version you pinned).

- [ ] **Step 3: Run the integration test**

Run: `bash scripts/test/generate-sbom.integration.sh`
Expected: `ok - integration` (or `SKIP: syft not installed` if unavailable — but you just installed it, so expect `ok`).

- [ ] **Step 4: Commit**

```bash
git add scripts/test/generate-sbom.integration.sh
git commit -m "test(sbom): syft integration test for SBOM generation"
```

---

### Task 3: `sbom-source` job — Swift + whole-repo SBOMs

Generate the two source-level SBOMs in CI and upload them for the release to pick up.

**Files:**
- Modify: `.github/workflows/build.yml` (add `sbom-source` job after `build-agent-macos-app`, before `package-*`).

**Interfaces:**
- Consumes: `determine-version.outputs.version`, `scripts/generate-sbom.sh`.
- Produces: workflow artifacts named `sbom-swift-<version>` and `sbom-source-<version>` containing `wendy-swift-<version>.spdx.json` and `wendy-source-<version>.spdx.json`.

- [ ] **Step 1: Resolve action SHAs to pin**

Run:

```bash
gh api repos/anchore/sbom-action/git/refs/tags/v0.20.0 --jq '.object.sha'   # download-syft
gh api repos/actions/upload-artifact/git/refs/tags/v7 --jq '.object.sha'    # already used in repo; reuse existing SHA
```

Use the existing `actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7` and `actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0` SHAs already in `build.yml`. For syft install, pin `anchore/sbom-action/download-syft` to the SHA printed above (record the tag, e.g. `# v0.20.0`).

- [ ] **Step 2: Add the `sbom-source` job**

Insert into `.github/workflows/build.yml` (after the `build-agent-macos-app` job, ~line 266):

```yaml
  sbom-source:
    name: SBOM (source + swift)
    runs-on: ubuntu-latest
    needs: [determine-version]
    steps:
      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0

      - name: Install Syft
        uses: anchore/sbom-action/download-syft@<RESOLVED_SHA> # v0.20.0

      - name: Generate source + swift SBOMs
        env:
          VERSION: ${{ needs.determine-version.outputs.version }}
        run: |
          set -euo pipefail
          chmod +x scripts/generate-sbom.sh
          mkdir -p sboms
          scripts/generate-sbom.sh swift  "sboms/wendy-swift-${VERSION}.spdx.json"  --repo-root "$PWD"
          scripts/generate-sbom.sh source "sboms/wendy-source-${VERSION}.spdx.json" --repo-root "$PWD"

      - name: Upload swift SBOM
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: sbom-swift-${{ needs.determine-version.outputs.version }}
          path: sboms/wendy-swift-${{ needs.determine-version.outputs.version }}.spdx.json
          retention-days: 14
          if-no-files-found: error

      - name: Upload source SBOM
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: sbom-source-${{ needs.determine-version.outputs.version }}
          path: sboms/wendy-source-${{ needs.determine-version.outputs.version }}.spdx.json
          retention-days: 14
          if-no-files-found: error
```

Replace `<RESOLVED_SHA>` with the SHA from Step 1.

- [ ] **Step 3: Validate workflow syntax**

Run: `cd .. && actionlint wendyos-sbom/.github/workflows/build.yml` (or, if actionlint absent, `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/build.yml'))"` from the worktree root).
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "ci(sbom): generate Swift + source SBOMs in build workflow"
```

---

### Task 4: `sbom-binaries` matrix job — per-binary SBOM + attestation

One matrix leg per shipped Go archive: download it, extract the binary, generate its SBOM, and emit signed SBOM + SLSA provenance attestations bound to the downloadable archive.

**Files:**
- Modify: `.github/workflows/build.yml` (add `sbom-binaries` job).

**Interfaces:**
- Consumes: `determine-version.outputs.version`; the archive artifacts uploaded by `build` and `build-go-macos` (names `<artifact-name>-<version>.tar.gz` / `.zip`); `scripts/generate-sbom.sh binary`.
- Produces: workflow artifacts `sbom-<artifact-name>-<version>` each containing `<artifact-name>-<version>.spdx.json`; plus signed attestations in the repo's attestation store.

- [ ] **Step 1: Resolve and record attestation action SHAs**

Run:

```bash
gh api repos/actions/attest-build-provenance/git/refs/tags/v3 --jq '.object.sha'
gh api repos/actions/attest-sbom/git/refs/tags/v3 --jq '.object.sha'
gh api repos/actions/download-artifact/git/refs/tags/v8 --jq '.object.sha'  # reuse repo's existing v8 SHA 3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c
```

Record the two `attest-*` SHAs; reuse the existing `download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8`.

- [ ] **Step 2: Add the `sbom-binaries` job**

Insert into `.github/workflows/build.yml` (after `sbom-source`):

```yaml
  sbom-binaries:
    name: SBOM+attest (${{ matrix.artifact-name }})
    runs-on: ubuntu-latest
    needs: [determine-version, build, build-go-macos]
    permissions:
      contents: read
      id-token: write
      attestations: write
    strategy:
      fail-fast: false
      matrix:
        include:
          - { artifact-name: wendy-agent-linux-amd64, binary: wendy-agent, ext: tar.gz }
          - { artifact-name: wendy-agent-linux-arm64, binary: wendy-agent, ext: tar.gz }
          - { artifact-name: wendy-cli-linux-amd64,   binary: wendy,       ext: tar.gz }
          - { artifact-name: wendy-cli-linux-arm64,   binary: wendy,       ext: tar.gz }
          - { artifact-name: wendy-cli-windows-amd64, binary: wendy.exe,   ext: zip }
          - { artifact-name: wendy-cli-windows-arm64, binary: wendy.exe,   ext: zip }
          - { artifact-name: wendy-cli-darwin-amd64,  binary: wendy,       ext: tar.gz }
          - { artifact-name: wendy-cli-darwin-arm64,  binary: wendy,       ext: tar.gz }
    steps:
      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0

      - name: Install Syft
        uses: anchore/sbom-action/download-syft@<RESOLVED_SHA> # v0.20.0

      - name: Download archive
        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8
        with:
          name: ${{ matrix.artifact-name }}-${{ needs.determine-version.outputs.version }}.${{ matrix.ext }}
          path: dist

      - name: Extract binary and generate SBOM
        id: sbom
        env:
          VERSION: ${{ needs.determine-version.outputs.version }}
          ARTIFACT: ${{ matrix.artifact-name }}
          BINARY: ${{ matrix.binary }}
          EXT: ${{ matrix.ext }}
        run: |
          set -euo pipefail
          chmod +x scripts/generate-sbom.sh
          ARCHIVE="dist/${ARTIFACT}-${VERSION}.${EXT}"
          mkdir -p extracted
          if [[ "$EXT" == "zip" ]]; then
            unzip -o "$ARCHIVE" -d extracted
          else
            tar -xzf "$ARCHIVE" -C extracted
          fi
          mkdir -p sboms
          SBOM="sboms/${ARTIFACT}-${VERSION}.spdx.json"
          scripts/generate-sbom.sh binary "extracted/${ARTIFACT}/${BINARY}" "$SBOM"
          echo "archive=$ARCHIVE" >> "$GITHUB_OUTPUT"
          echo "sbom=$SBOM" >> "$GITHUB_OUTPUT"

      - name: Attest SBOM
        uses: actions/attest-sbom@<RESOLVED_SHA> # v3
        with:
          subject-path: ${{ steps.sbom.outputs.archive }}
          sbom-path: ${{ steps.sbom.outputs.sbom }}

      - name: Attest build provenance
        uses: actions/attest-build-provenance@<RESOLVED_SHA> # v3
        with:
          subject-path: ${{ steps.sbom.outputs.archive }}

      - name: Upload SBOM
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: sbom-${{ matrix.artifact-name }}-${{ needs.determine-version.outputs.version }}
          path: ${{ steps.sbom.outputs.sbom }}
          retention-days: 14
          if-no-files-found: error
```

Replace both `<RESOLVED_SHA>` placeholders (syft + each attest action) with the SHAs from Steps 1/Task 3.

- [ ] **Step 3: Validate workflow syntax**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/build.yml'))"` (from the worktree root).
Expected: no errors. Also confirm the matrix `artifact-name` values exactly match the `build` and `build-go-macos` matrices (build.yml:80-109, 160-163).

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "ci(sbom): per-binary SBOM + SLSA provenance attestation matrix"
```

---

### Task 5: Attach SBOMs to the GitHub release + gate release on SBOM jobs

Wire the SBOM files into the release assets and make the release depend on the SBOM jobs so no release ships without them.

**Files:**
- Modify: `.github/workflows/build.yml` — `release` job `needs` (line ~662), `if` (lines ~663-673), and the file-list `find` (line ~685).

**Interfaces:**
- Consumes: `sbom-source` and `sbom-binaries` artifacts (already downloaded by the release job's existing `download-artifact` with `merge-multiple`).

- [ ] **Step 1: Add SBOM jobs to `release` needs**

In `build.yml`, change the `release` job `needs` (line ~662) from:

```yaml
    needs: [determine-version, build, build-go-macos, build-agent-macos-app, package-linux, package-windows, test-templates, integration-tests]
```

to:

```yaml
    needs: [determine-version, build, build-go-macos, build-agent-macos-app, sbom-source, sbom-binaries, package-linux, package-windows, test-templates, integration-tests]
```

- [ ] **Step 2: Gate on SBOM job success**

In the `release` job `if:` block (lines ~663-673), add these two conditions (after the `build-agent-macos-app.result == 'success'` line):

```yaml
      needs.sbom-source.result == 'success' &&
      needs.sbom-binaries.result == 'success' &&
```

- [ ] **Step 3: Include SBOMs in the release file list**

Change the `find` line in the "List files" step (line ~685) from:

```bash
            find . -name "*.tar.gz" -o -name "*.zip" -o -name "*.msi" -o -name "*.deb" -o -name "*.rpm" | sort
```

to:

```bash
            find . -name "*.tar.gz" -o -name "*.zip" -o -name "*.msi" -o -name "*.deb" -o -name "*.rpm" -o -name "*.spdx.json" | sort
```

- [ ] **Step 4: Validate workflow syntax**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/build.yml'))"`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "ci(sbom): attach SBOMs to release and gate release on SBOM jobs"
```

---

### Task 6: End-to-end verification on a prerelease run

Prove the pipeline emits SBOMs + verifiable attestations before relying on it.

**Files:** none (operational verification).

- [ ] **Step 1: Push the branch and trigger a prerelease build**

```bash
git push -u origin jo/sbom-signing-provenance
```

Open a PR (do not merge yet). To exercise the SBOM path on a prerelease, either merge to `main` (nightly prerelease) or run the workflow on the branch via `workflow_dispatch` with `publish=false`. Prefer `workflow_dispatch`:

```bash
gh workflow run build.yml --ref jo/sbom-signing-provenance
```

- [ ] **Step 2: Confirm SBOM jobs ran and produced artifacts**

Run:

```bash
RUN=$(gh run list --workflow build.yml --branch jo/sbom-signing-provenance --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN"
gh run view "$RUN" --json jobs --jq '.jobs[].name' | grep -i sbom
```

Expected: `SBOM (source + swift)` and 8 `SBOM+attest (...)` jobs all succeeded.

- [ ] **Step 3: Verify an attestation**

Download one binary archive artifact from the run and verify against the repo's attestations:

```bash
gh run download "$RUN" -n wendy-cli-linux-amd64-<version>.tar.gz -D /tmp/sbomcheck
gh attestation verify /tmp/sbomcheck/wendy-cli-linux-amd64-<version>.tar.gz \
  --repo wendylabsinc/WendyOS
```

Expected: `✓ Verification succeeded!` listing both a provenance and an SBOM attestation predicate.

- [ ] **Step 4: Record the result**

Note in the PR description: syft version pinned, jobs green, `gh attestation verify` output. No commit needed.

---

### Task 7: Verification docs + status updates

Document how consumers verify, and update the security artifacts to reflect that SBOM + provenance now ships.

**Files:**
- Create: `security/VERIFICATION.md`
- Modify: `security/THREAT_MODEL.md` (add an SBOM/provenance note)
- Modify: `specs/2026-06-28-security-status-map.md` (WDY-1001 row: SBOM+provenance portion → shipped)

**Interfaces:** none.

- [ ] **Step 1: Write `security/VERIFICATION.md`**

Create `security/VERIFICATION.md`:

```markdown
# Verifying WendyOS release artifacts

Every WendyOS release ships SPDX SBOMs and Sigstore-backed attestations
(SLSA build provenance + SBOM attestation), generated by the `build.yml`
workflow.

## SBOM files

Each release includes `*.spdx.json` SBOMs:

- `wendy-cli-<os>-<arch>-<version>.spdx.json` / `wendy-agent-linux-<arch>-<version>.spdx.json`
  — dependencies of each shipped binary (cataloged from the binary itself).
- `wendy-swift-<version>.spdx.json` — Swift package dependencies.
- `wendy-source-<version>.spdx.json` — whole-repo source dependencies.

Inspect one with any SPDX tool, e.g.:

    syft convert wendy-cli-linux-amd64-<version>.spdx.json -o table

## Verifying provenance and SBOM attestations

Download a release archive, then verify it was built by this repo's workflow:

    gh attestation verify wendy-cli-linux-amd64-<version>.tar.gz --repo wendylabsinc/WendyOS

Or with cosign (attestations are Sigstore bundles):

    cosign verify-blob-attestation \
      --new-bundle-format \
      --certificate-identity-regexp 'https://github.com/wendylabsinc/WendyOS/.github/workflows/build.yml@.*' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      wendy-cli-linux-amd64-<version>.tar.gz

A successful verification confirms the artifact's SLSA build provenance and
that its SBOM was produced by the WendyOS release pipeline.
```

- [ ] **Step 2: Update `security/THREAT_MODEL.md`**

Read `security/THREAT_MODEL.md` and add a short subsection under the relevant supply-chain / integrity section:

```markdown
### Software Bill of Materials & provenance

Releases include SPDX SBOMs for each shipped binary plus Swift and whole-repo
source SBOMs, and Sigstore-backed SLSA build provenance and SBOM attestations.
See `security/VERIFICATION.md` for verification steps.
```

- [ ] **Step 3: Update the WDY-1001 status**

In `specs/2026-06-28-security-status-map.md`, update the WDY-1001 row's status to note SBOM + provenance ships (leave reproducible-builds / signing-key items as still in progress). Match the table's existing wording/format.

- [ ] **Step 4: Commit**

```bash
git add security/VERIFICATION.md security/THREAT_MODEL.md specs/2026-06-28-security-status-map.md
git commit -m "docs(security): SBOM/provenance verification guide and status update"
```

---

## Self-Review

**Spec coverage:**
- SBOM for Go CLI+agent → Task 4 (per-binary matrix). ✓
- SBOM for Swift → Task 3 (`swift` scan). ✓
- Whole-repo source SBOM → Task 3 (`source` scan). ✓
- Attached to GitHub releases → Task 5. ✓
- Signing + SLSA provenance → Task 4 (`attest-sbom` + `attest-build-provenance`). ✓
- Shared script / reproducibility → Tasks 1-2. ✓
- SPDX-only, exclude Examples/node_modules/.build → script excludes + integration assertion (Tasks 1-2). ✓
- Release never ships without SBOMs → Task 5 gating. ✓
- Docs/status → Task 7. ✓

**Placeholder scan:** Action SHAs are intentionally resolved-at-execution via explicit `gh api` commands (Global Constraint: never invent a SHA); every other step has concrete code/commands. `<version>` in verification commands is a runtime value, not a plan gap.

**Type/name consistency:** Script CLI (`binary|swift|source|all`), SBOM filenames (`<artifact-name>-<version>.spdx.json`, `wendy-swift-<version>.spdx.json`, `wendy-source-<version>.spdx.json`), and matrix `artifact-name` values are consistent across Tasks 1, 3, 4, 5, 7 and match the `build`/`build-go-macos` matrices in `build.yml`.
