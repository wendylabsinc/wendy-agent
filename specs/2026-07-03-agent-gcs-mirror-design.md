# wendy-agent GCS mirror — design

Date: 2026-07-03

## Problem

The CLI fetches the `wendy-agent` binary from GitHub: `wendy device update` (and five
other call sites) lists releases via the GitHub API (`api.github.com/repos/wendylabsinc/wendy-agent/releases`)
and downloads the tarball via the release asset `browser_download_url`. Unauthenticated
GitHub API access is rate-limited (60 req/hr/IP), and hosting all agent traffic on GitHub
is fragile at scale.

`wendyos-builder` already publishes OS images to a public GCS bucket
(`gs://wendyos-images-public`), and the CLI already downloads OS images/OTA from that
bucket via `go/internal/cli/commands/manifest.go`
(`const gcsBaseURL = "https://storage.googleapis.com/wendyos-images-public"`). This design
extends that pattern to the agent binary so the download hot path leaves GitHub.

## Goal

1. Publish `wendy-agent` linux tarballs to `gs://wendyos-images-public` in CI, on the same
   triggers that cut a GitHub release today.
2. Make the CLI download the agent from GCS by default, falling back to the existing GitHub
   path on any GCS miss.

## Non-goals (v1)

- CLI self-update version check (`update.go`) — it reads only `tag_name` from a single API
  call, is low-volume, and stays on GitHub for now.
- GCS object retention / pruning of old nightly `agent/<version>/` prefixes — flagged as a
  follow-up.
- No new CLI flags or environment knobs. GCS-first is automatic and transparent.
- GitHub release publishing is unchanged and remains the fallback source.

## Bucket layout

Reuse the existing bucket and public-read base URL (no new bucket, no Artifact Registry):

```
gs://wendyos-images-public/
  agent/
    manifest.json                                        # index + latest pointers
    <version>/
      wendy-agent-linux-amd64-<version>.tar.gz
      wendy-agent-linux-arm64-<version>.tar.gz
```

- `<version>` is the CI version string `YYYY.MM.DD-HHMMSS` (same value used for the git tag
  and GitHub release; no `v` prefix).
- Tarball basenames and their internal layout (`wendy-agent-linux-<arch>/wendy-agent`) are
  **identical** to today's GitHub artifacts, so the CLI's existing gunzip/untar extraction
  needs no change.

### manifest.json schema

Mirrors the `master.json` conventions in `manifest.go`; written with
`Cache-Control: no-store` (as wendyos-builder does) to avoid stale-read races.

```json
{
  "latest": "2026.07.01-120000",
  "latest_nightly": "2026.07.03-093000",
  "versions": {
    "2026.07.03-093000": {
      "is_nightly": true,
      "artifacts": {
        "amd64": {
          "path": "agent/2026.07.03-093000/wendy-agent-linux-amd64-2026.07.03-093000.tar.gz",
          "checksum": "<sha256 hex>",
          "size_bytes": 12345678
        },
        "arm64": {
          "path": "agent/2026.07.03-093000/wendy-agent-linux-arm64-2026.07.03-093000.tar.gz",
          "checksum": "<sha256 hex>",
          "size_bytes": 12345678
        }
      }
    }
  }
}
```

- `path` values are bucket-relative (no leading slash), consumed as `gcsBaseURL + "/" + path`
  exactly like the OS-image manifest paths.
- `checksum` is the sha256 hex of the `.tar.gz`.
- `latest` points at the most recent **stable** version; `latest_nightly` at the most recent
  **prerelease**. A stable release updates `latest` (and its version entry has
  `is_nightly: false`); a nightly updates `latest_nightly` (`is_nightly: true`).

## CI upload (`.github/workflows/build.yml`)

Add one job, `publish-agent-gcs`. Rationale for a dedicated job rather than folding upload
into the `build` matrix: the matrix runs amd64 and arm64 in parallel, and a single serialized
manifest writer avoids a read-modify-write race on `manifest.json`.

```yaml
publish-agent-gcs:
  name: Publish agent to GCS
  runs-on: ubuntu-latest
  needs: [determine-version, build]
  # Same gate as the GitHub `release` job: publish on push to main (nightly) or
  # on workflow_dispatch with publish=true (stable). Never on pull_request.
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
    - name: Download agent tarballs
      uses: actions/download-artifact@<pinned>   # match existing pin in build.yml
      with:
        pattern: wendy-agent-linux-*-*.tar.gz     # amd64 + arm64
        merge-multiple: true
        path: agent-artifacts

    - name: Authenticate to GCP
      uses: google-github-actions/auth@<pinned>   # same pin as publish-linux-repos
      with:
        workload_identity_provider: ${{ vars.GCP_WORKLOAD_IDENTITY_PROVIDER }}
        service_account: ${{ vars.GCP_SERVICE_ACCOUNT }}

    - name: Set up Cloud SDK
      uses: google-github-actions/setup-gcloud@<pinned>

    - name: Upload tarballs and update manifest
      env:
        VERSION: ${{ needs.determine-version.outputs.version }}
        IS_RELEASE: ${{ needs.determine-version.outputs.is_release }}
        BUCKET: wendyos-images-public
      run: ./scripts/publish-agent-gcs.sh
```

`scripts/publish-agent-gcs.sh` (new) does:

1. For each `agent-artifacts/wendy-agent-linux-<arch>-<version>.tar.gz`:
   - `gcloud storage cp` it to `gs://$BUCKET/agent/$VERSION/<basename>`.
   - Compute sha256 and byte size for the manifest entry.
