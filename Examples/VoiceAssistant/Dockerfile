# syntax=docker/dockerfile:1.6
FROM python:3.11-slim

# pipewire-bin (pw-record/pw-play) reaches the host's session graph — the only
# route to Bluetooth audio; pulseaudio-utils (pactl) drives its volume.
# alsa-utils stays as the fallback for hosts that only expose /dev/snd.
RUN apt-get update && apt-get install -y --no-install-recommends \
    alsa-utils \
    ca-certificates \
    pipewire-bin \
    pulseaudio-utils \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --no-cache-dir -r requirements.txt

COPY app.py .

ENV PYTHONUNBUFFERED=1

CMD ["python", "app.py"]
