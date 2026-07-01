Selects a device as the [default device](../../device-selection.md), so that other commands default to this device if available.

The default can be a hostname, IP address, provider key, or explicit `host:port` value:

```sh
wendy device set-default my-mac.local:50051
wendy device info --json
wendy run
```

Use `wendy device get-default` to see the current default, or `wendy device unset-default` to clear it.

## Certificate pinning

When you set a default device (and again on the first successful connection if the device was offline at set-default time), the CLI **pins** the device's identity — the organisation and cloud host its TLS certificate belongs to. On every later connection to the default device, the CLI checks that the device still presents that same organisation and cloud host.

A routine certificate **renewal or re-enrollment** keeps the same organisation and cloud, so it is accepted silently. Only a change of organisation or cloud host — which can indicate a man-in-the-middle or a swapped device — triggers a red warning and an interactive prompt asking whether to trust the new identity. In non-interactive mode (`--json` or no TTY) the connection is refused; re-run `wendy device set-default` to deliberately re-pin.
