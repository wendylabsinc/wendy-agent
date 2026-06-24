Selects a device as the [default device](../../device-selection.md), so that other commands default to this device if available.

The default can be a hostname, IP address, provider key, or explicit `host:port` value:

```sh
wendy device set-default my-mac.local:50051
wendy device info --json
wendy run
```

Use `wendy device unset-default` to clear the saved target.
