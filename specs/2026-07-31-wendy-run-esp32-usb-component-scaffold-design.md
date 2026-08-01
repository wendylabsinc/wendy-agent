# `wendy run` — propose Wendy Lite ESP-IDF component install on missing `wendy.json`

Date: 2026-07-31
Status: Approved (design)

## Goal

When a developer runs `wendy run` inside an **existing, bare ESP-IDF project**
(their own `CMakeLists.txt` + `main/`, no `wendy.json` yet) with a **USB-connected
ESP32** plugged in, the CLI should notice this shape and offer to:

1. Add selected Wendy Lite ESP-IDF components
   (https://github.com/wendylabsinc/wendy-lite/tree/main/components) to the
   project as ESP-IDF Component Manager git dependencies.
2. Generate a minimal `wendy.json` so the project becomes buildable/flashable
   via the existing `wendy run` path immediately afterward.

This is a native-firmware flow (developer writes their own ESP-IDF `main.c`)
distinct from the existing WASM-app flow (`wendy init` → Swift/WASM app run on
top of the prebuilt Wendy Lite firmware). WASM-only components are never
offered here.

## Background & precedent

Investigation of the current codebase found the building blocks already exist
— only the "no `wendy.json` yet" bootstrap path is missing:

- **Device targeting**: `resolveRunTarget` → `resolveTarget`/`resolveTargetInner`
  (`go/internal/cli/commands/helpers.go:1758,1788`) is the single funnel for
  device selection in every command, producing a `SelectedDevice`
  (`helpers.go:723`) with `Agent`/`Bluetooth`/`External+Provider` variants.
  `providers.AvailableProviders()` (`helpers.go:1911,2303`) is queried
  unconditionally — discovery runs even with no `wendy.json` present.
- **Wendy Lite USB discovery already works**: `MicroWendyProvider`
  (`go/internal/cli/providers/microwendy.go`, key `"wendy-lite"`) discovers
  devices via mDNS *and* USB serial in `DiscoverDevices` (lines 56-102), using
  `discovery.GetSerialDiscovery()` /
  `go/internal/shared/discovery/serial_discovery.go` (OS-specific enumeration
  in `serial_{darwin,linux,windows}.go`). `models.ExternalDevice.ConnectionType()`
  already reports `"USB"` vs `"LAN"` vs `"BLE"` for a resolved device.
- **Build routing is file-shape based, not `wendy.json`-based**: `Build`
  (`microwendy.go:113-122`) switches on a `projectType` string computed by
  `detectProjectType` (`go/internal/cli/commands/docker.go:200-246`), which
  checks (in precedence order) compose files, Dockerfile, `Package.swift` →
  `"swift"`, `*.xcodeproj`, Python markers, then
  `espidftoolchain.IsEspIdfProject(dir)` → `"esp-idf"`. None of this reads
  `wendy.json` — it inspects the directory. So once a `wendy.json` exists at
  all, `buildEspIdf` (`microwendy.go:220-274`) already builds
  (`espidftoolchain.EnsureVersion` → `idf.py set-target` → `idf.py build`) and
  flashes existing ESP-IDF projects correctly.
- **`Platform: "wendy-lite"`** is already a distinct enum value
  (`go/internal/shared/appconfig/appconfig.go:116`), and `Language`
  (`appconfig.go:236`, schema `wendy.schema.json:30-33`) is a free-form,
  unvalidated string that nothing currently reads for routing — no schema
  changes needed.
- **No component-dependency management exists yet**: nothing in the repo
  touches `idf_component.yml`, the ESP-IDF Component Registry, or clones
  `wendy-lite`. `go/internal/cli/espidftoolchain/toolchain.go` only manages the
  SDK/toolchain itself (`EnsureVersion`, `IsEspIdfProject`, `IdfCommandContext`)
  — this is genuinely new.
- **UI primitives to reuse**: `tui.ConfirmDefaultYes`
  (`go/internal/cli/tui/confirm.go:169`) and the existing multi-select
  checklist `tui.ChecklistModel`/`RunChecklist`
  (`go/internal/cli/tui/checklist.go:23,215`), already used for the
  entitlements picker (`go/internal/cli/commands/init_cmd.go:1004-1013`).
  `gopkg.in/yaml.v3` is already a repo dependency.

The only genuinely new capability is: detect this specific opportunity, ask,
and write `idf_component.yml` + `wendy.json`.

## Decisions (from brainstorming)

- **Scope**: existing bare ESP-IDF projects only. Scaffolding a brand-new
  project from an empty directory is explicitly out of scope for this PR.
- **Install mechanism**: ESP-IDF Component Manager (`idf_component.yml` git
  dependency), not vendoring/cloning and not a git submodule. No new
  fetch/clone code in the CLI — `idf.py build`'s existing component manager
  (already invoked by `buildEspIdf`) resolves it.
- **Component set offered**: user picks via checklist; WASM-related components
  (`wendy_wasm`, `wendy_hal_export`, `wendy_wasi_shim`, `wendy_safety`,
  `wendy_callback`) are never offered in this flow.

## Architecture

### 1. Trigger detection

New function, e.g. `shouldOfferWendyLiteESPIDFScaffold(cwd string, target
SelectedDevice) bool`, called from the existing missing-config branch in
`go/internal/cli/commands/run.go` (`cfgMissing` handling around lines
674-699/781-799, alongside the existing `preflightMissingAppConfigForMacTarget`
pattern). True only when **all** of:

1. `wendy.json` is absent (`cfgMissing`).
2. `espidftoolchain.IsEspIdfProject(cwd)` is true.
3. `target.External != nil && target.Provider != nil &&
   target.Provider.Key() == "wendy-lite" && target.External.ConnectionType() ==
   "USB"`.

No new discovery code — `target` is whatever `resolveRunTarget` already
resolved (interactive picker or default device), and that picker already
surfaces USB wendy-lite devices today.

### 2. Confirm + component selection

New function `promptAndScaffoldWendyLiteESPIDF(cwd string) error`:

- `tui.ConfirmDefaultYes`: *"This looks like an ESP-IDF project without a
  wendy.json, and a USB-connected ESP32 was detected. Add Wendy Lite
  components and set up `wendy run` for this project?"* Decline → return a
  sentinel (no-op); caller falls through to today's existing behavior
  unchanged.
- `tui.RunChecklist("Which Wendy Lite components do you want to add?", items)`
  with items built from a static list (non-WASM only):
  `wendy_hal`, `wendy_usb`, `wendy_wifi`, `wendy_ble_prov`, `wendy_cloud_prov`,
  `wendy_storage`, `wendy_uart`, `wendy_spi`, `wendy_sys`, `wendy_otel`,
  `wendy_ble`, `wendy_net`, `wendy_app_usb`. All pre-selected
  (`ChecklistItem.Selected = true`) by default; user can uncheck.
  `ErrCancelled` → abort, no files written.

### 3. Writing `main/idf_component.yml`

New function `mergeIdfComponentDependencies(path string, components []string)
error` in a new file, e.g.
`go/internal/cli/commands/wendylite_scaffold.go`:

- Target path: `<projectPath>/main/idf_component.yml` (ESP-IDF's per-component
  manifest convention for the `main` component).
- Parse with `yaml.v3` into a `yaml.Node` (not a plain map) so existing
  formatting/comments/unrelated dependencies are preserved on write.
- Ensure a `dependencies:` mapping node exists; for each selected component,
  add/overwrite only that key:
  ```yaml
  dependencies:
    wendy_hal:
      git: "https://github.com/wendylabsinc/wendy-lite.git"
      path: "components/wendy_hal"
  ```
- Idempotent: re-running with the same selection produces no diff; re-running
  with a different selection only touches the changed keys.
- If the file doesn't exist, create it with just the `dependencies:` block.
- If zero components were selected in the checklist, skip this step entirely
  (see Error handling).

### 4. Writing `wendy.json`

Reuse the existing `wendy.json` writer used by `wendy init`
(`go/internal/cli/commands/init_cmd.go`, the same code path that produces
`AppID`/`Version`/`Entitlements`), called with `Platform:
appconfig.PlatformWendyLite` and a descriptive `Language` value (e.g. `"c"`).
No schema/appconfig changes required — `Language` is already free-form and
`PlatformWendyLite` already exists.

### 5. Continuation

After `wendy.json` exists, control returns to the normal `wendy run` flow.
Nothing about `resolveRunTarget`/`detectProjectType`/`buildEspIdf` changes —
the project now simply looks, to that existing code, exactly like a
hand-written ESP-IDF wendy-lite project already would.

## Data flow

```
wendy run (cwd = bare ESP-IDF project, no wendy.json)
  → resolveRunTarget()                     [existing, unchanged]
      → USB-connected wendy-lite device resolved
  → cfgMissing == true
  → shouldOfferWendyLiteESPIDFScaffold() == true   [new]
      → confirm (tui.ConfirmDefaultYes)            [new]
      → checklist (tui.RunChecklist)                [new]
      → mergeIdfComponentDependencies(main/idf_component.yml)  [new]
      → write wendy.json (Platform: wendy-lite)     [new, reuses init writer]
  → existing wendy run flow continues:
      detectProjectType() → "esp-idf" → buildEspIdf() → idf.py build → flash
```

## Error handling

- **Declined confirm**: no-op, falls through to whatever `wendy run` does
  today for a `wendy.json`-less directory (existing generic scaffold prompt,
  unchanged).
- **Zero components selected**: skip the `idf_component.yml` write, still
  write `wendy.json`, print a note that no components were added and they can
  be added later (re-running `wendy run` won't re-offer the scaffold once
  `wendy.json` exists — a future manual command, if ever needed, is out of
  scope here).
- **`idf_component.yml` write failure** (permissions, malformed existing YAML
  that fails to parse): abort before writing `wendy.json`, surface a clear
  error, leave the project untouched — no half-configured state.
- **Ctrl+C at either prompt**: `tui.ErrCancelled` propagates, abort cleanly,
  no files written.

## Testing

- **Trigger detection**: table-driven unit test over the 3-condition matrix
  (`wendy.json` present/absent × ESP-IDF shape yes/no × device
  provider/connection-type combinations) asserting
  `shouldOfferWendyLiteESPIDFScaffold`'s boolean result.
- **`mergeIdfComponentDependencies`**: unit tests for: empty/non-existent file,
  file with pre-existing unrelated dependencies (must be preserved), file with
  a subset of wendy-lite deps already present (idempotency + only-changed-keys
  behavior), malformed YAML (must error, not panic or clobber).
- **Manual / hardware**: end-to-end `wendy run` against a real USB-connected
  ESP32 with a bare ESP-IDF project is out of scope for CI — hardware-unverified,
  called out explicitly in the PR like other WendyOS device-facing changes.

## Scope (v1, YAGNI)

- Existing bare ESP-IDF projects only — no from-scratch scaffold of a new
  project in an empty directory.
- ESP-IDF Component Manager git dependency only — no vendoring, no submodules.
- Non-WASM components only, offered via a static list — no dynamic fetch of
  the current component list from the wendy-lite repo/registry.
- `main/idf_component.yml` only — no support for projects with a non-standard
  main-component layout.

## Out of scope / future

- From-scratch scaffold for an empty directory with a USB ESP32 plugged in.
- A standalone `wendy project add-component`-style command to add/remove
  wendy-lite components after initial scaffold.
- Dynamically listing available components by querying the wendy-lite repo at
  runtime instead of a static list.
- Pinning to a specific `wendy-lite` git ref/tag (currently `main`, matching
  existing precedent in `init_cmd.go:1610`'s Swift package dependency).
