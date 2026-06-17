# Wendy File Sync presentation

Slidev presentation and narrated-video sources for WDY-1532 file sync.

## Source model

- `deck/slides.md` — source deck, including presenter notes.
- `deck/style.css` — intentionally empty; use the default Slidev style for now.
- `voiceover/text/*.txt` — narration text, one file per timeline step.
- `timeline.json` — render order and timing.
- `scripts/generate-tts.sh` — OpenAI TTS generation.
- `scripts/render-deck.mjs` — renders the deck plus voiceover to `output/screencast.mp4`.

Generated media is ignored by git:

- `voiceover/mp3/*.mp3`
- `output/*`
- `deck/public/videos/*`
- `deck/public/images/*`

## Run locally

```sh
npm install
npm run present
```

## Generate voiceover

Requires `OPENAI_API_KEY`.

```sh
scripts/generate-tts.sh --dry-run
scripts/generate-tts.sh
```

Default TTS settings:

```sh
OPENAI_TTS_MODEL=gpt-4o-mini-tts
OPENAI_TTS_VOICE=alloy
```

## Render video

```sh
scripts/render-deck.mjs
```

Output:

```text
output/screencast.mp4
output/duration-report.tsv
```

## Validate

```sh
scripts/check.sh
```
