---
description: Create or adapt a Wendy app from the Wendy templates repo.
argument-hint: [template name, app id, language, destination path, or app idea]
---

Use the `wendy-template-app` skill. Search local templates first, then `wendylabsinc/templates` if local source is missing or unclear, and prefer adapting the closest existing template before writing a scaffold from scratch.

`$ARGUMENTS`

Inspect `template.json`, `wendy.json`, `Dockerfile`, and main source files before creating the destination app. Use explicit `wendy init` flags when possible; otherwise copy and adapt the template, then validate `wendy.json`.
