# Testing

Unit tests check the CLI and agent in isolation. Hardware qualification checks
the candidate binaries, deployment paths and applications on the physical rack.
The reusable workflow is `.github/workflows/integration-tests.yml`; fixtures live
under `.github/ci-tests/`.

## Release qualification

A stable CLI/agent release waits for the Linux agents, Linux CLI and macOS CLI
archives from the same workflow run. Hardware tests use those packaged binaries.
They install the candidate agent on each selected device and compare its reported
running executable SHA-256 with the candidate binary before and after the suite.
A version string alone cannot qualify a device. Missing evidence, a different
binary, or a failed authenticated app RPC fails qualification.

Manual runs without `candidate_version` build a stamped source candidate from
the checked-out commit. External reusable-workflow callers must supply an exact
WendyOS `source_ref` SHA. Source qualification verifies that commit's build;
stable release qualification verifies the archives that will be published.

The default rack matrix requires accessible devices in every family below and
coverage of both containerd server major versions 1 and 2. The runtime probe
queries the daemon used by the agent, including a configured socket override.
The installed `ctr` client version is not runtime evidence.

| Required family | Coverage |
| --- | --- |
| Raspberry Pi | A physical Pi |
| NVIDIA Jetson | A physical Jetson |
| Qualcomm | A physical Dragonwing/Qualcomm board |
| Generic Linux x86 | An x86 Linux distribution with the agent installed |

Both macOS and Linux build hosts run against the selected rack for stable
releases. Device jobs share a per-host concurrency lock. Missing required device
families or runtime versions fail discovery. Missing Docker, Python, jq, or the
required macOS Swift toolchain fails runner preparation. These failures do not
become successful skips.

The final `qualification` job requires successful selected jobs and one verified
result for every expected device/build-host pair. It rejects missing, duplicate,
stale and failed results. A skipped or cancelled required device job cannot pass
the release gate.

The GitHub release, agent manifest, installation scripts, package repositories,
AUR and WinGet publication all depend on successful stable qualification,
directly or through the gated release job. Nightly CLI/agent publication retains
its separate policy and does not imply a completed stable rack qualification.

### OS image candidates

The OS builder's hardware qualification passes an exact builder commit, build
version and candidate manifest metadata to the same workflow. The rack installs
the matching OTA candidate for its board and storage configuration. Verification
checks the OTA checksum, root filesystem build identity, and installed/running
agent hashes after boot and again after the tests. A rollback or a different
build with the same displayed OS version fails verification.

This is a representative gate for the whole OS candidate set: Raspberry Pi,
Jetson and Qualcomm must run candidate images, alongside generic Linux x86 and
containerd 1/2 coverage. It does not claim that every board or storage variant
was physically tested. Candidate files and per-version metadata can be published
before testing; advancing public `latest` pointers and publishing an OS release
waits for qualification. PR image publication follows the builder's separate
PR policy.

## Integration fixtures

`go/scripts/test-ci.sh` owns the test registry in `ALL_TESTS`. Default workflow
runs use that registry directly, so adding a fixture does not require copying
its name into a workflow. Use `<language>-<feature>` for application fixtures.

Typical layouts are:

```text
.github/ci-tests/python-example/
  wendy.json
  main.py
  Dockerfile

.github/ci-tests/swift-example/
  wendy.json
  Package.swift
  Sources/Example/main.swift

.github/ci-tests/compose-example/
  docker-compose.yml
```

Python and Swift fixtures declare an app ID such as `sh.wendy.ci.python-example`
in `wendy.json`. Multi-service fixtures declare their services there; Compose
fixtures can use a companion `wendy.json` or the Compose project identity.

### Assertions and verdicts

Fixtures should print `PASS` only after their assertions succeed and exit zero.
On failure they should print `FAIL` and exit nonzero. Assert something the feature
actually changes. For example, `/sys/class` visibility alone cannot prove device
entitlement isolation because containers can see read-only sysfs entries without
access to the underlying device. Pair positive and negative entitlement tests
where appropriate, and verify that flipping the entitlement breaks the test.

The runner requires a successful CLI command and independently checks the app's
terminal state. A nonzero exit code, missing app, running app or crash state
fails. Where the device cannot report an exit code, the runner also requires a
printed verdict. Multi-service and Compose tests run attached with
`--no-restart`, require the exact expected stopped services, and require each
service's `<service>: PASS` verdict. Creating a container with `--deploy`, or
returning from a detached start, does not prove the application passed.

OTEL isolation checks require an authenticated live-device control before
probing ports 4317 and 4318. DNS failures, unreachable networks and broken probes
fail the check instead of being treated as closed ports.

