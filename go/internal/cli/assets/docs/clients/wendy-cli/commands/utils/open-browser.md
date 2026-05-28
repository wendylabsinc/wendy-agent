Opens a URL using the preferred mechanism on the current OS.

This is useful for `wendy.json` postRun scripts that want to open your (web) app in a web-browser.

## Accepted URLs

Only `http` and `https` URLs are accepted. The URL must include a host, must not include credentials, and must not begin with `-`. Any other scheme, embedded credentials, a missing host, or a URL starting with `-` is rejected immediately with a non-zero exit code.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | The browser was opened successfully. |
| `1` | The browser could not be opened, or the URL was rejected as invalid. The URL is printed to stdout and a diagnostic is written to stderr. |
