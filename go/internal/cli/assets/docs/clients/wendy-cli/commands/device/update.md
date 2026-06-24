Updates the wendy-agent installation on the remote device. By default downloads the latest release binary from GitHub matching the device's CPU architecture. Pass `--binary <path>` to upload a locally built binary instead (e.g. a cross-compiled development build). The command waits for the restarted agent to become reachable before reporting success.

GitHub release lookups use the `GITHUB_TOKEN` environment variable for authentication when it is present, and fall back to unauthenticated requests otherwise.

> **TODO**: On ubuntu machines, this should use `apt upgrade wendy-agent`
> **IDEA**: It could also prompt the question to run `wendy os update` if needed, as to avoid confusion.
