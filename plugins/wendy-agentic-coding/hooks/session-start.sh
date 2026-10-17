#!/bin/sh
# Reports the Wendy CLI version when a Claude Code session opens in a Wendy project.
[ -f "${CLAUDE_PROJECT_DIR:-.}/wendy.json" ] || exit 0
if command -v wendy >/dev/null 2>&1; then
  printf 'Wendy project. Wendy CLI: %s\n' "$(WENDY_ANALYTICS=false wendy --version 2>/dev/null)"
else
  printf 'Wendy project, but the Wendy CLI is not installed. Offer /wendy-install to set it up.\n'
fi
