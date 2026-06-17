---
theme: default
title: Wendy File Sync
info: |
  Presentation deck for WDY-1532: wendy.json file sync.
class: text-left
transition: fade-out
mdc: true
---

# Wendy File Sync

Large app files, synced by `wendy run`.

- Models
- Prompts
- Calibration data
- Datasets

Konstantin · Wendy Labs · 2026-06-17

<!--
Timeline id: title
Say:
Container images are great for packaging code and dependencies, but they’re a poor fit for large app files that change independently. This presentation is about Wendy File Sync: making those large files part of the normal wendy run workflow.
-->

---

# The problem

Large app files change on their own timeline.

Examples:

- ML models
- prompts
- calibration data
- sample datasets
- generated assets

<!--
Timeline id: problem
Say:
Think about ML models, prompts, calibration data, sample datasets, or generated assets. These files are required by the app at runtime, but they often change on a different timeline than the application code.
-->

---

# Docker layers are linear

```text
base + dependencies
↓
model-a
↓
model-b
↓
model-c
↓
frequently changing app code
```

If an earlier large layer changes, unrelated later layers can be rebuilt and pushed.

<!--
Timeline id: linear-layers
Say:
If we bake those files into the image, Docker layer ordering becomes the problem. Layers are linear. If an earlier large model layer changes, Docker can invalidate unrelated later layers, so one changed file can turn into a much larger rebuild and push.
-->

---

# The usual workaround assumes network

```text
edge device
  ↓ downloads
S3 / Hugging Face / model registry / init container / sidecar
```

That often means:

- internet or internal network access
- credentials on the device
- extra lifecycle management

<!--
Timeline id: workaround
Say:
The usual industry workaround is to externalize the files: download from S3, Hugging Face, a model registry, or an init container or sidecar. That works in cloud-native environments, but it assumes the target device has network access, credentials, and extra lifecycle management.
-->

---

# Wendy's deployment story

One cable — USB or Thunderbolt — and you're ready to go.

No Wi‑Fi. No internet. No device-side cloud setup.

<!--
Timeline id: one-cable
Say:
That does not match Wendy’s deployment story. Wendy’s pitch is one cable — USB or Thunderbolt — and you’re ready to go. No Wi-Fi, no internet, no device-side cloud setup.
-->

---

# What File Sync does

```text
project files
  ↓ wendy run
agent app area
  ↓ read-only mount
app working directory
```

Only changed files are synced over the existing CLI-to-agent connection.

<!--
Timeline id: what-it-does
Say:
File Sync makes large runtime files part of wendy run. The developer declares the files in wendy.json. Wendy syncs only the changed files over the existing CLI-to-agent connection, stores them in an app-scoped area on the device, and mounts them read-only beside the app.
-->

---

# Declare files in `wendy.json`

```json
{
  "appId": "sh.wendy.demo.vision",
  "version": "1.0.0",
  "language": "python",
  "files": [
    { "path": "models/detector.onnx", "to": "models/detector.onnx" },
    { "path": "models/classifier.onnx", "to": "models/classifier.onnx" },
    { "path": "prompts/system.txt" }
  ]
}
```

Each entry is independently synced.

<!--
Timeline id: config
Say:
The config is small. Each entry is an independently synced file or directory. If the prompt changes, Wendy syncs the prompt. If one model changes, Wendy syncs that model. The rest are skipped when unchanged.
-->

---

# Developer flow

```sh
wendy run --device lab-edge-01
```

```text
Building application image
✓ Built sh.wendy.demo.vision:latest

Syncing wendy.json files
  models/detector.onnx      unchanged
  models/classifier.onnx    changed, syncing
  prompts/system.txt        unchanged

Starting app
```

<!--
Timeline id: developer-flow
Say:
From the developer’s point of view, there is no new command. This is just wendy run. Image changes and file changes are decoupled. Code can change frequently without forcing model transfer, and model transfer does not require the device to reach the internet.
-->

