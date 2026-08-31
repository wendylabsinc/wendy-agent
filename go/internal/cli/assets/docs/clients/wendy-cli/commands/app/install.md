Installs an app from the [Wendy AppStore](https://appstore.wendy.dev) onto a target device.

```sh
wendy app install <app-id> [flags]
```

This command is also available as `wendy device apps install <app-id>` for device-scoped workflows.

`wendy app install` resolves the given AppStore app ID to an OCI image reference by querying the Wendy Cloud resolution API (`GET /v1/apps/{id}/image`), then deploys the image to the target device. By default it also starts the container immediately with the **`UNLESS_STOPPED`** restart policy.

> **Note:** The AppStore resolution API is still being finalized as a public endpoint. If `wendy app install` cannot resolve an app ID, verify the resolution API is reachable or override the endpoint with `--api`. Check [appstore.wendy.dev](https://appstore.wendy.dev) for the latest supported app IDs.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--api` | `$WENDY_APPSTORE_API` or built-in default | Override the AppStore resolution API base URL. |
| `--no-start` | false | Create the container but do not start it. |

The global `--device` flag selects the target device.

## Environment variables

| Variable | Description |
|----------|-------------|
| `WENDY_APPSTORE_API` | Override the AppStore resolution API base URL. Takes precedence over the built-in default; `--api` takes precedence over this variable. |

## Resolution API

The CLI sends `GET {api}/v1/apps/{app-id}/image` and expects a JSON response:

```json
{
  "app_id": "jellyfin",
  "source": "dockerhub",
  "repository": "jellyfin/jellyfin",
  "tag": "latest",
  "reference": "docker.io/jellyfin/jellyfin:latest"
}
```

If the app ID is not found in the AppStore, the command exits with an error. If `reference` is empty in the response, the command also exits with an error.

## Examples

```sh
# Install from the AppStore and start immediately
wendy app install jellyfin

# Install without starting
wendy app install jellyfin --no-start

# Install onto a specific device
wendy --device 192.168.1.42 app install jellyfin

# Use a custom resolution API
wendy app install jellyfin --api https://staging-api.wendy.sh
```

## Related

- [`wendy device apps list`](../device/apps/list.md) — list installed apps on a device
- [`wendy device apps start`](../device/apps/start.md) — start an installed app
- [`wendy device apps stop`](../device/apps/stop.md) — stop a running app
- [`wendy device apps remove`](../device/apps/remove.md) — remove an app from a device
- [Wendy AppStore](https://appstore.wendy.dev)
