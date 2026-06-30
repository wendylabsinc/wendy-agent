# template.schema.json

`template.schema.json` is an optional file inside a Wendy project template that defines multi-phase, conditional configuration questions. Answers are collected interactively during `wendy init` and become template variables available for rendering any file in the template (Dockerfile, compose.yml, source files, etc.).

It lives alongside `template.json` at the root of a template directory.

## Example

```json
{
  "phases": [
    {
      "id": "project-type",
      "title": "Project Type",
      "questions": [
        {
          "id": "INFERENCE_MODE",
          "label": "Local inference or cloud?",
          "type": "radio",
          "required": true,
          "options": [
            { "value": "local", "label": "Local (on-device)" },
            { "value": "cloud", "label": "Cloud (API)" }
          ]
        }
      ]
    },
    {
      "id": "local-config",
      "title": "Local Model",
      "when": { "questionId": "INFERENCE_MODE", "equals": "local" },
      "questions": [
        {
          "id": "LOCAL_MODEL",
          "label": "Which local model?",
          "type": "radio",
          "required": true,
          "options": [
            { "value": "whisper_base", "label": "Whisper Base" },
            { "value": "vosk",         "label": "Vosk" }
          ]
        }
      ]
    },
    {
      "id": "cloud-config",
      "title": "Cloud Provider",
      "when": { "questionId": "INFERENCE_MODE", "equals": "cloud" },
      "questions": [
        {
          "id": "CLOUD_PROVIDER",
          "label": "Model provider?",
          "type": "radio",
          "required": true,
          "options": [
            { "value": "openai",   "label": "OpenAI" },
            { "value": "deepgram", "label": "Deepgram" }
          ]
        },
        {
          "id": "API_TOKEN",
          "label": "API token",
          "type": "input",
          "required": true,
          "secret": true
        }
      ]
    }
  ]
}
```

## How answers become template variables

Every question `id` becomes a variable accessible in Go template syntax (`{{.VARIABLE_NAME}}`) across all files the template engine renders. This works identically to variables declared in `template.json`.

For **radio** and **input** questions the variable holds the selected string value:

```dockerfile
{{if eq .INFERENCE_MODE "local"}}
RUN apt-get install -y ffmpeg
{{end}}
```

For **checkbox** questions two forms are available:

| Variable | Type | Example |
|----------|------|---------|
| `{{.FEATURES}}` | Comma-separated string | `"gps,camera"` |
| `{{.FEATURES_gps}}` | Boolean | `true` |

```dockerfile
{{if .FEATURES_gps}}
RUN pip install gpsd-py3
{{end}}
```

## Fields

### `phases` *(required)*

Array of phase objects. Phases are evaluated in order; a phase is skipped entirely when its `when` condition is not met.

---

### Phase object

#### `id` *(required)*

Unique identifier for the phase. Used internally; not shown to the user.

#### `title`

Optional heading printed before the phase's questions are shown.

#### `questions` *(required)*

Array of question objects (see below).

#### `when`

Condition that must be true for this phase to be shown. When absent the phase is always shown. See [Conditions](#conditions).

---

### Question object

#### `id` *(required)*

Variable name. Must be unique across all questions in the schema. The answer is stored in `vals[id]` and available as `{{.ID}}` in template files.

Convention: use `UPPER_SNAKE_CASE` to distinguish schema variables from variables declared in `template.json`.

#### `label` *(required)*

Human-readable question text shown in the interactive prompt.

#### `type` *(required)*

| Value | UI | Answer stored as |
|-------|----|-----------------|
| `radio` | Single-select list | Selected option's `value` string |
| `checkbox` | Multi-select checklist | Comma-separated string + per-option booleans |
| `input` | Text prompt | Trimmed string |

#### `options`

Array of option objects. Required for `radio` and `checkbox`. Ignored for `input`.

| Field | Description |
|-------|-------------|
| `value` | The value stored when selected |
| `label` | Human-readable display text |
| `description` | Optional secondary description (shown in picker) |
| `size` | Optional metadata column (e.g., model size) |
| `parameters` | Optional metadata column (e.g., parameter count) |
| `comments` | Optional metadata column (e.g., additional notes) |

When `size`, `parameters`, or `comments` are present, the picker displays a multi-column table view instead of a simple list.

#### `required`

When `true` and `type` is `input`, the prompt rejects empty input.

#### `default`

Default value pre-filled in the prompt. Only used for `input` questions.

#### `secret`

When `true`, hints that the value is sensitive (e.g. an API token). Currently rendered as a standard text prompt; masking is planned.

#### `when`

Condition that must be true for this question to be shown. When absent the question is always shown within its phase. See [Conditions](#conditions).

---

## Conditions

A condition controls whether a phase or question is presented, based on a previously collected answer. The condition references the `id` of an earlier question.

```json
{ "questionId": "INFERENCE_MODE", "equals": "local" }
```

### Condition operators

Exactly one of the following must be set:

#### `equals`

Show when the referenced answer equals the given string exactly.

```json
{ "questionId": "INFERENCE_MODE", "equals": "local" }
```

#### `in`

Show when the referenced answer equals any value in the array.

```json
{ "questionId": "INFERENCE_MODE", "in": ["local", "hybrid"] }
```

#### `contains`

Show when the referenced answer (a comma-separated checkbox result) includes the given value.

```json
{ "questionId": "FEATURES", "contains": "gps" }
```

---

## Evaluation order

Phases are evaluated top-to-bottom. Within a phase, questions are evaluated top-to-bottom. A `when` condition on a question is tested against **all answers collected so far**, including answers from the current phase that appeared before it.

This means you can gate a question on the answer to a question in the same phase, as long as the gating question appears first.

---

## Using variables in template files

The template engine is Go's `text/template`. All standard actions are available: `{{if}}`, `{{range}}`, `{{with}}`, etc.

**Common patterns:**

```dockerfile
# Conditional block
{{if eq .INFERENCE_MODE "local"}}
RUN apt-get install -y libsndfile1
{{end}}

# Nested conditions
{{if eq .INFERENCE_MODE "cloud"}}
  {{if eq .CLOUD_PROVIDER "openai"}}
ENV OPENAI_KEY={{.API_TOKEN}}
  {{end}}
{{end}}
```

```yaml
# compose.yml topology
services:
  app:
    build: .
{{if eq .INFERENCE_MODE "local"}}
  model-server:
    image: ollama/ollama:latest
{{end}}
```

```toml
# pyproject.toml dependencies
dependencies = [
{{if eq .INFERENCE_MODE "local"}}
    "openai-whisper>=20240930",
{{end}}
{{if eq .INFERENCE_MODE "cloud"}}
    "openai>=1.0",
{{end}}
]
```

---

## Relationship to template.json

`template.json` and `template.schema.json` serve complementary roles:

| File | Purpose | Question types |
|------|---------|----------------|
| `template.json` | Simple scalar variables (port numbers, version strings, app ID) | Free text / integer / boolean |
| `template.schema.json` | Structured, conditional decisions that reshape the generated project | Radio, checkbox, input with phases |

Variables from both files are merged into a single map before rendering. If the same `id` appears in both, the `template.json` variable wins (it is collected first).

---

## JSON Schema

A formal JSON Schema for this file format is available at [`template.schema-def.json`](./template.schema-def.json).

Add it to your `template.schema.json` to get editor validation:

```json
{
  "$schema": "https://wendy.dev/schemas/template.schema.json",
  "phases": [ ... ]
}
```
