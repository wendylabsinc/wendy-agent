# `wendy device keys`

Manages the operator keys a device that is **not enrolled in Wendy Cloud** trusts. The key of the computer that installed the device is its first owner key (see [`wendy install`](../os/install.md#your-key-is-the-devices-first-owner-key)). Every command needs an owner key.

```sh
wendy device keys list
wendy device keys add <public-key-file|-> --role <owner|operator|viewer> [--label <name>]
wendy device keys remove <fingerprint>
```

## Roles

| Role | May |
| --- | --- |
| `owner` | everything, including managing keys and unenrolling |
| `operator` | apps and containers, logs, shell, tunnel, drivers, device settings, updates |
| `viewer` | reads only: info, lists, status, logs |

The last owner key cannot be removed. If every owner key is lost, reinstall the device.

## Adding another operator

1. On the other operator's computer: `wendy auth local-key show > alice.pub`.
2. On an owner's computer: `wendy device keys add alice.pub --role operator --label alice`.

`add` reads the first PEM `PUBLIC KEY` block in the file, so the whole output of `wendy auth local-key show` works as is; `-` reads it from stdin. A private key is refused. `list` marks this computer's own key.

## How the CLI connects

The CLI tries your Wendy Cloud operator certificates first, then your local operator key. A device that is not enrolled accepts only the local key; a cloud-enrolled device accepts only cloud certificates. The CLI never falls back to an unauthenticated connection for a device that answered over TLS:

- A device that does not trust your key: the CLI prints your key's fingerprint and asks you to have an owner run `wendy device keys add`.
- A cloud-enrolled device and no `wendy auth login` session: the CLI asks you to log in to the device's organization.
- The device key is pinned on first contact under the name you connect with. A different key later (a reinstall, or another device answering under that name) is refused; after a deliberate reinstall run `wendy device unpin <name>`.

## While the device is enrolled

Cloud manages access. The device keeps the keys file but uses it nowhere, and every `keys` command is refused with the device's message, `local operator keys are managed by cloud while enrolled`. The keys apply again after the device is unenrolled.
