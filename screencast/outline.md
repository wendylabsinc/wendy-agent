# wendy.json file sync screencast

Audience: engineering peers and engineering management.

Tone: technical and direct. The video explains the product rationale and the runtime behavior without going into unrelated platform experiments.

Goal: show why top-level `wendy.json.files` exists, how an app declares deployment inputs, what `wendy run` does with them, and what guardrails make the feature safe enough for day-to-day development.

Key beats:

1. The problem: development inputs such as model weights, prompts, calibration files, and fixtures often change independently from application code.
2. The config: declare those inputs once in `wendy.json` using relative `path` and optional `to`.
3. The run behavior: `wendy run` builds the app image, syncs declared inputs into an app-scoped managed directory, and mounts them read-only at the app working directory.
4. The lifecycle: updates replace changed inputs, stale entries are removed, and app deletion cleans synced inputs because they are not persistent data.
5. The engineering boundary: single-service support now; multi-service and Compose fail explicitly and are tracked separately.

Call to action: review PR #998 with the feature boundary in mind; broader auth policy or deeper filesystem-hardening work should remain separate from this implementation PR.
