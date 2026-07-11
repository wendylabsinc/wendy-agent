FROM python:3.11-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 rsync git \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip pip install --no-cache-dir -r requirements.txt
COPY g1fleet/ ./g1fleet/
COPY app.py .
# Vendor the G1 model into a FIXED image path. The build has network; the
# runtime does NOT -- mesh-mode containers egress only to the mesh CIDR, so a
# runtime robot_descriptions git fetch would crash. g1env.py loads from
# G1_MODEL_DIR, so no network/git is ever touched at runtime.
RUN python -c "import os,shutil; from robot_descriptions import g1_mj_description as g; src=os.path.dirname(g.MJCF_PATH); shutil.copytree(src,'/opt/g1_model',dirs_exist_ok=True); open('/opt/g1_model/.mjcf_name','w').write(os.path.basename(g.MJCF_PATH)); print('vendored',g.MJCF_PATH)"
ENV G1_MODEL_DIR=/opt/g1_model
RUN useradd --create-home --shell /bin/bash app && mkdir -p /data/checkpoints && chown -R app:app /app /data && chmod -R a+rX /opt/g1_model
USER app
ENV PYTHONUNBUFFERED=1
EXPOSE 8080 5678
CMD ["python", "app.py"]
