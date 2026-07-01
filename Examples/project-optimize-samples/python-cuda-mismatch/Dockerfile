# Intentionally un-optimized. See ../README.md.
# - x86-first nvidia/cuda base image on an arm64 (Jetson) target
# - pip install without a cache mount
# - no .dockerignore alongside it
FROM nvidia/cuda:12.4.0-runtime-ubuntu22.04

WORKDIR /app
COPY requirements.txt .
RUN pip install -r requirements.txt
COPY . .
CMD ["python3", "main.py"]
