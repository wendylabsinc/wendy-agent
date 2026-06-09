Lists all local caches used by Wendy, mainly downloaded OS images.

Pass `--json` to receive a JSON array on stdout instead of the human-readable output described below.

When `--json` is **not** passed:

- In an **interactive terminal** (stdin and stdout are both TTYs), an interactive picker is shown listing all cached items with their sizes. Selected items can be removed.
- In a **non-interactive** context (pipe, CI, etc.), a plain text list is printed, one entry per line in the format `  <name>  (<size>)`.

When the cache is empty (or the cache directory does not exist), the command prints `Cache is empty.` in human-readable mode, or an empty JSON array (`[]`) with `--json`.

## JSON output

### `wendy cache list --json`

Each element of the returned array represents one cached item:

```json
[
  {
    "name": "os-images/wendyos-raspberry-pi-5-0.10.4.img",
    "path": "/Users/you/Library/Caches/wendy/os-images/wendyos-raspberry-pi-5-0.10.4.img",
    "sizeBytes": 4831838208,
    "size": "4.5 GB"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Display name of the cache entry (OS images are prefixed with `os-images/`) |
| `path` | string | Absolute path to the cached file or directory |
| `sizeBytes` | integer | Size in bytes |
| `size` | string | Human-readable size string |

> **Note:** `path` is a full local filesystem path and may include the current username or CI runner cache location. Redact it before forwarding command output to shared logs or support channels.

Returns `[]` when the cache is empty.

### `wendy os cache list --json`

Each element represents one cached OS image file:

```json
[
  {
    "name": "wendyos-raspberry-pi-5-0.10.4.img",
    "sizeBytes": 4831838208,
    "size": "4.5 GB"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Filename of the cached OS image |
| `sizeBytes` | integer | Size in bytes |
| `size` | string | Human-readable size string |

Returns `[]` when no cached OS images are found.
