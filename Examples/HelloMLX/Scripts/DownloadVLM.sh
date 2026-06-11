#!/usr/bin/env bash
# DownloadVLM.sh
#
# Downloads a selected MLXVLM-compatible vision-language model for HelloMLX
# and points Models/Current at that model. The app, wendy.json, and Xcode
# scheme all use the stable "Current" path so the selected tier stays in sync
# without editing configuration files.
#
# Usage:
#   Scripts/DownloadVLM.sh <small|medium|large>
#
# Requirements:
#   pip install huggingface_hub   (provides the hf command)

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  Scripts/DownloadVLM.sh <small|medium|large>

Choose a model tier explicitly. Each tier uses a model supported by the
current MLXVLM dependency in this example:
  small   16 GB Macs    mlx-community/Qwen2-VL-2B-Instruct-4bit         (~1.2 GiB)
  medium  32 GB Macs    mlx-community/Qwen2.5-VL-3B-Instruct-4bit       (~2.9 GiB)
  large   64 GB Macs    mlx-community/gemma-3-27b-it-qat-4bit           (~15.7 GiB)

Quality note:
  small and medium are low-quality validation tiers for constrained Macs.
  Use a Mac with lots of RAM and the large model when you need the demo
  to actually do the job well.

The selected model is downloaded under Models/<model-dir>, then Models/Current
is updated to point at it. wendy.json deploys Models/Current and the app runs
with --model-path Current.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

if [[ $# -ne 1 ]]; then
    usage >&2
    exit 64
fi

# ── Homebrew bootstrap ────────────────────────────────────────────────────────

if [[ -x /opt/homebrew/bin/brew ]]; then
    eval "$(/opt/homebrew/bin/brew shellenv bash)"
elif command -v brew &>/dev/null; then
    eval "$(brew shellenv bash)"
fi

# ── configuration ─────────────────────────────────────────────────────────────

TIER="$1"
case "$TIER" in
    small)
        HF_REPO="mlx-community/Qwen2-VL-2B-Instruct-4bit"
        MODEL_DIR="Qwen2-VL-2B-Instruct-4bit"
        SIZE_HINT="~1.2 GiB"
        MEMORY_HINT="recommended starting point for 16 GB Macs"
        QUALITY_WARNING="low-quality validation tier; use large for best results"
        ;;
    medium)
        HF_REPO="mlx-community/Qwen2.5-VL-3B-Instruct-4bit"
        MODEL_DIR="Qwen2.5-VL-3B-Instruct-4bit"
        SIZE_HINT="~2.9 GiB"
        MEMORY_HINT="recommended starting point for 32 GB Macs"
        QUALITY_WARNING="low-quality validation tier; use large for best results"
        ;;
    large)
        HF_REPO="mlx-community/gemma-3-27b-it-qat-4bit"
        MODEL_DIR="gemma-3-27b-it-qat-4bit"
        SIZE_HINT="~15.7 GiB"
        MEMORY_HINT="for high-memory Macs; use 64 GB unified memory or more"
        QUALITY_WARNING=""
        ;;
    *)
        echo "❌  Unknown model tier: $TIER" >&2
        echo "" >&2
        usage >&2
        exit 64
        ;;
esac

# ── locate destination ────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODELS_ROOT="$PROJECT_ROOT/Models"
DEST="$MODELS_ROOT/$MODEL_DIR"
CURRENT_LINK="$MODELS_ROOT/Current"

# ── check dependencies ────────────────────────────────────────────────────────

if ! command -v hf &>/dev/null; then
    echo "❌  hf not found."
    echo ""
    echo "Install it with:"
    echo "    pip install huggingface_hub"
    echo ""
    echo "Or, if you use Homebrew Python:"
    echo "    pip3 install huggingface_hub"
    exit 1
fi

if ! command -v python3 &>/dev/null; then
    echo "❌  python3 not found."
    echo ""
    echo "python3 is required to write Models/Current/info.json metadata."
    exit 1
fi

# ── download ──────────────────────────────────────────────────────────────────

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Tier  : $TIER ($MEMORY_HINT)"
echo "  Model : $HF_REPO"
echo "  Size  : $SIZE_HINT (4-bit quantised MLX weights)"
echo "  Dest  : $DEST"
echo "  Link  : $CURRENT_LINK -> $MODEL_DIR"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

if [[ -n "$QUALITY_WARNING" ]]; then
    echo "⚠️   Quality warning: $TIER is a $QUALITY_WARNING."
    echo ""
fi

mkdir -p "$DEST"

# Step 1 – populate the HF cache (no-op if already cached).
# This means a subsequent `git clean -fdx` won't require a re-download;
# the next run will copy from the cache instead of hitting the network.
hf download "$HF_REPO"

# Step 2 – copy from cache into the project's Models/ directory.
hf download \
    "$HF_REPO" \
    --local-dir "$DEST"

# Remove the .cache/ metadata folder created by hf; it's not part of the
# model and is not needed by MLX or the app.
rm -rf "$DEST/.cache"

MODEL_SIZE_BYTES="$(python3 - "$DEST" <<'PY'
import os
import sys

root = sys.argv[1]
total = 0
for directory, _, filenames in os.walk(root):
    for filename in filenames:
        path = os.path.join(directory, filename)
        try:
            total += os.path.getsize(path)
        except OSError:
            pass
print(total)
PY
)"

python3 - "$DEST/info.json" "$TIER" "$HF_REPO" "$MODEL_DIR" "$SIZE_HINT" "$MODEL_SIZE_BYTES" "$MEMORY_HINT" <<'PY'
import datetime
import json
import sys

info_path, model_class, hf_repo, model_dir, size_hint, size_bytes, memory_hint = sys.argv[1:]
metadata = {
    "class": model_class,
    "huggingFaceId": hf_repo,
    "directory": model_dir,
    "size": {
        "hint": size_hint,
        "bytes": int(size_bytes),
    },
    "memoryHint": memory_hint,
    "generatedAt": datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z"),
}
with open(info_path, "w", encoding="utf-8") as f:
    json.dump(metadata, f, indent=2)
    f.write("\n")
PY

if [[ -e "$CURRENT_LINK" && ! -L "$CURRENT_LINK" ]]; then
    echo "❌  $CURRENT_LINK exists and is not a symlink. Move it aside before selecting a model." >&2
    exit 1
fi

rm -f "$CURRENT_LINK"
ln -s "$MODEL_DIR" "$CURRENT_LINK"

echo ""
echo "✅  Model downloaded to:"
echo "    $DEST"
echo ""
echo "✅  Current model selected:"
echo "    $CURRENT_LINK -> $MODEL_DIR"
