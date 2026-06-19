FROM python:3.11-slim
WORKDIR /app
RUN mkdir -p /app/config /app/assets /app/models/detector \
    && touch /app/config/runtime.json /app/assets/message.txt
COPY main.py .
# config/, data/, and models/ are intentionally not copied into the image.
# Wendy syncs them via wendy.json.files and mounts them read-only at runtime.
ENV PYTHONUNBUFFERED=1
CMD ["python3", "main.py"]
