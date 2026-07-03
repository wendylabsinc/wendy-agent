#!/bin/bash
# Downloads the MLX checkpoint on-device (once, into the persist volume),
# then serves it. Mirrors the llm/ entrypoint's approach: aria2 for
# parallel downloads, optional HF token at /models/.hf-token.
set -euo pipefail

MODEL_ID="${MLX_MODEL:-mlx-community/gemma-3-4b-it-4bit}"
ALIAS="$(basename "$MODEL_ID")"
DIR="/models/mlx/$ALIAS"

if [ ! -f "$DIR/config.json" ]; then
    echo "Downloading $MODEL_ID to $DIR ..."
    mkdir -p "$DIR"
    python3 - "$MODEL_ID" > /tmp/files.txt <<'PY'
import json, sys, urllib.request
model_id = sys.argv[1]
request = urllib.request.Request(f"https://huggingface.co/api/models/{model_id}/tree/main")
try:
    token = open("/models/.hf-token").read().strip()
    request.add_header("Authorization", f"Bearer {token}")
except FileNotFoundError:
    pass
for entry in json.load(urllib.request.urlopen(request)):
    if entry["type"] == "file":
        print(entry["path"])
PY
    HEADER=()
    if [ -f /models/.hf-token ]; then
        HEADER=(--header="Authorization: Bearer $(cat /models/.hf-token)")
    fi
    while read -r file; do
        aria2c -x8 -s8 -c -d "$DIR" -o "$file" "${HEADER[@]}" \
            "https://huggingface.co/$MODEL_ID/resolve/main/$file"
    done < /tmp/files.txt
    echo "Download complete."
fi

exec /usr/local/bin/MLXServer --model-path "$DIR" --port 11434
