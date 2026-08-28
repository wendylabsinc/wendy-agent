FROM python:3.12-slim-bookworm

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    FRUIT_NINJA_HOST=0.0.0.0 \
    FRUIT_NINJA_PORT=8878

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      libegl1 \
      libgl1 \
      libosmesa6 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt ./requirements.txt
RUN python -m pip install --no-cache-dir -r requirements.txt

COPY fruit_ninja ./fruit_ninja
COPY models ./models

EXPOSE 8878

CMD ["python", "-m", "fruit_ninja.bootstrap"]
