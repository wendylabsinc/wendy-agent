# `wendy.json.files` for WendyOS/Linux — Screencast Script

## Focus points

- `wendy.json.files` is the normal `wendy run` mechanism for development
  inputs that should travel with the app but should not be baked into the image.
- Best examples: local model directories, calibration bundles, sample datasets,
  generated assets, config files, or other large static files.
- The problem: putting large assets in the top Docker layer means a normal code
  edit can invalidate that layer and force a large image push across the LAN.
- The model: keep the image focused on code/runtime, sync large inputs
  separately, and reuse unchanged synced content on later runs.
- WendyOS/Linux behavior: files are copied to an agent-managed app-scoped area
  and mounted read-only into the container under the app working directory.
- `path` is relative to `wendy.json`; `to` is relative to the app working
  directory.
- `files` are deployment inputs, not persistent app data. Use `persist` for
  app-written data that should survive redeploys or app removal.
- Stale synced files are pruned on redeploy when removed from `wendy.json`.
- Removing the app removes its app-scoped synced files.
- Top-level `files` currently apply to single-container deployments. Multi-
  service `wendy.json` and Docker Compose report a clear unsupported message.

## Research notes / what is safe to show

### Clearly documented and available

- `wendy.json` supports top-level `files` entries:

  ```json
  {
    "files": [
      { "path": "Models/Current", "to": "Current" }
    ]
  }
  ```

- `path` is relative to `wendy.json`.
- `to` is optional and resolves relative to the app working directory.
- If `to` is omitted, the destination is `path` with a leading `./` removed.
- `path` and `to` must be relative and must not contain `..` components.
- Configured paths must resolve inside the project directory.
- On WendyOS/Linux, synced files are stored under an agent-managed app-scoped
  area and mounted read-only into the container.
- Stale files removed from `wendy.json` are deleted from the managed area on the
  next `wendy run`.
- App removal removes synced deployment inputs even when persistent volumes are
  preserved.
- Multi-service and Compose do not consume top-level `files` yet.
- `Examples/HelloMLX` shows the intended large-model pattern with:

  ```json
  {
    "path": "Models/Current",
    "to": "Current"
  }
  ```

### Available, but be careful how it is framed

- Do not call file sync a workaround. It is first-class behavior for deployment
  inputs that should not be part of the image.
- Do not frame this as source-to-device builds. File sync keeps large inputs out
  of the image transfer path; `wendy run` still builds and deploys the app using
  the normal build path.
- Do not imply synced files are writable inside the container. They are mounted
  read-only on WendyOS/Linux.
- Do not present `files` as persistent storage. Use `persist` for app-written
  durable data.
- Do not imply multi-service or Compose support yet. Show the clear unsupported
  behavior if useful.

### Best pages/artifacts to show

1. `wendy.json` docs — `files` section and field rules.
2. `wendy run` docs — development file sync section.
3. `Examples/HelloMLX/wendy.json` — large model directory declared as files.
4. A small Docker demo project showing a file mounted into `/work/Current` or
   `/work/config/model.txt`.
5. Terminal output from `wendy run`: build, sync progress, container output.
6. Optional app removal: `wendy device apps remove <app-id> --force`.

## Visual style based on wendy.dev

### Overall look

- Clean, minimal, high-contrast, technical.
- White/black/neutral base with mint highlights.
- Use overlays sparingly and keep them rectangular.

### Fonts

- Primary UI/text: `Helvetica Neue`, `Helvetica`, `Arial`, `sans-serif`.
- Code / terminal labels: `IBM Plex Mono` or `SF Mono`.

### Core palette

| Use | Color | Hex |
|---|---:|---|
| Background | White | `#FFFFFF` |
| Foreground text | Black | `#000000` |
| Dark terminal surface | Near black | `#0C0C0C` |
| Muted surface | Light gray | `#F5F5F5` |
| Border | Neutral border | `#E5E5E5` |
| Muted text | Gray | `#737373` |
| Wendy accent | Mint | `#9FE2BF` |
| Success | Emerald | `#00BB7F` |
| Warning / limitation | Orange | `#F99C00` |

### Suggested overlays

- `files = deployment inputs` — mint background.
- `image = code + runtime` — dark card, mint border.
- `large assets sync separately` — mint underline.
- `read-only mount` — emerald label.
- `not persistent data` — orange label.
- `single-container only today` — orange label.

