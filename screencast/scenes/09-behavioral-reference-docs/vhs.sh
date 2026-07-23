#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "${SCREENCAST_DIR:?}/.." && pwd)"
cd "$repo_root/swift"
rm -rf Build/Reference
mkdir -p Build/Reference
(
  cd WendyE2ETests
  swift run swift-e2e-testing reference \
    --format html \
    --output ../Build/Reference \
    Tests/WendyE2ETests >/dev/null
)

test -f Build/Reference/index.html
page_count="$(find Build/Reference -maxdepth 1 -name '*.html' | wc -l | tr -d ' ')"
test "$page_count" -eq 110
