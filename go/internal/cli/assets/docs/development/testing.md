# Testing

WendyOS uses both unit tests and integration tests to validate the CLI and agent behaviour.

## Integration Tests

Integration tests verify that specific features work end-to-end on a real (or simulated) Wendy device. They live under `.github/ci-tests/` in the repository.

### Directory structure

Each integration test is a self-contained directory:

```
.github/ci-tests/<name>/
```

The naming convention is `<language>-<feature>`, for example `python-camera` or `swift-filesystem`.

Depending on the language, a test directory contains:

**Python tests:**
```
.github/ci-tests/<name>/
  wendy.json          # App identity and entitlements
  main.py             # Test application
  Dockerfile          # FROM python:3.11-slim
```

**Swift tests:**
```
.github/ci-tests/<name>/
  wendy.json          # App identity and entitlements
  Package.swift
  Sources/<Target>/main.swift
```

**Compose tests:**
```
.github/ci-tests/<name>/
  docker-compose.yml  # No wendy.json needed
```

### `wendy.json` format for CI tests

```json
{
  "appId": "sh.wendy.ci.<name>",
  "platform": "linux",
  "version": "1.0.0",
  "language": "python",
  "entitlements": [...]
}
```

### Test behaviour

- Test apps must print a success message and exit `0` on success.
- Negative/blocking tests (verifying a capability is absent) should exit `1` if the blocked capability accidentally succeeds.
- Each test is single-purpose: it proves exactly one feature works.

### Running integration tests locally

Tests are driven by `go/scripts/test-ci.sh`. The script contains an `ALL_TESTS` array listing every registered test name.

```sh
bash go/scripts/test-ci.sh <name>
```

Refer to the `usage()` block in that script for a full list of available test names and options.

### CI execution

Integration tests run in `.github/workflows/integration-tests.yml` on both macOS and Linux runners.

> **Note:** Swift tests (`swift-*`) run only on the macOS job. The Linux job omits them.

#### Stable release gate

When a release build is triggered with `publish=true`, the full macOS integration test suite runs as a required gate before the `release` and `publish-linux-repos` jobs proceed. If integration tests fail, neither job runs. Nightly builds (triggered by a `push` event or `publish=false`) skip the integration-test gate and proceed directly to release.

#### PR gate for CI config changes

Pull requests from within the repository that modify files under `.github/` automatically trigger a macOS integration test run via `.github/workflows/pr-integration-tests.yml`. Only one run is active per PR at a time; opening a new commit cancels the previous run.

---

## Automated Integration Test Coverage (CI)

When a PR is merged into `main`, the workflow `.github/workflows/integration-test-coverage.yml` automatically checks whether the merged changes introduced new CLI features, device capabilities, entitlements, or deployment modes that are not yet covered by an integration test.

### How it works

1. **Trigger:** Fires on `pull_request` closed events against `main`, but only when the PR was actually merged and does **not** carry the `ai-suggestion` label (to avoid recursive loops).
2. **Diff fetch:** Downloads the full PR diff via the GitHub CLI.
3. **Analysis:** Sends the diff, the PR title/body, all existing CI tests (up to a 20 000-byte budget), `go/scripts/test-ci.sh`, and `.github/workflows/integration-tests.yml` to Claude (Anthropic SDK `anthropic==0.97.0`, model `claude-sonnet-4-6`) with a prompt that instructs it to identify uncovered features and generate the necessary test files.
4. **File writing:** Parses the `<file path="...">...
