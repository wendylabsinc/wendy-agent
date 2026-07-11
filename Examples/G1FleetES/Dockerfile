FROM python:3.11-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 rsync git \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip pip install --no-cache-dir -r requirements.txt
COPY g1fleet/ ./g1fleet/
COPY app.py .
RUN useradd --create-home --shell /bin/bash app && mkdir -p /data/checkpoints && chown -R app:app /app /data
USER app
# Bake the G1 model into the image (no runtime downloads). Runs after USER app
# so the robot_descriptions cache under $HOME/.cache lands in a dir the
# non-root "app" user actually owns and can read from at runtime.
RUN python -c "from robot_descriptions import g1_mj_description as g; print(g.MJCF_PATH)"
ENV PYTHONUNBUFFERED=1
EXPOSE 8080 5678
CMD ["python", "app.py"]
