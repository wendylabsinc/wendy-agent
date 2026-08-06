# Built from a staged context assembled by the fleet launcher: this template's
# files, the pip-installable wendytrain tree staged at wendytrain/, and
# cartpole.py staged in from templates/single/. Everything is copied with a
# plain COPY because the wendy Command Line Interface rejects parent-directory
# build contexts.
FROM python:3.12-slim

WORKDIR /app
COPY . /app/
RUN pip install --no-cache-dir numpy ./wendytrain

# Wendy injects debugpy for Python apps, so keep the container command a
# direct Python invocation instead of wrapping it in a shell script.
CMD ["python", "train.py"]
