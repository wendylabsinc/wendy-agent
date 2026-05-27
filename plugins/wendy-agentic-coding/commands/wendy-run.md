---
description: Build, deploy, run, or detach a Wendy app.
argument-hint: [app path, hostname, detach/deploy mode, or build type]
---

Use the `wendy-app-lifecycle` skill. Run or deploy the app for:

`$ARGUMENTS`

Prefer `wendy run --yes --detach --device <hostname>` for long-running apps that the coding agent should start and then inspect with logs. Use attached `wendy run --yes --device <hostname>` only when live stdout/stderr is the desired foreground interaction. Use `--build-type docker|compose|swift|python` to avoid build-type ambiguity.
