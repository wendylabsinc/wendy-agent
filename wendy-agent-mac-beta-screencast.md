# Wendy Agent for Mac Beta — Screencast Script

## Focus points

- Same Wendy CLI workflow, but targeting an Apple Silicon Mac.
- Wendy Agent is installed and configured once; later app deploys are headless.
- macOS permissions are granted to Wendy Agent so apps can later use Mac
  features.
- Beta supports native Swift apps only.
- Supported project types: SwiftPM and Xcode.
- Native Mac recognition: Darwin target, `platform: "darwin"`, and
  `Package.swift` or `.xcodeproj`.
- Dockerfile, Compose, and container projects are not supported on Mac beta.
- VLMMLX/MLX needs Xcode and the Metal toolchain, not plain SwiftPM.
- Unsupported CLI commands fail gracefully with Mac-specific “not supported in
  beta” messages.
- App lifecycle commands still use familiar `wendy device apps ...` commands.
- Beta limitations: no Linux/WendyOS containers, no mTLS/provisioning/cloud yet.
- Intended use: development/trusted-network preview.
- Next likely work: containers, mTLS/provisioning/cloud, production hardening.

## Research notes / what is safe to show

### Clearly documented and available

- **Install path:** the Mac install docs show:

  ```sh
  brew tap wendylabsinc/tap
  brew install --cask wendy-agent
  open /Applications/WendyAgentMac.app
  ```

- **Supported target:** Apple Silicon Mac running macOS 15 or later. Intel Macs
  are explicitly not supported.
- **Agent model:** Wendy Agent for Mac runs as a macOS menu bar app.
- **CLI verification:** docs show `wendy --device {hostname}:50051 device info
  --json`; expected output includes `"os": "darwin"` and
  `"cpuArchitecture": "arm64"`.
- **Discovery/default device:** Bonjour/mDNS discovery and
  `wendy device set-default {hostname}:50051` are documented, with the caveat
  that discovery depends on network and macOS Local Network permission.
- **Native Mac deployment:** Mac apps run as native macOS processes, not Linux
  containers.
- **`wendy.json` platform:** docs explicitly include `platform: "darwin"` for
  native macOS apps managed by Wendy Agent for Mac.
- **Supported `wendy run` project types for Mac:** run docs list native SwiftPM
  and native Xcode as supported for Darwin targets.
- **Rejected project types for Mac:** Dockerfile/container images, Python
  container path, Docker Compose, multi-service `wendy.json`, `linux/...`, and
  `wendyos` are rejected for Darwin/Mac targets.
- **SwiftPM host requirement:** macOS-target SwiftPM builds require a macOS host.
- **Graceful errors:** run docs say unsupported Mac project shapes are rejected
  before build, registry auth, or registry setup; install docs say unsupported
  features should fail with actionable unsupported messages.

### Available, but be careful how it is framed

- **Xcode support:** overview/build/run docs mention Xcode support, and the repo
  includes `Examples/HelloMLX/HelloMLX.xcodeproj`. The Mac install page’s
  “Should work” list emphasizes SwiftPM, so for Xcode claims show the run docs
  supported-project-types table or the actual HelloMLX Xcode project.
- **VLMMLX/MLX Xcode requirement:** this is valid product/demo context and the
  repo has an Xcode HelloMLX example, but the public docs do not currently spell
  out “VLMMLX requires Xcode because of SwiftPM limitations.” Mention it in
  narration as context rather than pointing at docs.
- **Permissions:** it is okay to show Wendy Agent/macOS permissions as one-time
  setup, but full app-level hardware API support is not part of the beta. Frame
  permissions as preparing Wendy Agent to act as the broker for app access over
  time, not as proof that all hardware APIs are already supported.
- **mTLS/provisioning/cloud:** not covered on the Mac install page. Mention as a
  beta boundary in narration, but do not present it as a visible docs claim
  unless a separate internal/source page is opened.

### Best pages/artifacts to show

1. Mac install page — install, launch, verify, default device, “What Works / Not
   supported.”
2. Run command docs — Wendy Agent for Mac supported/rejected project type table.
3. `wendy.json` docs — `platform: "darwin"` section.
4. `Examples/HelloMLX/HelloMLX.xcodeproj` — Xcode/VLMMLX/Metal point.

## Visual style based on wendy.dev

### Overall look

- **Style:** clean, minimal, high-contrast, technical.
- **Mood:** black/white/neutral base with one bright mint accent.
- **Shapes:** mostly rectangular, subtle radius if needed.
  - Marketing site radius: `10px` / `0.625rem`.
  - Docs style is sharper; for overlays use square or very slightly rounded
    rectangles.

### Fonts

