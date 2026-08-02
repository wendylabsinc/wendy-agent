# Use Python 3.11 slim image as base
FROM python:3.11-slim

# Set working directory
WORKDIR /app

# Install system libraries required for audio playback (PortAudio/ALSA)
RUN apt-get update && apt-get install -y \
    libportaudio2 \
    libasound2-dev \
    portaudio19-dev \
    alsa-utils \
    && rm -rf /var/lib/apt/lists/*

# Copy requirements first for better Docker layer caching
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the application code and bundled sound sample
COPY app.py .
COPY sounds/ sounds/

# Create a non-root user and add it to the audio group so it can reach /dev/snd
RUN useradd -m -u 1000 appuser && \
    usermod -a -G audio appuser && \
    chown -R appuser:appuser /app
USER appuser

ENV PYTHONUNBUFFERED=1

CMD ["python", "app.py"]
