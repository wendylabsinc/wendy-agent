# `wendy cloud logout`

Removes the stored Wendy Cloud auth session. Equivalent to `wendy auth logout`.

## Usage

```sh
wendy cloud logout
```

## Description

`wendy cloud logout` clears the stored mTLS session from `~/.wendy/config.json`.
It reuses the same implementation as the (now hidden) `wendy auth logout` and
remains the surfaced entry point for signing out.

## See also

- [`wendy cloud login`](./login.md)
- [`wendy cloud status`](./status.md)
