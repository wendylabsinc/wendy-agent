#!/usr/bin/env bash
set -euo pipefail

cd "${SCREENCAST_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"

# Verify the commands demonstrated by vhs.tape from the real screencast root.
test -x scripts/render-slide
test -x scripts/render-voice
test -x scripts/render-tape
test -x scripts/stitch

scripts/render-voice --dry-run template/scenes/01-title >/dev/null
scripts/render-tape --dry-run --with-hooks template/scenes/06-render-pipeline >/dev/null
scripts/stitch --help >/dev/null
