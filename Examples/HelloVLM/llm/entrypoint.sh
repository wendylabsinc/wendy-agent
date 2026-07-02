#!/bin/sh
# Downloads the model weights on the device (into the persist volume mounted
# at /models) on first start, then execs llama-server. Files are prefixed
# with the model alias so switching models never reuses a stale cache.
#
# aria2c fetches with parallel connections: Hugging Face throttles per
# connection (~2 MB/s each was measured), so a single-stream download of a
# 27B model takes hours while 16 streams saturate the line. Drop a token
# into /models/.hf-token to authenticate (needed for gated models).
set -eu

MODELS_DIR=/models
mkdir -p "$MODELS_DIR"
rm -f "$MODELS_DIR"/*.partial

AUTH_ARGS=""
if [ -s "$MODELS_DIR/.hf-token" ]; then
    AUTH_ARGS="--header=Authorization: Bearer $(cat "$MODELS_DIR/.hf-token")"
    echo "Using Hugging Face token from /models/.hf-token"
fi

fetch() {
    url="$1"
    dest="$2"
    if [ -s "$dest" ] && [ ! -e "$dest.aria2" ]; then
        echo "Using cached $(basename "$dest")"
        return 0
    fi
    echo "Downloading $(basename "$dest") from $url ..."
    if [ -n "$AUTH_ARGS" ]; then
        aria2c -x16 -s16 -k1M -c --file-allocation=none --console-log-level=warn \
            --summary-interval=30 --header="Authorization: Bearer $(cat "$MODELS_DIR/.hf-token")" \
            -d "$(dirname "$dest")" -o "$(basename "$dest")" "$url"
    else
        aria2c -x16 -s16 -k1M -c --file-allocation=none --console-log-level=warn \
            --summary-interval=30 \
            -d "$(dirname "$dest")" -o "$(basename "$dest")" "$url"
    fi
    echo "Finished $(basename "$dest")"
}

MODEL_PATH="$MODELS_DIR/$MODEL_ALIAS-$(basename "$MODEL_URL")"
MMPROJ_PATH="$MODELS_DIR/$MODEL_ALIAS-$(basename "$MMPROJ_URL")"

fetch "$MODEL_URL" "$MODEL_PATH"
fetch "$MMPROJ_URL" "$MMPROJ_PATH"

exec /usr/lib/ollama/llama-server \
    --model "$MODEL_PATH" \
    --mmproj "$MMPROJ_PATH" \
    --alias "$MODEL_ALIAS" \
    --host 0.0.0.0 \
    --port 11434 \
    -ngl 999 \
    -c 8192 \
    -np 1 \
    --no-webui
