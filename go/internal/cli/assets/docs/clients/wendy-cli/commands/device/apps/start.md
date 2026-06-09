Starts an app by name. If the app-name argument is not provided, and the terminal is interactive - a list of all uploaded apps is shown. You can then interactively start an app.

By default, after starting the app the CLI attaches to the container's output stream and prints logs to the terminal. Press **Ctrl-C** to detach.

Starting an app always applies the **`UNLESS_STOPPED`** restart policy, so the agent automatically restarts the container if it exits unexpectedly. The container only stays stopped when it is explicitly stopped (e.g. via `wendy device apps stop`).

## Flags

| Flag | Description |
|------|-------------|
| `-d`, `--detach` | Start the app and return immediately without streaming output. |

## Examples

Start an app and stream its output:

```sh
wendy device apps start my-app
```

Start an app in the background (detached):

```sh
wendy device apps start --detach my-app
wendy device apps start -d my-app
```

When `--detach` is used, the CLI sends the start request to the agent and exits as soon as the container start is initiated, without waiting for or streaming any output.
