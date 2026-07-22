FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .
COPY static/ ./static/

RUN useradd --create-home --shell /bin/bash app && chown -R app:app /app
USER app

ARG RCON_HOST=127.0.0.1
ARG RCON_PORT=25575
ARG RCON_PASSWORD=wendymc-local
ARG MC_HOST=127.0.0.1
ARG MC_PORT=25565
ARG LOG_PATH=/mc-data/logs/latest.log
ARG WEBUI_PORT=8080

ENV PYTHONUNBUFFERED=1 \
    RCON_HOST=${RCON_HOST} \
    RCON_PORT=${RCON_PORT} \
    RCON_PASSWORD=${RCON_PASSWORD} \
    MC_HOST=${MC_HOST} \
    MC_PORT=${MC_PORT} \
    LOG_PATH=${LOG_PATH} \
    WEBUI_PORT=${WEBUI_PORT}

EXPOSE 8080

CMD ["sh", "-c", "uvicorn app:app --host 0.0.0.0 --port ${WEBUI_PORT}"]
