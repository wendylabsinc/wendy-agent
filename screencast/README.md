# Screencast production

Reusable workflow for producing engineering screencasts with terminal clips,
optional UI/docs clips, generated voiceover, and a deterministic final render.

This directory is the reusable screencast scaffold. Copy the whole
`screencast/` directory when starting a new screencast, or edit it in place for a
single project-specific video.

## Asset model

- `tapes/*.tape` — VHS terminal recordings.
- `recordings/` — generated or manually captured video clips (`.mp4`, `.mov`).
- `voiceover/text/*.txt` — voiceover scripts, one file per narrated scene.
- `voiceover/mp3/*.mp3` — generated TTS audio. Ignored by git.
- `scene-plan.tsv` — ordered scene list used by the planning/reporting/render scripts.
- `title-card.svg` / `title-card.env` — editable intro-card source and metadata.
- `closing-card.svg` / `closing-card.env` — editable closing-card source and metadata.
- `stitch/output/` — final rendered video. Ignored by git.

Generated media is intentionally not committed. Keep the scripts, scene plan,
title card sources, text scripts, and tape sources in git; regenerate clips when
needed.

## Building blocks

A screencast is assembled from ordered scenes. Useful scene types include:

- **Slides/cards** — static or lightly animated SVG/PNG cards for intros,
  section breaks, diagrams, summary points, and closing/contact details.
- **VHS terminal tapes** — deterministic terminal recordings from `tapes/*.tape`.
  Use these for commands, logs, CLIs, REPLs, and repeatable developer flows.
- **Screen recordings** — captured application, desktop, or device UI footage.
  Use these when the real interface matters more than deterministic replay.
- **Live coding** — editor-centric footage where code is written, refactored,
  or debugged in real time. This is useful when the thought process matters;
  keep edits focused and consider speeding up repetitive typing.
- **Browsing** — website or web-app navigation captured as a user would
  experience it. Use this for product pages, docs exploration, dashboards,
  account flows, and hosted demos.
- **B-roll footage** — supplemental real-world or atmospheric footage, such as
  devices, hardware, people working, lab/office shots, or product context. This
  is the common video-production name for general supporting footage.
- **Scripted browser/docs captures** — reproducible browser recordings for docs
  pages, dashboards, changelogs, or web apps. These are a structured variant of
  browsing/screen recording when repeatability matters.
- **Overlays** — callouts, captions, arrows, highlights, lower thirds, or labels
  composited over another scene during editing/rendering.
- **Audio-only beats** — silence, room tone, music stings, or narration-only
  pauses used to control pacing between visual scenes.

Every scene still resolves to a video file in `recordings/` plus optional
voiceover audio in `voiceover/mp3/` before final rendering.

## Automation approach

This scaffold is designed so an AI agent can assemble a screencast from a small
set of declarative inputs rather than hand-editing a timeline. The agent should
produce or update:

1. A short narrative outline: audience, goal, key beats, and call to action.
2. A scene list that chooses one building block per beat.
3. Source assets for each scene: slide metadata, VHS tapes, capture URLs,
   code-reveal snippets, screen-recording notes, or b-roll placeholders.
4. Voiceover text files, one per narrated scene.
5. `scene-plan.tsv`, using minimum useful durations first and letting the
   planner expand scenes to fit generated voiceover.
6. A duration report and final render.

Prefer generated/reproducible sources where possible. Commit plans, scripts,
tape files, SVG/card metadata, and voiceover text; do not commit generated media.

### Scripted code reveal

For live-coding-like segments, prefer a **scripted code reveal** over recording
an editor session by hand. The source of truth can be a patch, a before/after
file pair, or a sequence of small snippets. The generated footage should reveal
code progressively in an editor-like frame, optionally with highlights or pauses
at important lines.

This keeps the segment deterministic while preserving the feel of live coding.
It also avoids fragile typing timing, autocomplete popovers, local editor state,
and accidental secrets. VS Code styling is a good visual target, but the exact
implementation is intentionally left for later: HTML/SVG/browser rendering,
editor automation, or a dedicated recorder are all possible.

### AI-assisted production workflow

A good agent loop is:

