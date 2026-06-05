# Handover: temporarily soft-fail Swift E2E PR workflow

## Worktree

```sh
cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.swift-e2e-soft-fail
```

Branch:

```text
kb.swift-e2e-soft-fail
```

Base:

```text
main @ 23187f33
```

## Goal

Temporarily make the Swift E2E GitHub Actions workflow **officially succeed** even when the E2E tests themselves are flaky/failing, while still posting a PR note/comment that clearly reports whether Swift E2E passed or failed.

This is intended as a short-term CI stability measure, not a permanent lowering of test standards.

## Relevant workflow

Primary file:

```text
.github/workflows/swift-e2e-tests.yml
```

Important areas:

- `swift-e2e-tests` matrix job runs each route via:

```yaml
- uses: ./.github/actions/swift-e2e-run
```

- `swift-e2e-analyze` job currently has:

```yaml
if: ${{ always() && !cancelled() }}
needs: swift-e2e-tests
```

- Analysis job downloads artifacts, aggregates, performs AI review, renders report, uploads report, and posts PR review/comment.

Composite action:

```text
.github/actions/swift-e2e-run/action.yml
```

It currently runs the actual E2E script directly:

```sh
bash ./Scripts/E2ETest.sh \
  --output-dir "$output_dir" \
  --agent-address "${{ inputs.device_address }}"
```

and PowerShell equivalent on Windows.

## Desired behavior

For PRs:

1. Swift E2E workflow/check should complete with a successful GitHub status.
2. Actual E2E failures should not be hidden:
   - Post/update a PR comment saying whether Swift E2E passed or failed.
   - Include enough detail/link to the report/artifacts/logs for reviewers.
3. If the workflow infrastructure itself is broken in a way that prevents posting/reporting, decide whether that should still fail. Recommended: keep true infrastructure/config failures visible, but soft-fail test execution failures.

## Suggested implementation approach

Keep the change small and explicit.

### Option A: soften only the route execution step

In `.github/actions/swift-e2e-run/action.yml`:

- Let the E2E command run and capture its exit code.
- Write an output or small status file into the attempt/output directory.
- Do **not** exit non-zero from the composite action for test failures.

Potential Unix sketch:

```sh
set +e
bash ./Scripts/E2ETest.sh \
  --output-dir "$output_dir" \
  --agent-address "${{ inputs.device_address }}"
status=$?
set -e

mkdir -p "$output_dir"
printf '%s\n' "$status" > "$output_dir/e2e-exit-code.txt"

if [[ "$status" -eq 0 ]]; then
  echo "Swift E2E route passed."
else
  echo "::warning::Swift E2E route failed with exit code $status; soft-failing temporarily."
fi
exit 0
```

Need equivalent PowerShell handling.

Caveat: if `E2ETest.sh` fails before writing normal attempt artifacts, upload/report steps may have less data. The status file helps preserve at least the failure signal.

### Option B: let matrix fail, but make workflow overall pass

Use `continue-on-error: true` at the matrix job or step level, then have `swift-e2e-analyze` inspect `needs.swift-e2e-tests.result` and post a note. This is smaller, but GitHub UI may still show confusing job-level failure/neutral behavior depending on placement. Verify desired status semantics.

## Existing PR context that motivated this

PR #874 changed Go CLI behavior used by Swift E2E, but Swift E2E did not run because the workflow path filter did not include `go/**`.

A separate draft PR was created to broaden path filters:

```text
https://github.com/wendylabsinc/WendyOS/pull/877
```

That PR adds `go/**` to `.github/workflows/swift-e2e-tests.yml`.

This new soft-fail work should be independent and based on `main`.

## Acceptance criteria

- Workflow status is green/successful even when E2E test execution fails.
- PR receives a clear comment/note with pass/fail status.
- Logs still include warnings for failed E2E routes.
- Artifacts/report are still uploaded when available.
- The temporary nature is obvious in comments/names/messages.

## Useful commands

Inspect workflow:

```sh
read .github/workflows/swift-e2e-tests.yml
read .github/actions/swift-e2e-run/action.yml
```

Validate YAML shape after edits:

```sh
ruby -e 'require "yaml"; YAML.load_file(".github/workflows/swift-e2e-tests.yml"); YAML.load_file(".github/actions/swift-e2e-run/action.yml"); puts "ok"'
```

Check diff:

```sh
git diff -- .github/workflows/swift-e2e-tests.yml .github/actions/swift-e2e-run/action.yml
```

Commit suggestion:

```text
ci: temporarily soft-fail Swift E2E test results
```
