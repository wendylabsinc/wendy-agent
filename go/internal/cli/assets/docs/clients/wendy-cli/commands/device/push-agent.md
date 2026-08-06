> **Developer / diagnostic tool.** This command is hidden from `wendy device --help`. It is not part of the normal update workflow.

Uploads a locally-built `wendy-agent` binary to the device using the same `UpdateAgent` RPC that `wendy device update` uses, bypassing the GitHub release fetch. After upload the CLI waits for the restarted agent to become reachable before returning.

## Usage

```sh
wendy device push-agent <path-to-agent-binary>
```

## When to use this

Use `push-agent` when you need a patched or development agent running on a device to capture verbose engine diagnostics (e.g. raw EFI variables via `wendy os update-status --json`) without SSH access. For routine agent updates, use [`wendy device update`](update.md).

## Difference from `wendy device update --binary`

`wendy device update --binary` performs an OS-update check after uploading the binary and re-uploads the binary if an OS update is applied. `push-agent` performs no OS update check — it uploads the binary and waits for the agent restart only.

## Arguments

| Argument | Description |
|---|---|
| `<path-to-agent-binary>` | Path to the compiled `wendy-agent` binary to upload. |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Agent uploaded and restarted successfully. |
| non-zero | Binary could not be read, upload failed, or the agent did not come back online. |
