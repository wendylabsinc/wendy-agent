# FastAPI Example

A small FastAPI service with CRUD endpoints, built from a Stagefile
(`build.stagefile.yaml`).

> **This example used to demonstrate something else.** It shipped without any
> build file so that `wendy run` would detect `requirements.txt`, read
> `.python-version`, recognise FastAPI, and offer to generate a Dockerfile for
> you. That auto-generation path still exists in the CLI — it is what any
> Python project with no build file gets — but this example no longer
> exercises it, because a Stagefile takes precedence over both a Dockerfile
> and language detection. If you want to see the generated-Dockerfile flow,
> delete `build.stagefile.yaml` and `build.stagefile.lock.yaml` from a copy of
> this directory and run `wendy run` there.

## Files

- `main.py` — FastAPI application with CRUD endpoints
- `requirements.txt` — Python dependencies (FastAPI, uvicorn, pydantic)
- `build.stagefile.yaml` — the build descriptor
- `build.stagefile.lock.yaml` — the resolved base-image digest, committed
- `.python-version` — 3.12, for local tooling. Note the Stagefile pins
  `python:3.11-slim`, matching the Dockerfile that preceded it; the
  auto-generation path is what read `.python-version`.
- `wendy.json` — Wendy project configuration
- `.gitignore` — ignores the compiled `Dockerfile`/`Dockerfile.generated`

## Running

```bash
cd Examples/FastAPIExample
wendy run
```

`wendy run` compiles `build.stagefile.yaml` into `Dockerfile.generated` (plus
a derived `Dockerfile.generated.dockerignore`) and builds that. Both are build
output — regenerated on every build, and safe to delete.

## The build

```yaml
version: 1
stages:
  - name: app
    from: python:3.11-slim
    workdir: /app
    install:
      pip:
        - requirements: requirements.txt
    copy:
      - from: local
        paths: [main.py]
    cmd: [python, main.py]
```

`main.py`'s `__main__` block starts uvicorn on `$PORT` (default 8000).

## API Endpoints

Once running, the following endpoints are available:

- `GET /` - Welcome message with links
- `GET /health` - Health check endpoint
- `GET /docs` - Interactive API documentation (Swagger UI)
- `GET /items` - List all items
- `GET /items/{id}` - Get item by ID
- `POST /items/{id}` - Create new item
- `PUT /items/{id}` - Update item
- `DELETE /items/{id}` - Delete item
