# Build context: staged by the fleet launcher (Training/launch/fleet.py). The
# launcher copies this template's files and the pip-installable wendytrain
# library into one flat temporary directory and builds from there, because the
# wendy Command Line Interface rejects build contexts that reach into parent
# directories. Everything below therefore copies from the context root only.
FROM python:3.12-slim

WORKDIR /app

COPY . /app/

RUN pip install --no-cache-dir numpy==2.5.1 /app/wendytrain && rm -rf /app/wendytrain

# The image bakes the template's configuration file; deploy-time
# WT_<SECTION>__<KEY> variables override individual keys, and a deploy-time
# WT_CONFIG replaces the file wholesale.
ENV WT_CONFIG=/app/config.toml

# Wendy injects debugpy for Python apps, so keep the container command a
# direct Python invocation instead of wrapping it in a shell script.
CMD ["python", "train.py"]
