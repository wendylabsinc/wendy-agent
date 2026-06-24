# `wendy auth use`

Sets the default Wendy Cloud session used when several auth sessions are stored and no `--cloud-grpc` flag is given.

## Usage

```sh
wendy auth use [selector]
```

## Description

With a **selector** argument, `wendy auth use` matches exactly one stored session and persists it as the default in `~/.wendy/config.json`. With no argument in an interactive terminal, an interactive picker is shown instead.

The selector is interpreted as:
- A plain integer — matched against the organization ID in the session's certificate.
- Any other string — a case-insensitive substring match against the gRPC endpoint or dashboard URL.

An ambiguous selector (multiple matches) lists the candidates and errors. A selector that matches nothing also errors.

Sessions without certificate material are rejected; re-run `wendy auth login` to refresh them.

## Flags

_None._

## Interactive picker keys

When the session picker is shown (e.g. by `wendy auth use` with no argument, or by any cloud command that reaches the multi-session branch):

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate sessions |
| `enter` | Select this session for the current invocation only |
| `d` | Persist the highlighted session as the default (equivalent to running `wendy auth use`) |
| `x` | Clear the current default |
| `q` / `Ctrl+C` | Cancel |

The current default session is marked with `✦`.

## Examples

```sh
# Set default by org ID
wendy auth use 7

# Set default by endpoint substring
wendy auth use prod.example.com

# Interactive picker (TTY only)
wendy auth use
```

## See also

- [`wendy auth default`](./default.md) — show or clear the persisted default
- [`wendy auth login`](./login.md)
