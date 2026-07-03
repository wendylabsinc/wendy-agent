# Lean runtime image for MLXServer on NVIDIA arm64 (Jetson Thor et al.).
# Built natively on an arm64 device (or CI arm64 runner) — the binary and
# the Swift runtime libs are provided by the build context, produced by
# the dev-container build documented in ../README.md.
#
#   context/
#     MLXServer      <- swift build -c release artifact
#     swift-linux/   <- /usr/lib/swift/linux from the build toolchain
FROM nvidia/cuda:13.0.0-runtime-ubuntu24.04

# cuda-cccl + cudart-dev are NOT build leftovers: MLX compiles its GPU
# kernels at runtime via nvrtc, which needs these headers on the device.
RUN apt-get update && apt-get install -y --no-install-recommends \
        libcudnn9-cuda-13 cuda-nvrtc-13-0 cuda-cccl-13-0 cuda-cudart-dev-13-0 \
        libgstreamer1.0-0 gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
        libopenblas0 liblapack3 libblas3 libgfortran5 libffi8 libcurl4 \
        ca-certificates aria2 python3 \
    && rm -rf /var/lib/apt/lists/*

COPY swift-linux/linux/ /usr/lib/swift/linux/
RUN echo /usr/lib/swift/linux > /etc/ld.so.conf.d/swift.conf && ldconfig

COPY MLXServer /usr/local/bin/MLXServer
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /usr/local/bin/MLXServer

ENV MLX_MODEL=mlx-community/gemma-3-4b-it-4bit
EXPOSE 11434
ENTRYPOINT ["/entrypoint.sh"]