1. **Plan** — write a scene outline with building block, purpose, target length,
   and voiceover intent for each scene.
2. **Draft sources** — create or update card metadata, VHS tapes, code-reveal
   snippets, browser-capture notes, and voiceover text.
3. **Generate media** — render cards, record VHS/browser/code scenes, and create
   TTS voiceover. Keep generated files ignored by git.
4. **Plan durations** — run the duration planner and adjust minimum durations or
   voiceover text when pacing feels off.
5. **Render** — assemble the final video with `scripts/render.sh`.
6. **Review** — watch the output, check the duration report, then iterate on the
   smallest source asset that fixes the issue.

The agent should treat generated footage as disposable build output. If a scene
needs correction, change the declarative source and regenerate it rather than
manually editing the rendered video.

## Standard format

Use the display's logical aspect ratio, with encoder-safe even dimensions:

```text
1440 × 900, 10 fps
```

For VHS clips, use:

```tape
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
```

The title card uses the same terminal-style defaults: JetBrains Mono
when available, a `#171717` background matching VHS's default terminal
background, and light monospace text. The fallback stack also includes common
JetBrains Mono Nerd Font family names for setups that install the patched font.
Install the font locally before recording if you want exact typography;
otherwise VHS/SVG rendering will use a monospace fallback.

Disable cursor blinking for terminal clips. The renderer may freeze the final
frame when a scene needs padding; a non-blinking cursor makes that freeze look
intentional instead of like a stuck blink state.

`1440 × 900` is exact 16:10, close to a non-Retina MacBook-sized canvas, and
uses encoder-safe even dimensions. Override the defaults with
`SCREENCAST_WIDTH`, `SCREENCAST_HEIGHT`, and `SCREENCAST_FPS` if your project
needs another format.

## Scene plan

`scene-plan.tsv` columns:

```text
scene_id    video_path    min_seconds    voiceover_path    final_seconds
```

Paths are relative to this screencast directory. Leave `voiceover_path`
empty for silent scenes. Leave `final_seconds` empty for automatic planning.

Duration rule:

```text
render duration = final_seconds ?? max(min_seconds, voiceover duration)
```

If voiceover is shorter than the render duration, the renderer pads with
silence. If voiceover is longer than `min_seconds`, the render duration expands
to fit the voiceover. If the source video is shorter than the render duration,
the renderer freezes the final frame.

Use `final_seconds` only when you need a fixed duration. Rendering fails if an
explicit final duration is shorter than the minimum or shorter than the
voiceover.

## Create intro and closing cards

Edit `title-card.env` for title, subtitle, author, place, and date, then run:

```sh
scripts/create-title-card.sh
```

This substitutes metadata into `title-card.svg`, rasterizes it, and writes
`recordings/00-title-card.mp4`.

Edit `closing-card.env` for title, subtitle, website, contact details, author,
place, and date, then run:

```sh
scripts/create-closing-card.sh
```

This writes `recordings/99-closing-card.mp4`. The scaffold keeps the website
generic; set `WEBSITE` to your project or talk URL in a copied screencast
folder.

## Generate voiceover

Requires `OPENAI_API_KEY`.

```sh
scripts/generate-tts.sh --dry-run
scripts/generate-tts.sh
```

This reads `voiceover/text/*.txt` and writes matching `.mp3` files under
`voiceover/mp3/`. Dry-run mode prints word counts and rough duration estimates
without calling the API.

## Plan durations

After recording clips and generating voiceover, compute planned render lengths:

```sh
scripts/plan-durations.py scene-plan.tsv
scripts/plan-durations.py scene-plan.tsv --format markdown
```

The report includes source video length, voiceover length, recommended final
length, padding, and blocking issues such as missing media or an explicit final
duration that is too short.

## Record docs page

```sh
scripts/record-docs-page.mjs \
  https://docs.example.com/page \
  recordings/10-docs-page.mp4
```

## Render final video

```sh
scripts/render.sh
```

Output:

```text
stitch/output/screencast.mp4
stitch/duration-report.tsv
```

## Validation

Run syntax and generated-media checks:

```sh
scripts/check.sh
```

Run a strict planning pass before render:

```sh
scripts/plan-durations.py scene-plan.tsv --strict
```
