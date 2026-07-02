# MLX-on-Jetson feasibility probe: builds the MLXProbe CLI (Swift, MLX text
# generation) natively for arm64 with the CUDA backend of
# wendylabsinc/mlx-swift, as wired by wendylabsinc/mlx-swift-lm.
#
# Built ON an arm64 host (the Jetson itself, or Apple Container on a Mac —
# the CUDA compile does not need a GPU, only the toolkit). There is no
# cross-compile path; upstream CI builds natively too.
#
# Thor (sm_110) notes:
#  - CUDA 13 is required to target sm_110 directly; the base image ARG
#    defaults to a CUDA 13 devel image. If that tag is unavailable, CUDA
#    12.9 works via PTX forward-compat: set CUDA_ARCH=compute_90 and let
#    the driver JIT (slower first run).
#  - MLX JIT-compiles kernels at runtime via nvrtc, so the runtime image
#    keeps the devel toolkit (nvcc + nvrtc). Big image, simple probe.

ARG BASE_IMAGE=docker.io/nvidia/cuda:13.0.0-devel-ubuntu24.04
FROM ${BASE_IMAGE}

ARG SWIFT_VERSION=swift-6.3-RELEASE
ARG CUDA_ARCH=sm_110

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    binutils build-essential cmake curl git gnupg2 \
    libblas-dev libc6-dev libcurl4-openssl-dev libedit2 \
    liblapack-dev liblapacke-dev libncurses-dev libopenblas-dev \
    libpython3-dev libsqlite3-0 libxml2-dev libz3-dev \
    ninja-build pkg-config python3 python3-pip tzdata unzip zip zlib1g-dev \
    ca-certificates aria2 \
    && rm -rf /var/lib/apt/lists/*

# cuDNN for the matching CUDA major; package name differs per CUDA version.
# (libcudnn9-dev-cuda-13 for CUDA 13, libcudnn9-dev-cuda-12 for 12.x.)
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    libcudnn9-dev-cuda-13 || apt-get install -y --no-install-recommends libcudnn9-dev-cuda-12; \
    rm -rf /var/lib/apt/lists/*

# Swift toolchain (aarch64). ubuntu24.04 builds; download.swift.org layout.
RUN set -ex; \
    SWIFT_PLATFORM="ubuntu24.04"; \
    SWIFT_BRANCH=$(echo "${SWIFT_VERSION}" | tr '[:upper:]' '[:lower:]'); \
    SWIFT_WEBDIR="https://download.swift.org/${SWIFT_BRANCH}/$(echo ${SWIFT_PLATFORM} | tr -d .)-aarch64"; \
    curl -fsSL "${SWIFT_WEBDIR}/${SWIFT_VERSION}/${SWIFT_VERSION}-${SWIFT_PLATFORM}-aarch64.tar.gz" \
      | tar xzf - --directory / --strip-components=1; \
    swift --version

# Model downloads at runtime use the huggingface CLI (MLX checkpoints are
# multi-file safetensors directories).
RUN pip3 install --break-system-packages --no-cache-dir "huggingface_hub[cli]"

WORKDIR /probe
COPY probe/Package.swift ./
COPY probe/Sources ./Sources

ENV SPM_CUDA=1
ENV CUDA_ARCH=${CUDA_ARCH}
ENV PATH=/usr/local/cuda/bin:${PATH}
ENV LD_LIBRARY_PATH=/usr/local/cuda/lib64:${LD_LIBRARY_PATH}

RUN swift build -c release && cp .build/release/MLXProbe /usr/local/bin/MLXProbe

COPY entrypoint.sh /usr/local/bin/probe-entrypoint
RUN chmod +x /usr/local/bin/probe-entrypoint

ENTRYPOINT ["/usr/local/bin/probe-entrypoint"]
