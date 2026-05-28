Opens a URL using the preferred mechanism on the current OS.

This is useful for `wendy.json` postRun scripts that want to open your (web) app in a web-browser.

## Automatic opening

The underlying browser opener automatically opens only `http` and `https` URLs. The URL must include a host, must not include credentials, and must not begin with `-`. If the browser opener cannot open the URL, the command prints the URL to stdout and a diagnostic to stderr so it can be opened manually.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | The browser was opened successfully, or the browser could not be opened and the URL was printed to stdout with a diagnostic on stderr. |
| Non-zero | The command arguments were invalid, such as a missing URL, malformed URL, missing scheme, or missing host for an `http`/`https` URL. |
