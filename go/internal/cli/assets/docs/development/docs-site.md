# Docs Site

The public WendyOS documentation site is built with Fumadocs and Next.js, then
published as a static export to `https://docs.wendy.dev`.

`https://docs.wendy.sh` redirects to the equivalent `https://docs.wendy.dev`
URL.

## URLs

| Purpose | URL |
|---|---|
| Latest stable docs | `https://docs.wendy.dev/latest/` |
| Latest nightly docs | `https://docs.wendy.dev/latest-nightly/` |
| Specific stable release | `https://docs.wendy.dev/release-<version>/` |
| Specific nightly release | `https://docs.wendy.dev/release-nightly-<version>/` |
| Branch preview | `https://docs.wendy.dev/branch-<branch>-<sha>/` |

## Source Layout

The Fumadocs app is reached through `docs/`, which is a repository-root symlink
to `go/internal/cli/assets/docs`. Top-level docs content lives as MDX and
`meta.json` files in that tree. Existing lower-level Markdown reference files
are still read from the same tree, but the prep script publishes them under the
`advanced/` section at build time.

```
docs/
  app/          Next.js app router routes, layout, and search
  components/   MDX components, search dialog, and providers
  guides/       Top-level guides and tutorials
  installation/ Top-level setup guides
  lib/          Fumadocs source loader and shared layout config
  scripts/      Content preparation for Fumadocs
  next.config.mjs
  package.json
```

Generated build output is ignored by git: `content/`, `public/`, `.source/`,
`.next/`, `out/`, and `export/`.

## Local Development

```sh
cd docs
npm ci
npm run dev
```

The dev server starts at `http://localhost:3000/`.

When editing source Markdown or MDX files, rerun the content prep script to
refresh generated Fumadocs content:

```sh
cd docs
node scripts/prepare-content.mjs
```

## Local Build

The deployed site uses a path prefix. Set `NEXT_PUBLIC_BASE_PATH` when testing a
prefixed build:

```sh
cd docs
NEXT_PUBLIC_BASE_PATH=/branch-local npm run build
```

The static export is written to `docs/out/`.

## CI And Deploy

The `.github/workflows/fumadocs.yml` workflow runs when `docs`,
`docs/**`, `go/internal/cli/assets/docs/**`, or the workflow file changes.

| Trigger | Behavior |
|---|---|
| `main` branch push | Builds and deploys a branch preview |
| Pull request to `main` from this repository | Builds and deploys a branch preview |
| Published stable release | Deploys `release-<version>/` and updates `latest/` |
| Published prerelease/nightly | Deploys `release-nightly-<version>/` and updates `latest-nightly/` |
| Manual dispatch (no inputs) | Builds a branch-style preview artifact without deploying |
| Manual dispatch with `release_tag` input | Deploys a release, identical to a published-release trigger. The `release_prerelease` input selects the target: `false` (default) deploys `release-<version>/` and updates `latest/`; `true` deploys `release-nightly-<version>/` and updates `latest-nightly/`. The dispatch ref must match `release_tag` (dispatch with `--ref "<release_tag>"`), otherwise the deploy fails fast so docs built from one ref are never published under a different release path. |

The deploy job authenticates to GCP with Workload Identity Federation and syncs
static files to `gs://wendy-docs-public/<deploy-path>`. Static exports include
SHA-256 manifests that are verified before each deploy path is synced.
Release deploys attempt to enable bucket object versioning before updating
`latest/` or `latest-nightly/` so alias overwrites remain recoverable. If the
deploy identity lacks bucket-update permission, the deploy verifies the current
state instead: it aborts only when versioning is confirmed disabled (an
overwrite would be unrecoverable), and continues with a warning when versioning
is already enabled out-of-band or cannot be read.

Required GitHub environment variables:

| Variable | Description |
|---|---|
| `GCP_WORKLOAD_IDENTITY_PROVIDER` | Workload Identity provider resource name |
| `GCP_SERVICE_ACCOUNT` | Deploy service account email |
| `GCP_PROJECT_ID` | GCP project ID |

## Hosting

Static files are served from the public `wendy-docs-public` Cloud Storage bucket
through the global external HTTP(S) load balancer for `docs.wendy.dev`. The load
balancer terminates HTTPS, redirects `docs.wendy.sh` to `docs.wendy.dev`, and
adds security response headers for the public docs host.

Branch-preview objects under `branch-*` are cleaned up by CI after 30 days.

## Release Notifications

The release workflow adds docs links to Discord notifications:

| Release type | Links |
|---|---|
| Stable | `release-<version>/` and `latest/` |
| Nightly | `release-nightly-<version>/` and `latest-nightly/` |

## Validation

Run these before opening or updating a PR that changes the docs app:

```sh
cd docs
npm run types:check
npm run build
```
