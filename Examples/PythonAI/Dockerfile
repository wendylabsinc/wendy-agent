# Use Python 3.11 slim image as base
FROM python:3.11-slim

# Set working directory
WORKDIR /app

# Install system dependencies required for audio processing and Whisper
RUN apt-get update && apt-get install -y \
    # Audio libraries
    libportaudio2 \
    libasound2-dev \
    portaudio19-dev \
    alsa-utils \
    alsa-tools \
    # Required for building audio packages
    gcc \
    g++ \
    python3-dev \
    # FFmpeg for audio processing (required by Whisper)
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Copy requirements first for better Docker layer caching
COPY requirements.txt .

# Install Python dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Copy the application code
COPY speech_transcriber.py .
COPY test_microphone.py .

# Create a non-root user for security and add to audio group
RUN useradd -m -u 1000 appuser && \
    usermod -a -G audio appuser && \
    chown -R appuser:appuser /app
USER appuser

# Set environment variables
ENV PYTHONUNBUFFERED=1
# Configure ALSA for proper audio device detection
ENV ALSA_CARD=0
ENV ALSA_DEVICE=0

# Default command
CMD ["python", "speech_transcriber.py"]
# CMD ["python", "/app/test_microphone.py"]
