#!/bin/sh
# Downloads an MLX checkpoint into the persist volume on first start, then
# runs the probe. HF_MODEL is an MLX-format repo (safetensors directory).
# gemma-3-4b-it-4bit gives a direct comparison with the llama.cpp numbers
# measured for HelloVLM on AGX Thor (~56 tok/s decode).
set -eu

MODELS_DIR=/models
HF_MODEL="${HF_MODEL:-mlx-community/gemma-3-4b-it-4bit}"
MODEL_DIR="$MODELS_DIR/$(basename "$HF_MODEL")"

mkdir -p "$MODELS_DIR"
if [ -s "$MODELS_DIR/.hf-token" ]; then
    export HF_TOKEN="$(cat "$MODELS_DIR/.hf-token")"
fi

if [ ! -s "$MODEL_DIR/config.json" ]; then
    echo "Downloading $HF_MODEL to $MODEL_DIR ..."
    hf download "$HF_MODEL" --local-dir "$MODEL_DIR"
fi

exec MLXProbe --model-path "$MODEL_DIR" "$@"