2. Read-modify-write the manifest:
   - `gcloud storage cp gs://$BUCKET/agent/manifest.json -` to read the current manifest
     (empty skeleton `{"versions":{}}` if it does not yet exist / 404).
   - With `jq`: splice in the new `versions.<version>` entry (both arch artifacts); set
     `latest` when `IS_RELEASE == true`, else `latest_nightly`.
   - Upload the merged JSON with `gcloud storage cp --cache-control=no-store`, using an
     `--if-generation-match` precondition captured from the read (retry the read-modify-write
     once on precondition failure). The per-ref `concurrency` group already serializes
     nightly vs stable runs; the precondition is defense-in-depth.

Prerequisites (ops, outside code): the WIF service account already used by
`publish-linux-repos` must have `roles/storage.objectAdmin` (or object create/read) on the
`wendyos-images-public` bucket. Called out in the plan as a manual step to confirm.

## CLI download (GCS-primary, GitHub-fallback)

### Consolidation

Today the pattern `fetchAgentRelease(nightly) → match asset "wendy-agent-linux-<arch>-*.tar.gz"
→ downloadAgentBinary(asset)` is duplicated across six call sites:

- `device.go:2113` (`wendy device update`)
- `device.go:195`
- `helpers.go:1438` (`performAgentUpdate`)
- `discover.go:805`
- `cloud_discover.go:399`
- `os_cmd.go:923`
- `os_install.go:2003` (hardcoded arm64)

Replace all of them with one helper:

```go
// resolveAgentBinary returns the wendy-agent binary for linux/<arch>, preferring
// GCS (to avoid GitHub rate limits) and falling back to GitHub releases on any GCS
// miss (network error, manifest/version/arch absent). version is the resolved
// version tag; source is "gcs" or "github" (for logging).
func resolveAgentBinary(arch string, nightly bool) (binary []byte, version, source string, err error)
```

Call sites that today read `release.TagName` for messages use the returned `version`.

### GCS path

Add to `manifest.go` (or a new `agent_manifest.go` in the same package):

```go
type agentManifest struct {
    Latest        string                            `json:"latest"`
    LatestNightly string                            `json:"latest_nightly"`
    Versions      map[string]agentManifestVersion   `json:"versions"`
}
type agentManifestVersion struct {
    IsNightly bool                              `json:"is_nightly"`
    Artifacts map[string]agentManifestArtifact  `json:"artifacts"` // key = arch
}
type agentManifestArtifact struct {
    Path      string `json:"path"`
    Checksum  string `json:"checksum"`
    SizeBytes int64  `json:"size_bytes"`
}
```

`resolveAgentBinary` GCS branch:

1. `GET gcsBaseURL + "/agent/manifest.json"` (30s timeout, matching the OS manifest fetchers).
2. `version := m.Latest` (or `m.LatestNightly` when `nightly`); error if empty.
3. `art := m.Versions[version].Artifacts[arch]`; error if absent → fall back.
4. `GET gcsBaseURL + "/" + art.Path` (5 min timeout, matching `downloadAgentBinary`).
5. Extract via the shared `extractAgentFromTarGz` (below); verify the downloaded bytes'
   sha256 against `art.Checksum` before returning (mismatch → error → fall back).

### Fallback path

The existing GitHub functions are retained unchanged and invoked only when the GCS branch
returns an error:

- `fetchAgentRelease` / `downloadAgentBinary` (`device.go`)
- the `api.github.com` host gate in `github.go`

On GCS failure, emit a one-line stderr note (e.g. `GCS agent fetch failed (<err>); falling
back to GitHub`) and proceed with the GitHub path so the outcome is identical to today.

### Shared extraction

Factor the tar walk currently inside `downloadAgentBinary` into:

```go
// extractAgentFromTarGz reads a gzipped tar and returns the bytes of the file
// whose name ends in "wendy-agent".
func extractAgentFromTarGz(r io.Reader) ([]byte, error)
```

Used by both the GCS branch and `downloadAgentBinary`.

## Error handling

- GCS unreachable / 404 manifest / missing version / missing arch / checksum mismatch: log
  and fall back to GitHub. The user-visible behavior degrades to exactly today's behavior.
- GitHub also failing: return the GitHub error (unchanged from today).
- CI upload: `set -euo pipefail`; a failed `gcloud storage cp` fails the job. Manifest
  read-modify-write retries once on generation-match conflict, then fails the job (a failed
  agent-GCS publish must not silently leave the manifest pointing at a missing version).

## Testing

- Unit: `agentManifest` JSON decode; `resolveAgentBinary` version/arch selection (stable vs
  nightly, missing arch → fallback signal); `extractAgentFromTarGz` on a fixture tarball;
  checksum-mismatch → error. GCS/GitHub HTTP mocked via `httptest`.
- CI: `scripts/publish-agent-gcs.sh` — the jq manifest-merge logic is unit-testable in
  isolation (feed a sample manifest + new entry, assert merged output and correct
  latest/latest_nightly pointer). Verify a first-run (no existing manifest) produces a valid
  skeleton.
- Manual/integration (post-merge, unverified until a real main build): confirm a nightly run
  populates `agent/<version>/` + updates `latest_nightly`, and `wendy device update --nightly`
  pulls from GCS (source == "gcs").

## Rollout / risk

- Additive: GitHub publishing and download both stay. Until the first GCS publish runs,
  every CLI download simply falls back to GitHub — no regression.
- The manifest is the only shared-mutable object; single serialized writer + generation-match
  keeps it consistent.