---

# Runtime model

On Linux/WendyOS, synced files are stored under:

```text
/var/lib/wendy/files/<appId>/
```

They are mounted read-only at:

```text
<working directory>/<to-or-path>
```

The app just reads normal files.

<!--
Timeline id: runtime-model
Say:
At runtime, the app sees these files under its working directory. On Linux and WendyOS, Wendy stores them under the app’s file sync directory and bind-mounts them read-only into the container. No SDK required. No special runtime API. It is just files.
-->

---

# App code stays normal

```python
from pathlib import Path

model_path = Path("models/classifier.onnx")
prompt = Path("prompts/system.txt").read_text()

print(model_path.exists())
print(prompt)
```

File Sync changes packaging, not app code.

<!--
Timeline id: app-code
Say:
That means the app code stays ordinary. It reads files from normal relative paths. File Sync changes how those files get to the device and into the app, not how the app consumes them.
-->

---

# Lifecycle semantics

File Sync:

- comes from the project
- read-only in the app
- cleaned with the app
- synced by `wendy run`

Persistent volumes:

- are written by the app
- are mutable
- are app data

<!--
Timeline id: lifecycle
Say:
These files are not persistent app data. If the app writes user data, logs, a database, or generated state, that belongs in a persistent volume. File Sync is for files that come from the project and are needed by the app at runtime.
-->

---

# Safety and scope

Supported now:

- single-container `wendy run`
- native macOS runs
- files and directories
- optional destination via `to`

Not yet:

- multi-service `services`
- Docker Compose

<!--
Timeline id: scope
Say:
The first implementation is intentionally scoped. It supports single-container wendy run, native macOS runs, files and directories, and optional destinations with to. Multi-service projects and Docker Compose are explicit follow-ups, not silent partial support.
-->

---

# Path guardrails

Rejected examples:

```json
{ "path": "../secrets.env" }
```

```json
{ "path": "/tmp/model.onnx" }
```

```json
{ "path": "model.onnx", "to": "../../outside" }
```

Not a general “mount anything from my laptop” feature.

<!--
Timeline id: guardrails
Say:
There are also path safety rules. Path and to must be relative, parent directory traversal is rejected, sources must resolve inside the project, and Linux mount paths are checked to prevent symlink escapes. This is not a general mount anything from my laptop feature.
-->

---

# Why this matters

Before File Sync, teams had to choose between bad options:

- bake large assets into images and fight Docker layer invalidation
- require the device to download from the cloud
- pre-populate volumes manually
- write custom startup scripts
- manage object-store credentials on the device

<!--
Timeline id: why-matters
Say:
The core value is faster, more reliable iteration for edge apps. Without File Sync, teams have to bake large assets into images, pre-populate volumes manually, write startup download scripts, or manage object-store credentials on the device.
-->

---

# Wendy File Sync

- Large files outside the image
- Independent sync per file or directory
- No internet required on the device
- Read-only runtime mounts
- App-scoped cleanup
- Built into `wendy run`

<!--
Timeline id: summary
Say:
File Sync gives Wendy a cleaner answer. Large runtime files are synced directly from the developer machine, over the same connection as wendy run, with independent change detection and no internet required on the device.
-->

---

# Closing

Container images should carry code and dependencies.

Wendy File Sync handles large runtime files that change on their own timeline.

```sh
wendy run
```

<!--
Timeline id: closing
Say:
The pitch is simple: container images should carry code and dependencies. Wendy File Sync handles large runtime files that change on their own timeline. That keeps images lean, keeps edge devices offline-friendly, and keeps the workflow as simple as wendy run.
-->

---

# Questions?

The engineer working on this feature is **Konstantin**.

Please contact him if you have further questions, feedback, or follow-up requests.

<!--
Timeline id: contact
Say:
The engineer working on this feature is Konstantin. Please contact him if you have further questions, feedback, or follow-up requests.
-->
