# GPU ES trainer: CUDA base + NVIDIA Warp + mujoco_warp + CuPy, on the proven
# Spark recipe (see Examples/G1GpuProbe). The WarpBackend runs the ES population
# as batched GPU worlds with a CUDA-graph-captured physics step.
#
# NOTE: no ghcr.io / uv here — wendy's buildx cannot reliably resolve ghcr.io in
# this environment, so we use the base image's apt python3.12 + pip (docker.io +
# PyPI + pypi.nvidia.com only).
FROM nvidia/cuda:12.8.0-runtime-ubuntu24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip python3-venv git curl \
    libegl1 libgl1 libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN python3 -m venv /app/.venv
ENV VIRTUAL_ENV=/app/.venv PATH=/app/.venv/bin:$PATH

# Warp (NVIDIA index) + mujoco_warp + mujoco + CuPy (batched MLP on GPU, reuses
# the base image's CUDA runtime) + model fetcher.
RUN pip install --no-cache-dir \
      --extra-index-url https://pypi.nvidia.com \
      "warp-lang>=1.14.0" "mujoco~=3.10.0" "mujoco-warp~=3.10.0,>=3.10.0.1" \
      cupy-cuda12x robot_descriptions "numpy<2.5"

# Vendor the G1 model (build has network; the mesh runtime does not).
RUN python -c "import os,shutil; from robot_descriptions import g1_mj_description as g; shutil.copytree(os.path.dirname(g.MJCF_PATH),'/opt/g1_model',dirs_exist_ok=True); open('/opt/g1_model/.mjcf_name','w').write(os.path.basename(g.MJCF_PATH)); print('vendored',g.MJCF_PATH)"

ENV G1_MODEL_DIR=/opt/g1_model MUJOCO_GL=egl PYTHONUNBUFFERED=1
COPY g1fleet/ ./g1fleet/
COPY app.py .
EXPOSE 8080
CMD ["python", "app.py"]
