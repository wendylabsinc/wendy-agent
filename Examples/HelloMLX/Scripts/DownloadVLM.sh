#!/usr/bin/env bash
# DownloadVLM.sh
#
# Downloads a selected MLX vision-language model for HelloMLX and points
# Models/Current at that model. The app, wendy.json, and Xcode scheme all use
# the stable "Current" path so the selected tier stays in sync without editing
# configuration files.
#
# Usage:
#   Scripts/DownloadVLM.sh <small|medium|large|xlarge>
#
# Requirements:
#   pip install huggingface_hub   (provides the hf command)

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  Scripts/DownloadVLM.sh <small|medium|large|xlarge>

Choose a model tier explicitly:
  small   8 GB Macs     mlx-community/SmolVLM-500M-Instruct-4bit        (~0.3 GiB)
  medium  16 GB Macs    mlx-community/Qwen2-VL-2B-Instruct-4bit         (~1.2 GiB)
  large   32 GB Macs    mlx-community/Qwen2.5-VL-3B-Instruct-4bit       (~2.9 GiB)
  xlarge  64 GB Macs    mlx-community/gemma-3-27b-it-qat-4bit           (~15.7 GiB)

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
        HF_REPO="mlx-community/SmolVLM-500M-Instruct-4bit"
        MODEL_DIR="SmolVLM-500M-Instruct-4bit"
        SIZE_HINT="~0.3 GiB"
        MEMORY_HINT="recommended starting point for 8 GB Macs"
        ;;
    medium)
        HF_REPO="mlx-community/Qwen2-VL-2B-Instruct-4bit"
        MODEL_DIR="Qwen2-VL-2B-Instruct-4bit"
        SIZE_HINT="~1.2 GiB"
        MEMORY_HINT="recommended starting point for 16 GB Macs"
        ;;
    large)
        HF_REPO="mlx-community/Qwen2.5-VL-3B-Instruct-4bit"
        MODEL_DIR="Qwen2.5-VL-3B-Instruct-4bit"
        SIZE_HINT="~2.9 GiB"
        MEMORY_HINT="recommended starting point for 32 GB Macs"
        ;;
    xlarge)
        HF_REPO="mlx-community/gemma-3-27b-it-qat-4bit"
        MODEL_DIR="gemma-3-27b-it-qat-4bit"
        SIZE_HINT="~15.7 GiB"
        MEMORY_HINT="for high-memory Macs; use 64 GB unified memory or more"
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

# ── download ──────────────────────────────────────────────────────────────────

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Tier  : $TIER ($MEMORY_HINT)"
echo "  Model : $HF_REPO"
echo "  Size  : $SIZE_HINT (4-bit quantised MLX weights)"
echo "  Dest  : $DEST"
echo "  Link  : $CURRENT_LINK -> $MODEL_DIR"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

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
