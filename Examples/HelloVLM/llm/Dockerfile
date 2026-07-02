# VLM backend for HelloVLM: llama.cpp's llama-server, taken from the
# official Ollama image (which bundles CUDA 13 builds that support recent
# Jetson GPUs such as AGX Thor, sm_110).
#
# We run llama-server directly instead of `ollama serve` because Ollama
# disables vision-projector GPU offload on integrated GPUs (every Jetson),
# which makes VLM prompt processing ~10x slower. llama-server offloads the
# projector fine and speaks the same OpenAI-compatible API.
#
# The model weights are downloaded ON THE DEVICE at first start (see
# entrypoint.sh) into the `hellovlm-models` persist volume, so they never
# transit the developer machine and survive redeploys. Change MODEL_URL /
# MMPROJ_URL / MODEL_ALIAS to switch models; tiers mirroring HelloMLX's
# DownloadVLM.sh:
#   fast    ggml-org/gemma-3-4b-it-GGUF   Q4_K_M (~3.3 GB, ~55 tok/s on Thor)
#   large   ggml-org/gemma-3-27b-it-GGUF  Q4_K_M (~16.5 GB) — same model as
#           HelloMLX's `large` tier
FROM docker.io/ollama/ollama:latest

RUN apt-get update \
    && apt-get install -y --no-install-recommends aria2 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ENV LD_LIBRARY_PATH=/usr/lib/ollama:/usr/lib/ollama/cuda_v13:/usr/local/nvidia/lib:/usr/local/nvidia/lib64
ENV GGML_BACKEND_PATH=/usr/lib/ollama/cuda_v13/libggml-cuda.so

ENV MODEL_URL=https://huggingface.co/ggml-org/gemma-3-27b-it-GGUF/resolve/main/gemma-3-27b-it-Q4_K_M.gguf
ENV MMPROJ_URL=https://huggingface.co/ggml-org/gemma-3-27b-it-GGUF/resolve/main/mmproj-model-f16.gguf
ENV MODEL_ALIAS=gemma-3-27b-it

COPY entrypoint.sh /usr/local/bin/hellovlm-llm-entrypoint
RUN chmod +x /usr/local/bin/hellovlm-llm-entrypoint

EXPOSE 11434

ENTRYPOINT ["/usr/local/bin/hellovlm-llm-entrypoint"]