- **Primary UI/text:** `Helvetica Neue`, `Helvetica`, `Arial`, `sans-serif`.
- **Code / terminal labels / step numbers:** `IBM Plex Mono`.
- **Terminal:** any readable monospace, ideally `IBM Plex Mono` or `SF Mono`.

### Core palette

| Use | Color | Hex |
|---|---:|---|
| Background | White | `#FFFFFF` |
| Foreground text | Black | `#000000` |
| Primary dark | Neutral 900 | `#171717` |
| Card/dark surface | Near black | `#0C0C0C` |
| Muted surface | Light gray | `#F5F5F5` |
| Border | Neutral border | `#E5E5E5` |
| Muted text | Gray | `#737373` / `#71717B` |

### Brand/accent colors

| Use | Color | Hex |
|---|---:|---|
| Main Wendy accent / CTA | Mint | `#9FE2BF` |
| Hover / stronger mint | Mint green | `#86D3A8` |
| Success / supported | Emerald | `#00BB7F` |
| Bright success accent | Emerald 400 | `#00D294` |
| Beta badge / warning | Orange | `#F99C00` |
| In-progress / caution | Yellow | `#FCBB00` |
| Strong warning/orange chart accent | Orange-red | `#F05100` |
| Teal secondary | Teal | `#009588` |
| Destructive/error only | Red | `#E40014` |

### Suggested usage

- **Main section titles / callouts:** black text on white.
- **Beta labels:** orange block or pill, `#F99C00`, with white or black text
  depending contrast.
- **Supported/native path:** mint or emerald, `#9FE2BF` / `#00BB7F`.
- **Unsupported/boundary callouts:** orange `#F99C00`, not red unless something
  truly failed.
- **Success highlights in terminal:** emerald `#00BB7F`.
- **Code highlights:** dark surface `#0C0C0C`, white text, mint highlight.

### Overlay examples

Use small, rectangular labels:

- `BETA` — background `#F99C00`.
- `platform: "darwin"` — background `#9FE2BF`, text `#000000`.
- `Native SwiftPM` — border `#00BB7F`.
- `Xcode + Metal` — border `#9FE2BF`.
- `Not supported yet` — background `#F99C00`.

### Terminal window styling

- Background: `#0C0C0C`.
- Text: `#FAFAFA`.
- Dim text: `#A1A1AA`.
- Prompt/accent: `#9FE2BF`.
- Success: `#00BB7F`.
- Warning: `#F99C00`.
- macOS window dots if custom rendered:
  - Red `#FF5F57`.
  - Yellow `#FEBC2E`.
  - Green `#28C840`.

### Motion / editing

- Keep cuts crisp.
- Use quick zooms/highlights on:
  - `brew install --cask wendy-agent`.
  - `"os": "darwin"`.
  - `"platform": "darwin"`.
  - `wendy run`.
  - graceful unsupported error.
- Avoid flashy transitions; use simple fades or hard cuts.

## Script

### 0:00 — Intro / framing

**Show on screen**

- Terminal connected to the target Apple Silicon Mac mini, or Screen Sharing to
  that Mac mini.
- Keep the story on the Mac mini: install the agent, launch it, then move into
  CLI-driven deploys.

**Say**

> This is Wendy Agent for Mac Beta, running on an Apple Silicon Mac mini.
> If you already know the WendyOS workflow, the model is the same: the Wendy CLI
> talks to an agent on a target device.
> The difference is that this target is macOS, and the beta focuses on native
> Swift apps.

### 0:20 — Install and launch Wendy Agent

**Show on screen**

Install from Homebrew:

```sh
brew tap wendylabsinc/tap
brew install --cask wendy-agent
```

Then launch it:

```sh
open /Applications/WendyAgentMac.app
```

Show Wendy Agent appearing in the menu bar.

**Say**

> We start by installing Wendy Agent on the Mac mini with Homebrew, then launch
> it like a normal macOS app.
> There is no WendyOS image to flash here. The Mac is still macOS; Wendy Agent is
> the service that lets the Wendy CLI target it.

### 0:45 — One-time permissions and headless deploys

**Show on screen**

- Wendy Agent menu bar app.
- macOS privacy settings or permission prompts, if easy: Microphone, Camera,
  Bluetooth.

**Say**

> This setup only needs to happen once. During setup, macOS may ask for
> permissions like microphone, camera, Bluetooth, and later more system
> capabilities.
> Those permissions are granted to Wendy Agent, which is what allows future Wendy
> apps on this Mac to access those devices and features.

**Say**

> After the agent is installed, launched, and configured, app deploys are
> headless from the CLI.

### 1:10 — Verify Mac target

**Show on screen**

