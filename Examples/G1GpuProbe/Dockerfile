# GPU probe: proven-on-Spark recipe borrowed from ~/git/wendy/mjlab
#   base nvidia/cuda:12.8.0-runtime (backward-compatible with the Sparks'
#   CUDA-13 Blackwell driver), NVIDIA Warp from pypi.nvidia.com, mujoco_warp.
FROM nvidia/cuda:12.8.0-runtime-ubuntu24.04
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl libegl1 libgl1 libglib2.0-0 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

ENV UV_LINK_MODE=copy UV_PYTHON_PREFERENCE=only-managed
RUN uv python install 3.12
WORKDIR /app
RUN uv venv --python 3.12 /app/.venv
ENV VIRTUAL_ENV=/app/.venv PATH=/app/.venv/bin:$PATH

# warp-lang from the NVIDIA index (the CUDA-enabled build mjlab pins); the rest
# from PyPI. Kept in one resolve so uv picks a consistent set.
RUN --mount=type=cache,target=/root/.cache/uv \
    uv pip install --python /app/.venv/bin/python \
      --extra-index-url https://pypi.nvidia.com \
      "warp-lang>=1.14.0" "mujoco~=3.10.0" "mujoco-warp~=3.10.0,>=3.10.0.1" robot_descriptions

# Vendor the G1 model (build has network; runtime may not).
RUN python -c "import os,shutil; from robot_descriptions import g1_mj_description as g; shutil.copytree(os.path.dirname(g.MJCF_PATH),'/opt/g1_model',dirs_exist_ok=True); open('/opt/g1_model/.mjcf_name','w').write(os.path.basename(g.MJCF_PATH)); print('vendored',g.MJCF_PATH)"

ENV G1_MODEL_DIR=/opt/g1_model MUJOCO_GL=egl PYTHONUNBUFFERED=1
COPY probe.py .
CMD ["python", "probe.py"]
