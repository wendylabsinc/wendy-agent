Opens a URL using the preferred mechanism on the current OS.

This is useful for `wendy.json` postRun scripts that want to open your (web) app in a web-browser.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | The browser was opened successfully. |
| `1` | The browser could not be opened. The URL is printed to stdout and a diagnostic is written to stderr. |
