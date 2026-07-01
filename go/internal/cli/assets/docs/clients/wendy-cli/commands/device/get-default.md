
> **Note:** `wendy device get-default` is not listed in `wendy device --help`,
> but it remains fully functional.

Shows the current [default device](../../device-selection.md), if one is set.

Use this to confirm what is persisted in the CLI configuration:

```sh
wendy device get-default
```

Use [`set-default`](./set-default.md) to change the saved target, or [`unset-default`](./unset-default.md) to clear it.