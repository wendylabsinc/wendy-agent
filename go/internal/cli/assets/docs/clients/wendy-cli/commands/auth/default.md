# `wendy auth default`

Shows or clears the persisted default Wendy Cloud session.

## Usage

```sh
wendy auth default [--clear]
```

## Description

With no flags, prints the current default session (`org N — endpoint`). If the stored default points at a session that no longer exists (stale default), it is automatically cleared and a warning is printed.

Pass `--clear` to remove the default without showing it.

## Flags

| Flag | Description |
|------|-------------|
| `--clear` | Unset the default session. |

## Examples

```sh
# Show the current default
wendy auth default

# Clear the default
wendy auth default --clear
```

## See also

- [`wendy auth use`](./use.md) — set the default