```sh
wendy --device mac-mini.local:50051 device info --json
```

Highlight:

```json
"os": "darwin",
"cpuArchitecture": "arm64"
```

**Say**

> I can verify the connection with the usual `device info` command.
> The key Mac-specific bits are `darwin` for the OS and `arm64` for Apple
> Silicon.

### 1:30 — Device discovery / default target

**Show on screen**

```sh
wendy discover
wendy device set-default mac-mini.local:50051
```

**Say**

> Discovery and default device selection work like other Wendy targets.
> For a headless Mac mini, I can use the hostname or IP and save it as the
> default target.

### 1:50 — What counts as a native Mac app

**Show on screen**

- `wendy.json`:

```json
{
  "appId": "com.example.hello-mac",
  "platform": "darwin"
}
```

**Say**

> For the CLI to treat this as a native Mac app, the target must be a Darwin
> agent, and the project should resolve to `platform: "darwin"`.
> I like to put that explicitly in `wendy.json` so the intent is clear.

**Show on screen**

- A project with `Package.swift`.
- Then a project with `.xcodeproj`.

**Say**

> The beta supports two native Swift project shapes: SwiftPM projects with
> `Package.swift`, and Xcode projects with `.xcodeproj`.

**Show on screen**

- Briefly show there is no Dockerfile or Compose file in the native Mac project.

**Say**

> Container markers are different. If the project has a Dockerfile or Compose
> file, Wendy treats it as a container project, and that is not supported on Mac
> in this beta.
> If a project has both a Dockerfile and `Package.swift`, use the native Swift
> path explicitly with `--build-type swift`, or remove the container marker.

### 2:35 — Run SwiftPM app

**Show on screen**

```sh
wendy run --device mac-mini.local:50051
```

Or, if the default target is set:

```sh
wendy run
```

**Say**

> Here is the SwiftPM path. `wendy run` builds the Swift package, syncs it to
> Wendy Agent for Mac, and launches it as a native macOS process on the Mac mini.

**Show on screen**

- Build output.
- App logs.
- App responding in terminal or browser, if applicable.

**Say**

> No desktop interaction is needed for deploys after the agent is configured.

### 3:05 — Xcode / VLMMLX path

**Show on screen**

- HelloMLX / VLMMLX `.xcodeproj`.
- Xcode project files.
- Optional: Xcode toolchain installed.

**Say**

> Xcode projects are also supported. This is especially important for MLX and
> VLMMLX-style apps.
> Because of current SwiftPM limitations, VLMMLX needs to build through Xcode and
> run inside an app bundle, with the Metal toolchain installed.

**Show on screen**

```sh
wendy run --device mac-mini.local:50051
```

**Say**

> So for that kind of workload, use the Xcode project path rather than plain
> SwiftPM.

### 3:40 — App lifecycle

**Show on screen**

```sh
wendy device apps list
wendy device apps stop <app-id>
wendy device apps remove <app-id>
```

**Say**

> Once the app is running, lifecycle commands use the familiar Wendy CLI surface.
> I can list, stop, and remove apps from the Mac target just like other Wendy
> devices.

### 4:05 — Graceful unsupported behavior

**Show on screen**

Run an unsupported command, for example a hardware/container-related command:

```sh
wendy device hardware list
```

Or run from a Dockerfile/Compose project:

```sh
wendy run --device mac-mini.local:50051
```

**Say**

> The beta is intentionally narrow, but unsupported paths should fail clearly.
> If I use a command or project type that is not supported by Wendy Agent for Mac
> yet, the CLI gives a Mac-specific unsupported message instead of a generic
> agent-version or build failure.

**Show on screen**

- Highlight a message such as “not supported on macOS / Wendy Agent for Mac” or
  “Linux containers are not supported on Macs yet.”

### 4:35 — Beta boundaries

**Show on screen**

- Support matrix or docs page.
- Optional bullets on screen.

**Say**

> The beta supports native Swift apps on Apple Silicon Macs.
> It does not yet support Linux or WendyOS container deployment to Mac.
> It also does not yet include production mTLS, provisioning, or Wendy Cloud
> support.

**Say**

> So for now, treat this as a development preview for trusted networks and
> controlled environments.

### 5:00 — Close / next steps

**Show on screen**

- Successful app running.
- Wendy Agent menu bar.
- Terminal with `wendy device apps list`.

**Say**

> The mental model is simple: install and configure Wendy Agent once, then deploy
> native SwiftPM or Xcode apps headlessly with the same Wendy CLI workflow.

**Say**

> Next up after the beta are good production candidates like Linux or WendyOS
> container deployment support, mTLS and provisioning, Wendy Cloud integration,
> and broader production hardening.