### Motion / editing

- Keep cuts crisp.
- Use quick zooms/highlights on:
  - the `files` array in `wendy.json`
  - a large file/model directory that is not in the Dockerfile
  - `Syncing files...`
  - second `wendy run` showing files up to date or only changed files
  - read-only mount behavior
- Avoid flashy transitions; simple fades or hard cuts are enough.

## Demo setup

Use a single-container Docker project targeting a WendyOS/Linux device or local
Ubuntu E2E-style agent.

Suggested project shape:

```text
file-sync-demo/
  Dockerfile
  check.sh
  wendy.json
  Models/
    Current/
      model.txt
```

Example `Dockerfile`:

```dockerfile
FROM alpine:3.20
WORKDIR /work
RUN mkdir -p /work/Current
COPY check.sh /check.sh
CMD ["/bin/sh", "/check.sh"]
```

Example `check.sh`:

```sh
#!/bin/sh
set -eu
printf 'MODEL:%s\n' "$(cat Current/model.txt)"
if echo changed > Current/model.txt 2>/tmp/write.err; then
  printf 'WRITE:unexpected\n'
  exit 1
else
  printf 'WRITE:read-only\n'
fi
```

Example `wendy.json`:

```json
{
  "appId": "sh.wendy.demo.file-sync",
  "files": [
    { "path": "Models/Current", "to": "Current" }
  ]
}
```

Create the model file:

```sh
mkdir -p Models/Current
printf 'demo-model-v1' > Models/Current/model.txt
```

## Script

### 0:00 — Intro / framing

**Show on screen**

- A terminal in a Wendy project.
- Split-screen or simple graphic:

```text
Before:
  Docker image = app code + runtime + large model

Now:
  Docker image = app code + runtime
  files       = large model / config / assets
```

**Say**

> This is `wendy.json.files` for WendyOS and Linux container deployments.
> It is for development inputs that should travel with your app, but should not
> be baked into the image.

**Say**

> A common example is a local model directory. If that model sits in the top
> Docker layer beside frequently changing app code, every code edit can turn
> into a large image push across the LAN.

**Say**

> With file sync, the image stays focused on code and runtime, while large
> inputs sync separately and unchanged content is reused on later runs.

### 0:35 — Show the problem in the Dockerfile

**Show on screen**

A Dockerfile without model copy:

```dockerfile
FROM alpine:3.20
WORKDIR /work
RUN mkdir -p /work/Current
COPY check.sh /check.sh
CMD ["/bin/sh", "/check.sh"]
```

Highlight that there is no:

```dockerfile
COPY Models/Current /work/Current
```

**Say**

> Notice what is not in this Dockerfile. I am not copying the model directory
> into the image.

**Say**

> That means changing app code does not have to rebuild and push a layer that
> also contains the model.

### 0:55 — Declare files in `wendy.json`

**Show on screen**

Open `wendy.json`:

```json
{
  "appId": "sh.wendy.demo.file-sync",
  "files": [
    { "path": "Models/Current", "to": "Current" }
  ]
}
```

Highlight `path` and `to`.

**Say**

> Instead, I declare the model as a top-level `files` entry in `wendy.json`.
> `path` is relative to `wendy.json` on my development machine.
> `to` is where the file or directory appears inside the app working directory.

**Say**

> In this case, `Models/Current` becomes `Current` inside the container's
> working directory.

### 1:25 — Run the app

**Show on screen**

```sh
wendy run --device <device-name-or-host>
```

Show output with:

```text
Building and pushing Docker image...
Build and push completed.
Syncing files...
  Current/model.txt ... 100.0%
Container sh.wendy.demo.file-sync created.
Application sh.wendy.demo.file-sync started.
MODEL:demo-model-v1
WRITE:read-only
```

**Say**

> `wendy run` still builds and deploys the app normally.
> After the image build, Wendy syncs the files declared in `wendy.json`.

**Say**

> On WendyOS and Linux, the agent stores them in an app-scoped managed area, then
> mounts them read-only into the container.

**Say**

> The app can read the model, but it cannot mutate the synced deployment input.

### 2:05 — Show app code sees normal files

**Show on screen**

Open `check.sh`:

```sh
printf 'MODEL:%s\n' "$(cat Current/model.txt)"
```

Optional overlay:

```text
App sees: /work/Current/model.txt
Source was: Models/Current/model.txt
```

