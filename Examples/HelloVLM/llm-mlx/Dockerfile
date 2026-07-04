# MLX backend for HelloVLM — drop-in alternative to llm/ (llama.cpp).
#
# Same serviceName ("llm") as llm/, so deploying one replaces the other:
# the app keeps talking to localhost:11434 either way.
#
# The base image carries the prebuilt MLX server for NVIDIA arm64
# (see experiments/mlx-jetson-probe/runtime/ for how it is produced —
# mlx-swift has no official prebuilt binaries for CUDA/arm64 yet, so we
# build them). Until it is published to a registry, load it into your
# builder's local store: `docker load -i mlx-server-0.1.tar` or
# `container image load --input mlx-server-0.1.tar`.
#
# The entrypoint downloads the MLX checkpoint on-device into the persist
# volume on first start (override with MLX_MODEL), then serves an
# OpenAI-compatible API on :11434.
FROM mlx-server:0.1

ENV MLX_MODEL=mlx-community/gemma-3-27b-it-qat-4bit