The `host-sensor-loopback` fixture runs an authenticated host check for the
installed image's kernel modules and character devices. Qualcomm images require
video and audio loopback; supported Jetson images require video loopback. This
fixture is part of the default registry. See its
[README](https://github.com/wendylabsinc/wendyos/blob/main/.github/ci-tests/host-sensor-loopback/README.md)
for exact contracts.

Capability-based skips remain explicit. CUDA fixtures require an NVIDIA GPU and
a supported architecture. Swift fixtures need a Swift toolchain on the build
host; the default macOS qualification job requires it before running the suite.
Loopback checks skip known platforms without an image contract and fail unknown
WendyOS identities. A focused manual run may intentionally select only fixtures
that do not apply to that device; it does not establish full rack coverage.

### Running and diagnosing fixtures

```sh
# Use an explicit device and CLI candidate.
bash go/scripts/test-ci.sh -w ./go/bin/wendy -h DEVICE -t python-hello

# Run every applicable fixture.
bash go/scripts/test-ci.sh -w ./go/bin/wendy -h DEVICE
```

The script logs agent version, OS, board and GPU identity before running tests.
Local fixture execution uses the agent already installed on the specified
device; use the qualification workflow when candidate installation and identity
verification are required.

Each fixture has a complete log, including successful output and retry attempts.
Set `WENDY_CI_LOG_DIR` to choose the destination; otherwise the script prints its
temporary log directory. Build/run commands default to a 900-second deadline and
device queries/cleanup to 30 seconds. Override these with
`WENDY_CI_COMMAND_TIMEOUT_SECONDS` and `WENDY_CI_CONTROL_TIMEOUT_SECONDS`.
Timeouts fail the fixture, terminate its child processes and allow remaining
fixtures to run. The suite tracks its apps, verifies cleanup, and retries pending
cleanup on exit. Cleanup failure fails the suite and requires device inspection.

Workflow artifacts retain discovery inventory, candidate identity, preparation
and verification logs, per-test logs and qualification results. Read those
artifacts before retrying a failed rack job. The successful result includes
post-suite candidate verification and the tested containerd server version.

## Tests for the test runner

These checks run locally without rack access, Docker builds or module loads:

```sh
python3 .github/scripts/integration-harness_test.py
python3 .github/scripts/integration-discover_test.py
python3 .github/ci-tests/host-sensor-loopback/check_test.py
python3 .github/scripts/integration-workflow_test.py  # requires PyYAML
```

The mocked runner tests inject failed CLI exits, crash states, missing services,
timeouts, interruptions, broken network probes and cleanup failures. Workflow
tests check artifact dependencies and publication gates, including skipped
qualification jobs. The Go tests workflow runs these checks.

## Shared device leases

GitHub concurrency groups are scoped to one repository. A reusable workflow
called by `wendyos-builder` therefore cannot share a GitHub lock with a nightly
job running in `wendyos`. Qualification, nightly OS updates and standalone
template deployments also run through `.github/scripts/integration-lease.py`.
The lock covers candidate installation, OS reboot, tests, cleanup and post-suite
verification.

The wrapper acquires an atomic directory lock over authenticated HostShell. On
WendyOS it uses `/data/wendy-ci-rack/lease` and requires `/data` to be mounted,
so the lock survives an OS update and reboot. Vendor Linux hosts use
`/var/lib/wendy-ci-rack/lease`. Each owner has a random token directory containing
the base64-encoded GitHub run/job identity. A job waits at most 15 minutes by
default, then fails without touching another owner's lock.

A successful child releases its lease. A normal test failure can also release
when the child creates the `--release-marker` cleanup receipt. After a failed
fixture suite, this requires a fresh `WENDY_CI_CLEANUP_RECEIPT` file from the test
runner confirming that every tracked app was removed. Failed template deployment
keeps the lease for inspection. The wrapper removes previous receipts before
starting.
Signals, timeouts and uncertain preparation failures retain the lease even if a
receipt exists. Leases have no automatic expiry because an OTA or reboot can
continue after its runner disappears.

After a retained lease, inspect the owner and token printed in the workflow log.
Confirm that the owning run and its runner processes have ended, and inspect the
device's update status and test applications before recovery. Confirm that no
OTA, reboot or device command remains active, check device health, and remove
leftover test apps. The owner file is
`<lease-path>/<token>/owner`; decode it with `base64 -d` to identify the run. Once
the device is idle, remove only that owner's file, then use `rmdir` on its token
directory and the outer `lease` directory through authenticated HostShell.
Avoid recursive deletion: `rmdir` fails if another owner's directory exists.
An acquisition interrupted before its token was written can leave an empty
outer directory, which also needs inspection before `rmdir` recovery.
If ownership or device activity is uncertain, leave the lease in place and
investigate.

The lease tests run entirely against local temporary directories and mock
connections:

```sh
python3 .github/scripts/integration-lease_test.py
```

## Automated coverage review

`.github/workflows/integration-test-review.yml` reviews relevant same-repository
PR changes for missing integration coverage. Its suggestions do not execute the
hardware suite or replace the release gate. Apply and validate proposed fixtures
through the registry and verdict rules above. Keep the review prompt aligned
with the runner when registration or applicability changes.
