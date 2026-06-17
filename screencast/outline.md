# wendy.json file sync screencast

Audience: engineering peers and engineering management.

Tone: technical, upbeat, and product-oriented. Use slides for rationale and boundaries; use terminal only to show the essential config and run flow.

Goal: explain why top-level `wendy.json.files` exists, how a project declares large app assets, what `wendy run` does with them, and what guardrails define the first implementation.

Scene beats:

1. Title: file sync for `wendy run`.
2. Slide: inputs change faster than images.
3. Slide: declare once, sync on run, mount read-only.
4. Terminal: show the compact `wendy.json.files` shape and app reads.
5. Terminal: run, update one prompt, run again.
6. Slide: guardrails and first-PR boundary.
7. Closing: review/merge path.

Avoid unrelated platform details. The story is large app assets, developer workflow, and clear lifecycle semantics.
