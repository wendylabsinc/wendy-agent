# CLI Screenshot Pipeline — Design

**Date:** 2026-06-28
**Status:** Approved (design); pending implementation plan
**Area:** `go/internal/cli/assets/docs` (CLI docs site)

## Problem

The docs embed hand-taken PNG screenshots of the Wendy CLI experience (e.g.
`images/docs/cli-wifi-connect.png`, `cli-wifi-ssid-select.png`,
`cli-wifi-password-entry.png`, `cli-wifi-password-success.png`,
`wendy-discover.png`). These are **interactive TUI flows** — a device picker, an
arrow-key SSID list, a password prompt, a success message — not plain command
output. Captured by hand, they suffer from all three of:

1. **Visual inconsistency** — varying terminal theme, font, window size, padding.
2. **Staleness** — they drift from the real CLI output as the TUI changes.
3. **Manual effort** — taking, cropping, and naming each one is tedious.

We want a repeatable pipeline that produces uniform, current captures with one
command, in **both light and dark variants**.

## Decisions (locked during brainstorming)

| Question | Decision |
| --- | --- |
| Primary goal | All three: consistency + freshness + low effort (a real pipeline) |
| Content source | **Scripted real session** against an attached device (most authentic; not CI-runnable) |
| Output format | **Both** — static per-step PNGs *and* an animated clip per flow |
| Light & dark | **Required** — render each flow twice, once per theme |
| Frame styling | **Window chrome** (macOS title bar, padding, shadow) — but **square corners** to match the site |

## Tool choice: VHS

