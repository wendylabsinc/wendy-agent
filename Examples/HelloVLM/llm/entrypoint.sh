#!/bin/sh
# Downloads the model weights on the device (into the persist volume mounted
# at /models) on first start, then execs llama-server. Files are prefixed
# with the model alias so switching models never reuses a stale cache.
set -eu

MODELS_DIR=/models
mkdir -p "$MODELS_DIR"

fetch() {
    url="$1"
    dest="$2"
    if [ -s "$dest" ]; then
        echo "Using cached $(basename "$dest")"
        return 0
    fi
    echo "Downloading $(basename "$dest") from $url ..."
    curl -fL --retry 5 --retry-delay 5 -C - -o "$dest.partial" "$url"
    mv "$dest.partial" "$dest"
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
