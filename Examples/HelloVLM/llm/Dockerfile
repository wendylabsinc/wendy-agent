# VLM backend for HelloVLM: llama.cpp's llama-server, taken from the
# official Ollama image (which bundles CUDA 13 builds that support recent
# Jetson GPUs such as AGX Thor, sm_110).
#
# We run llama-server directly instead of `ollama serve` because Ollama
# disables vision-projector GPU offload on integrated GPUs (every Jetson),
# which makes VLM prompt processing ~10x slower. llama-server offloads the
# projector fine and speaks the same OpenAI-compatible API.
#
# The model weights are baked into the image at build time (Ollama's
# llama-server is built without HTTPS support, so it cannot download
# models itself). Docker layer caching means they download once.

FROM alpine:3.20 AS model
RUN apk add --no-cache curl
RUN curl -fL -o /gemma-3-4b-it-Q4_K_M.gguf \
    https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf
RUN curl -fL -o /mmproj-model-f16.gguf \
    https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/mmproj-model-f16.gguf

FROM docker.io/ollama/ollama:latest

COPY --from=model /gemma-3-4b-it-Q4_K_M.gguf /opt/models/gemma-3-4b-it-Q4_K_M.gguf
COPY --from=model /mmproj-model-f16.gguf /opt/models/mmproj-model-f16.gguf

ENV LD_LIBRARY_PATH=/usr/lib/ollama:/usr/lib/ollama/cuda_v13:/usr/local/nvidia/lib:/usr/local/nvidia/lib64
ENV GGML_BACKEND_PATH=/usr/lib/ollama/cuda_v13/libggml-cuda.so

EXPOSE 11434

ENTRYPOINT ["/usr/lib/ollama/llama-server"]
CMD [ \
    "--model", "/opt/models/gemma-3-4b-it-Q4_K_M.gguf", \
    "--mmproj", "/opt/models/mmproj-model-f16.gguf", \
    "--alias", "gemma-3-4b-it", \
    "--host", "0.0.0.0", \
    "--port", "11434", \
    "-ngl", "999", \
    "-c", "8192", \
    "-np", "1", \
    "--no-webui" \
]
