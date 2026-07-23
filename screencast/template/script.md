# AI-generated screencasts from one script

Audience: Wendy contributors and engineering teammates.
Goal: Show how AI agents can turn feature context and one script into a complete narrated screencast, while the render pipeline remains deterministic for humans and agents alike.
Tone: Calm, practical, developer-focused.

---

## 01 Title

### Say

AI-generated screencasts from one script.

In this screencast, we will show how feature context and one source file can become a complete narrated video, while the render pipeline stays deterministic and reviewable.

### Show (slide)

```text
AI-generated screencasts
from one script

script.md → scenes → renders → final MP4
```

---

## 02 Payoff first

### Say

A screencast that used to take hours, or even days, can now be produced in minutes.

Once an AI agent finishes developing a feature, it already has the context: what changed, why it matters, how to demo it, and what reviewers should notice. The agent can draft the script, generate scenes, render voiceover, record terminal demos, and stitch the final video alongside the pull request.

Humans can still review the story and provide real footage when needed, but the first complete video can be produced autonomously.

### Show (slide)

```text
Before: feature PR, then manual video production
After:  feature PR + AI-generated screencast

script → scenes → renders → final MP4

minutes, not hours or days
```

---

## 03 Why this exists

### Say

Engineering screencasts usually involve many disconnected artifacts: a script, slides, terminal demos, voiceover, screen recordings, timing decisions, and final assembly.

When those steps are manual, the story and the rendered output drift apart. This workflow gives the agent a clear source of truth and gives humans a repeatable pipeline they can inspect and run themselves.

### Show (slide)

```text
Without structure

script
slides
terminal recording
screen capture
voiceover
final edit

→ easy to drift
→ hard to repeat
→ hard for agents to own
```

---

## 04 Source of truth

### Say

The source of truth is `screencast/script.md`.

It describes the audience, goal, tone, and each scene. For every scene, it says what the narration should say and what the viewer should see.

The script is not a build file. It is the creative plan. The agent uses it to generate renderable scene folders.

### Show (code)

```md
## 03 Developer flow

### Say

Explain what the developer does and what changes.

### Show (terminal)

wendy run --device example-device
```

---

## 05 Agent first pass

### Say

After the script is ready, the agent creates one folder per scene under `screencast/scenes`.

For each scene, it writes `voice.md` for narration and `slide.md` for the visual fallback. If the scene needs a terminal demo, it writes a `vhs.tape`.

If a scene eventually needs a real screen recording, the agent still creates a placeholder slide in the first pass. That keeps the whole screencast renderable before final footage exists.

### Show (diagram)

```text
script.md
  ↓ agent
scenes/
  01-intro/
    slide.md
    voice.md
  02-terminal-demo/
    voice.md
    vhs.tape
    vhs.sh         # optional command verifier
  03-ui-demo/
    slide.md        # placeholder until user provides video
    voice.md
```

---

## 06 Render pipeline

### Say

Under the hood, the workflow is mechanical.

Each scene is rendered independently. A slide scene becomes `slide.md.mp4`. A terminal scene becomes `vhs.tape.mp4`. If the scene includes `vhs.sh`, that verifier runs before recording so path mistakes and command failures are caught before VHS captures the terminal. Narration becomes `voice.md.mp3`.

After each scene has visual media and optional audio, the stitcher reads the scene folders in order and creates the final MP4. This is the same pipeline whether a human runs it manually or an AI agent runs it autonomously.

### Show (terminal)

```sh
cd screencast

scripts/render-slide 01
scripts/render-voice 01

scripts/render-tape --dry-run --with-hooks 06
scripts/render-tape --with-hooks 06   # runs scenes/06-*/vhs.sh first, if present
scripts/render-voice 02

scripts/stitch scenes/* --output output/feature-name.mp4
```

---

## 07 Scene artifacts

### Say

The naming convention is intentionally simple.

Generated files keep the source filename and append the output extension. So `slide.md` renders to `slide.md.mp4`, `voice.md` renders to `voice.md.mp3`, and `vhs.tape` renders to `vhs.tape.mp4`.

That makes every generated artifact traceable back to the source file that produced it.

### Show (slide)

```text
slide.md   → slide.md.mp4
voice.md   → voice.md.mp3
vhs.tape   → vhs.tape.mp4

source file + output extension
```

---

## 08 Stitching and timing

### Say

The stitcher chooses one visual source per scene. A real `video.mp4` wins. If that is not present, it looks for another scene-local video. If there is a terminal tape, it uses `vhs.tape.mp4`. Otherwise it falls back to the rendered slide.

Scene duration is also calculated mechanically: the scene lasts as long as the longer input, voiceover or visual media. If narration is longer, the final visual frame is frozen. If video is longer, audio is padded with silence.

### Show (slide)

```text
Visual source priority

1. video.mp4
2. one scene-local video
3. vhs.tape.mp4
4. slide.md.mp4

scene duration = max(voice duration, visual duration)
```

---

## 09 Human second pass

### Say

Some visuals cannot be generated by the agent. Real UI interactions, app demos, browser sessions that require credentials, or external videos should be recorded by the human.

In that second pass, the human drops the recording into the relevant scene folder using the expected naming convention. The same stitch command can be run again, and the placeholder slide is replaced automatically.

### Show (slide)

```text
Second pass

Human provides:
  scenes/03-ui-demo/video.mp4

Agent reruns:
  scripts/stitch scenes/* --output output/feature-name.mp4

Placeholder slide is replaced automatically.
```

---

## 10 Safety and CI

### Say

The workflow is text-first and reviewable.

The agent should commit source files: the script, scene Markdown, tape files, hooks, and configuration. It should not commit generated media.

CI checks enforce that, along with npm audit policy, script validation, hook checksums, secret scanning, and browser-capture guardrails.

### Show (slide)

```text
Commit source:
  script.md
  scenes/*/slide.md
  scenes/*/voice.md
  scenes/*/vhs.tape

Do not commit generated media:
  *.mp4
  *.mp3
  *.webm
  output/*

CI validates the guardrails.
```

---

## 11 Closing

### Say

To use it, start with feature context or `screencast/script.md`.

Ask the agent to generate and render the screencast. It will create the scenes, render the assets, generate voiceover, and stitch the final MP4.

If any scene needs real footage, provide that video in a second pass and let the agent stitch again. The detailed workflow lives in `screencast/README.md`.

### Show (slide)

```text
Ask the agent:
  Generate and render the screencast.

Second pass:
  provide real videos where needed
  rerun stitch

Source of truth:
  screencast/README.md
```

---

## 12 Thanks

### Say

Thanks for watching. For questions or follow-up, contact Konstantin at konstantin at wendy dot dev, or see the pull request linked on screen.

### Show (slide)

```text
Thanks for watching

Contact:
  konstantin@wendy.dev

PR:
  https://github.com/wendylabsinc/WendyOS/pull/1082
```