[`charmbracelet/vhs`](https://github.com/charmbracelet/vhs) drives a headless
terminal from a declarative `.tape` script. It is the only candidate that gives
us, in one pass:

- **Deterministic scripted keystrokes** (`Type`, `Down`, `Up`, `Enter`,
  `Sleep`) — so the same flow renders identically every time.
- **Still frames at chosen moments** via the `Screenshot <path>` command.
- **An animated recording** of the whole session (GIF/WebM) as the tape's
  `Output`.
- **Declarative, uniform styling** — `Set Theme`, `Set FontSize`, `Set Padding`,
  `Set WindowBar`, `Set BorderRadius`, `Set Width` — guaranteeing visual
  consistency and making light/dark a one-line change.

Installable via `brew install vhs`.

Rejected alternatives:

- **asciinema + agg/svg-term** — records real sessions, but timing is
  non-deterministic, theming is limited, and there is no "grab a still at this
  moment"; frames would have to be extracted from the GIF afterward. More glue,
  less consistency.
- **tmux + OS screenshot scripting** — brittle, platform-specific, hard to keep
  uniform.

VHS spawns its **own** terminal and runs `wendy …` inside it, so the scripted
session executes against the real device attached when the operator runs it —
matching the chosen "scripted real session" content source.

## Architecture

A self-contained capture pipeline lives in a new directory under the docs tree:

```
go/internal/cli/assets/docs/screenshots/
  tapes/
    wifi-connect.tape      # keystrokes + Screenshot marks for `wendy device wifi connect`
    discover.tape          # `wendy discover`
  lib/
    common.tape            # shared chrome: font, size, padding, window bar, square corners, shell
  themes/
    wendy-light.json       # VHS palette tuned to fumadocs neutral (light)
    wendy-dark.json        # VHS palette tuned to fumadocs neutral (dark)
  render.sh                # driver: each tape × {light,dark} → frames + clip
  README.md                # prerequisites + usage
```

### Component responsibilities

- **`tapes/<flow>.tape`** — describes *one* flow: the command to run, the
  keystrokes to send, `Sleep`s to let the TUI settle, and a `Screenshot
  <step>.png` at each documented step. Contains **no** theme, size, or output
  settings — those are injected so the same tape renders for both themes.
- **`lib/common.tape`** — the single source of truth for visual styling, sourced
  by every render: font family/size, padding, margin, `Set WindowBar Colorful`
  (macOS three-dot title bar), `Set BorderRadius 0` (square corners), terminal
  width, and `Set Shell`.
- **`themes/wendy-{light,dark}.json`** — VHS theme palettes (background,
  foreground, 16 ANSI colors) tuned to the docs' fumadocs **neutral** palette
  plus the Wendy accent. `MarginFill` per theme matches the doc page background
  so the (corner-less) window blends into the page.
- **`render.sh`** — the driver. For each requested flow and each theme, it
  composes a render from `theme + common.tape + <flow>.tape`, runs `vhs`, and
  relocates outputs to the canonical names under `images/docs/cli/<flow>/`.
  Validates that `vhs` is installed; documents that a device must be attached.

### Render contract (naming)

For a flow `wifi-connect` with steps `select-device`, `select-ssid`,
`enter-password`, `success`, `render.sh` produces under
`images/docs/cli/wifi-connect/`:

```
select-device-light.png     select-device-dark.png
select-ssid-light.png       select-ssid-dark.png
enter-password-light.png    enter-password-dark.png
success-light.png           success-dark.png
wifi-connect-light.webp     wifi-connect-dark.webp   # the animated clip
```

`images/docs/` is the tracked source directory; `scripts/prepare-content.mjs`
already copies it into the gitignored `public/` at build time, so no build
changes are needed beyond placing files here.

> **Theme/output injection note (for the plan):** VHS `Set Theme` takes a theme
> name or inline JSON, not a file path, and `Output`/`Screenshot` paths are
> literal. `render.sh` therefore generates a small per-(flow,theme) driver tape
> that sets `Output`/theme and `Source`s `common.tape` then the flow tape, runs
> it in a temp working dir, and copies results to the canonical names. The exact
> mechanism is an implementation detail; the **naming contract above is the
> stable interface** docs depend on.

## Docs integration

Two MDX components keep usage DRY and theme-aware:

```tsx
// Shows the light PNG normally, the dark PNG when the site is in dark mode.
<CliShot flow="wifi-connect" step="select-device" alt="Selecting the device" />

// Same, for the animated overview clip.
<CliClip flow="wifi-connect" alt="Connecting to Wi-Fi" />
```

`CliShot` renders both `<img>`s and toggles them with fumadocs' class-based dark
mode (Tailwind `block dark:hidden` / `hidden dark:block`). Registered in the
existing MDX components map used by the docs site.

### First consumers (migration scope)

- `guides/device/connecting-to-wifi.mdx` — 4 step images → `<CliShot>` +
  optionally one `<CliClip>` overview.
- `installation/*.mdx` (`wendy-discover.png`, referenced from three install
  guides) → `discover` flow `<CliShot>`.

Non-CLI images (`grayskull.png`, `webcam-setup.png`, board photos, install
videos) are **out of scope** and left as-is.

## Scope & non-goals

- **Semi-automated, not CI.** Because content comes from a scripted *real*
  session, regeneration requires a device attached and runs on a developer
  machine. One command (`./screenshots/render.sh`) regenerates everything
  uniformly. CI auto-regeneration is **explicitly out of scope** — it would
  require a CLI demo/mock mode, which was considered and declined.
- **No CLI code changes.** The pipeline drives the shipping `wendy` binary as-is.
- Initial flows: **wifi-connect** and **discover**. Additional flows are added by
  dropping a new `tapes/<flow>.tape` and referencing `<CliShot>` — no pipeline
  changes needed.

## Success criteria

1. `./screenshots/render.sh` (with a device attached) regenerates all per-step
   PNGs and animated clips for both themes in one invocation.
2. Every capture shares identical dimensions, font, padding, and window chrome;
   corners are square, matching the site.
3. Light and dark variants exist for every capture and switch automatically with
   the docs site theme.
4. `connecting-to-wifi.mdx` and the discover install steps render via
   `<CliShot>`/`<CliClip>` instead of hand-taken PNGs.
5. Adding a new screenshot is: write a `.tape`, run `render.sh`, drop a
   `<CliShot>` — no manual cropping or theming.
```
