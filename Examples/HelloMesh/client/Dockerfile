# Use Python 3.11 slim image as base
FROM python:3.11-slim

# Set working directory
WORKDIR /app

# Copy requirements first for better caching
COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY app.py .

# Create a non-root user for security
RUN useradd --create-home --shell /bin/bash app && \
    chown -R app:app /app
USER app

# Wendy injects debugpy for Python apps, so keep the container command
# as a direct Python invocation instead of wrapping it in a shell script.
CMD ["python", "app.py"]
