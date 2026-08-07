# syntax=docker/dockerfile:1.6
FROM python:3.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    alsa-utils \
    pipewire-bin \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --no-cache-dir -r requirements.txt

COPY app.py index.html ./

# Fetched at build time instead of committed to the repo (public domain, CC0
# via Wikimedia Commons — see README's "Sound attribution" section).
RUN mkdir -p assets && \
    curl -fsSL -o assets/sleigh-bells.wav \
    https://upload.wikimedia.org/wikipedia/commons/c/ce/Sleigh_bells.wav

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
