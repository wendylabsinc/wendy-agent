FROM python:3.11-slim

WORKDIR /app

# The image contains only application code. Runtime assets declared in
# wendy.json.files are synced by Wendy and mounted read-only beside the app.
COPY app.py .

RUN useradd --create-home --shell /bin/bash app && chown -R app:app /app
USER app

EXPOSE 8000
ENV PORT=8000

CMD ["python", "app.py"]
