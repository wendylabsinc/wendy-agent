#!/bin/sh
# Download the 70B Ultravox model and projector on the device, cache them
# in the persist volume, then launch a local llama-server plus the live voice
# service. aria2 leaves a control file for safe interrupted-download resumes.
set -eu

MODELS_DIR=/models
mkdir -p "$MODELS_DIR"

if [ ! -e /dev/nvidia0 ]; then
    echo "error: /dev/nvidia0 is missing; the NVIDIA GPU entitlement is not active" >&2
    exit 1
fi

if [ ! -e /dev/nvidiactl ]; then
    echo "error: /dev/nvidiactl is missing; NVIDIA CDI devices were not mounted" >&2
    exit 1
fi

total_kib=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)
available_kib=$(awk '/^MemAvailable:/ { print $2 }' /proc/meminfo)
total_gib=$((total_kib / 1024 / 1024))
available_gib=$((available_kib / 1024 / 1024))
echo "Unified system memory: ${total_gib} GiB total, ${available_gib} GiB currently available"
echo "Configured CUDA model/runtime budget: ${MEMORY_BUDGET_GIB:-80} GiB"
if [ "$total_gib" -lt "${MIN_SYSTEM_MEMORY_GIB:-100}" ]; then
    echo "error: this profile requires a 128 GB DGX Spark-class system" >&2
    exit 1
fi
if [ "$available_gib" -lt "${MIN_AVAILABLE_MEMORY_GIB:-72}" ]; then
    echo "error: at least ${MIN_AVAILABLE_MEMORY_GIB:-72} GiB must be free before loading the 70B profile" >&2
    exit 1
fi

if command -v nvidia-smi >/dev/null 2>&1; then
    cuda_memory_mib=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | awk 'NR == 1 { print int($1) }')
    if [ -n "$cuda_memory_mib" ]; then
        cuda_memory_gib=$((cuda_memory_mib / 1024))
        echo "CUDA-visible unified memory: ${cuda_memory_gib} GiB"
        if [ "$cuda_memory_gib" -lt "${MIN_CUDA_MEMORY_GIB:-100}" ]; then
            echo "error: CUDA exposes only ${cuda_memory_gib} GiB; expected a 128 GB DGX Spark" >&2
            exit 1
        fi
    fi
    compute_cap=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | awk 'NR == 1 { print $1 }')
    if [ -n "$compute_cap" ]; then
        echo "NVIDIA compute capability: ${compute_cap}"
        case "$compute_cap" in
            12.1*) ;;
            *) echo "warning: this profile is tuned for DGX Spark compute capability 12.1" >&2 ;;
        esac
    fi
fi

fetch() {
    url=$1
    dest=$2

    if [ -s "$dest" ] && [ ! -e "$dest.aria2" ]; then
        echo "Using cached $(basename "$dest")"
        return
    fi

    echo "Downloading $(basename "$dest") ..."
    aria2c \
        --continue=true \
        --max-connection-per-server=16 \
        --split=16 \
        --min-split-size=1M \
        --file-allocation=none \
        --console-log-level=warn \
        --summary-interval=30 \
        --dir="$(dirname "$dest")" \
        --out="$(basename "$dest")" \
        "$url"
    echo "Finished $(basename "$dest")"
}

MODEL_FILE_1=$(basename "$MODEL_URL_1")
MMPROJ_FILE=$(basename "$MMPROJ_URL")
MODEL_PATH="$MODELS_DIR/$MODEL_FILE_1"
MMPROJ_PATH="$MODELS_DIR/$MMPROJ_FILE"

fetch "$MODEL_URL_1" "$MODEL_PATH"
if [ -n "${MODEL_URL_2:-}" ]; then
    MODEL_FILE_2=$(basename "$MODEL_URL_2")
    fetch "$MODEL_URL_2" "$MODELS_DIR/$MODEL_FILE_2"
fi
fetch "$MMPROJ_URL" "$MMPROJ_PATH"

TTS_DIR=${TTS_MODEL_DIR:-/models/kokoro-multi-lang-v1_0}
if [ ! -s "$TTS_DIR/model.onnx" ] || [ ! -s "$TTS_DIR/voices.bin" ] || [ ! -s "$TTS_DIR/tokens.txt" ]; then
    TTS_ARCHIVE="$MODELS_DIR/$(basename "$TTS_MODEL_URL")"
    fetch "$TTS_MODEL_URL" "$TTS_ARCHIVE"
    echo "${TTS_MODEL_SHA256}  ${TTS_ARCHIVE}" | sha256sum -c -
    echo "Installing Kokoro neural voice ..."
    tar -xjf "$TTS_ARCHIVE" -C "$MODELS_DIR"
    rm -f "$TTS_ARCHIVE"
else
    echo "Using cached Kokoro neural voice"
fi

echo "Starting ${MODEL_ALIAS:-ultravox-70b} with ${GPU_LAYERS:-all} CUDA GPU layers"
echo "Open http://<device-hostname>.local:${VOICE_PORT:-8080} and start listening"

# Keep llama-server private; voice_server owns the public UI/API port and
# captures the device microphone through PipeWire or ALSA.
/app/llama-server \
    --model "$MODEL_PATH" \
    --mmproj "$MMPROJ_PATH" \
    --alias "${MODEL_ALIAS:-ultravox-70b}" \
    --host 127.0.0.1 \
    --port "${LLAMA_PORT:-8081}" \
    --n-gpu-layers "${GPU_LAYERS:-all}" \
    --ctx-size "${CONTEXT_SIZE:-16384}" \
    --parallel 1 \
    --cache-ram 0 \
    --flash-attn "${FLASH_ATTENTION:-on}" \
    --no-mmap &

llama_pid=$!
trap 'kill "$llama_pid" 2>/dev/null || true' EXIT INT TERM

python3 /usr/local/bin/dgx-spark-speechllm/voice_server.py
