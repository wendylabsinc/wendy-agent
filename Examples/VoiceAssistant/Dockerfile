# syntax=docker/dockerfile:1.6
FROM python:3.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    alsa-utils \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --no-cache-dir -r requirements.txt

COPY app.py .

ENV PYTHONUNBUFFERED=1

CMD ["python", "app.py"]