**Say**

> From the app's point of view this is just a file in the working directory.
> The app does not need to know where it came from on the development machine.

### 2:25 — Iterate without resending unchanged inputs

**Show on screen**

Change app code, but leave `Models/Current/model.txt` unchanged:

```sh
# edit check.sh or source code
wendy run --device <device-name-or-host>
```

Show output that either reports files up to date or only transfers changed file
sync content if something under `files` changed.

**Say**

> Now I can iterate on app code. The image changes, but the model input is
> managed separately.

**Say**

> If the files are unchanged, Wendy does not need to send the large payload again.
> That is the core development loop improvement.

### 2:55 — Update the synced input

**Show on screen**

```sh
printf 'demo-model-v2' > Models/Current/model.txt
wendy run --device <device-name-or-host>
```

Show output:

```text
Syncing files...
  Current/model.txt ... 100.0%
MODEL:demo-model-v2
```

**Say**

> When the input actually changes, Wendy syncs the changed content before the
> replacement container starts.

### 3:20 — Remove a stale synced file

**Show on screen**

Add then remove a second file from `wendy.json`, or show a before/after:

Before:

```json
"files": [
  { "path": "Models/Current", "to": "Current" },
  { "path": "fixtures/old.txt", "to": "old.txt" }
]
```

After:

```json
"files": [
  { "path": "Models/Current", "to": "Current" }
]
```

Then:

```sh
wendy run --device <device-name-or-host>
```

Show:

```text
deleted: old.txt
```

**Say**

> The managed file area also stays clean. If a file is removed from
> `wendy.json`, Wendy prunes the stale synced path on the next run.

### 3:50 — Clarify deployment inputs vs persistent data

**Show on screen**

Two-column overlay:

```text
files
- deployment inputs
- read-only in container
- removed with app
- examples: models, configs, fixtures

persist
- app-written data
- writable
- survives redeploys
- examples: databases, logs, generated maps
```

**Say**

> `files` are deployment inputs. They are not persistent app data.

**Say**

> If the app writes data that should survive redeploys, use a `persist`
> entitlement. Keep `files` for inputs you manage from the project.

### 4:20 — App removal cleanup

**Show on screen**

```sh
wendy device apps remove sh.wendy.demo.file-sync --force
```

Optional follow-up:

```sh
wendy device apps list
```

**Say**

> When the app is removed, Wendy also removes the app-scoped synced files.
> That cleanup is separate from persistent volumes.

### 4:40 — Safety rules and unsupported shapes

**Show on screen**

Highlight docs or bullets:

```text
Rules:
- path and to are relative
- no .. components
- paths must resolve inside the project
- top-level files are single-container only today
```

Optional show a failing config:

```json
{ "path": "../secret.txt" }
```

Output:

```text
invalid wendy.json: files[0]: path must not contain '..' components
```

Optional show multi-service/Compose boundary:

```text
top-level wendy.json files are not supported for multi-service deployments yet
```

**Say**

> The safety rules are intentionally strict. Wendy does not mount arbitrary host
> paths from the CLI machine. Paths must be project-relative and stay inside the
> project.

**Say**

> Today, top-level `files` are for single-container deployments. Multi-service
> and Compose need service-specific semantics, so Wendy fails clearly instead of
> silently guessing.

### 5:15 — HelloMLX / real model example

**Show on screen**

Open `Examples/HelloMLX/wendy.json`:

```json
"files": [
  {
    "path": "Models/Current",
    "to": "Current"
  }
]
```

Open `Examples/HelloMLX/README.md` where it explains model tiers.

**Say**

> This is the same pattern used by the HelloMLX example.
> The selected local model lives under `Models/Current`, and Wendy deploys it as
> `Current` beside the app.

**Say**

> That keeps a multi-gigabyte model out of the app artifact while still making
> it available to the app at runtime.

### 5:45 — Close / recap

**Show on screen**

Final graphic:

```text
wendy.json.files

Project input  ->  Wendy Agent managed sync area  ->  read-only container mount

Use for: models, calibration, datasets, generated assets, config
Use persist for: app-written durable state
```

**Say**

> The rule of thumb is simple: if it is a development input that should travel
> with the app, but should not be part of the image, put it in `files`.

**Say**

> Your container gets a normal read-only file or directory, Wendy avoids
> resending unchanged large inputs, and app cleanup remains app-scoped.
