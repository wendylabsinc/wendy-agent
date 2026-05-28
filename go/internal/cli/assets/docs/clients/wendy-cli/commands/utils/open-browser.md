Opens a URL using the preferred mechanism on the current OS.

This is useful for `wendy.json` postRun scripts that want to open your (web) app in a web-browser.

## Automatic opening

The underlying browser opener automatically opens only `http` and `https` URLs. The URL must include a host, must not include credentials, and must not begin with `-`. If the browser opener cannot open the URL, the command prints the URL to stdout and a diagnostic to stderr so it can be opened manually.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | The browser was opened successfully. |
| Non-zero | The browser could not be opened, or the command arguments were invalid. When opening fails after URL validation, the URL is printed to stdout and a diagnostic is written to stderr. |
