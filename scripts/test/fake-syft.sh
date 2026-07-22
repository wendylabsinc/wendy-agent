#!/usr/bin/env bash
# Fake syft for tests. Emits a minimal SPDX-JSON doc to stdout.
# If FAKE_SYFT_FAIL=1, exits non-zero to simulate a scan failure.
set -euo pipefail
if [[ "${FAKE_SYFT_FAIL:-0}" == "1" ]]; then
  echo "fake-syft: simulated failure" >&2
  exit 1
fi
# Emit a minimal valid SPDX-JSON stub regardless of args; tests only assert
# that a well-formed document was written, not its contents.
printf '{"spdxVersion":"SPDX-2.3","name":"fake","packages":[]}\n'
