# Built from the staged context assembled by 'wendy fleet train': this
# template directory, plus wendytrain/ (the library package), plus
# cartpole.py from templates/single. No parent-directory contexts.
FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir numpy==2.5.1
RUN pip install --no-cache-dir torch==2.13.0 --index-url https://download.pytorch.org/whl/cpu

COPY . /app/

# Install the staged library, then remove the source tree so it cannot
# shadow the installed package through namespace-package resolution.
RUN pip install --no-cache-dir /app/wendytrain && rm -rf /app/wendytrain

# Wendy injects debugpy for Python apps, so keep the container command a
# direct Python invocation.
CMD ["python", "train.py"]
