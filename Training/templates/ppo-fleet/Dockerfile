# Built from the staged context assembled by the fleet launcher: this
# template directory, plus wendytrain/ (the library package), plus
# cartpole.py from templates/single. No parent-directory contexts.
FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir numpy
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu

COPY . /app/

# Wendy injects debugpy for Python apps, so keep the container command a
# direct Python invocation.
CMD ["python", "train.py"]
