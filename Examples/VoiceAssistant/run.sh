#!/usr/bin/env bash
# Convenience wrapper: load .env, then deploy with `wendy run`.
set -euo pipefail

demo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ -f "$demo_dir/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$demo_dir/.env"
  set +a
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is missing; copy .env.example to .env and add your key" >&2
  exit 2
fi

cd "$demo_dir"
exec wendy run "$@"
