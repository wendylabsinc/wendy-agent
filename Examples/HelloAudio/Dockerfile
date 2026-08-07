# syntax=docker/dockerfile:1.6
FROM python:3.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    alsa-utils \
    pipewire-bin \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --no-cache-dir -r requirements.txt

COPY app.py index.html generate_sound.py ./

# Synthesized at build time instead of downloaded or committed to the repo —
# hermetic (no network) and deterministic (seeded RNG).
RUN python generate_sound.py

ENV PYTHONUNBUFFERED=1

ARG WENDY_DEVICE_TYPE
ARG WENDY_DEBUG=false
ENV WENDY_DEVICE_TYPE=${WENDY_DEVICE_TYPE}
ENV WENDY_DEBUG=${WENDY_DEBUG}
RUN --mount=type=cache,target=/root/.cache/pip \
    if [ "$WENDY_DEBUG" = "true" ]; then pip install debugpy; fi

ENV PORT=3004
EXPOSE 3004

CMD ["python", "app.py"]
