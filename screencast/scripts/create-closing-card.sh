#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

export TITLE_CARD_METADATA="${CLOSING_CARD_METADATA:-$PROJECT_DIR/closing-card.env}"
export TITLE_CARD_SECONDS="${CLOSING_CARD_SECONDS:-4}"

exec "$SCRIPT_DIR/create-title-card.sh" \
  "$PROJECT_DIR/closing-card.svg" \
  "$PROJECT_DIR/closing-card.png" \
  "$PROJECT_DIR/recordings/99-closing-card.mp4"
