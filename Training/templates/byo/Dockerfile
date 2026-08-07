FROM python:3.12-slim

WORKDIR /app

# 'wendy fleet train' stages this build context at deploy time: the template
# files plus a wendytrain/ directory copied from Training/wendytrain and
# verified by stage-manifest.json. One library copy lives in git; this copy
# is a build step, never a committed vendor. Building this directory directly
# with docker therefore fails; deploy with 'wendy fleet train', or copy
# Training/wendytrain in yourself first.
COPY . /app/
# Install the vendored library, then remove its source directory so the
# bare wendytrain/ folder cannot shadow the installed package when Python
# adds /app to sys.path.
RUN pip install --no-cache-dir numpy==2.5.1 /app/wendytrain && rm -rf /app/wendytrain

# Wendy injects debugpy for Python apps, so keep the container command a
# direct Python invocation instead of wrapping it in a shell script.
CMD ["python", "main.py"]
