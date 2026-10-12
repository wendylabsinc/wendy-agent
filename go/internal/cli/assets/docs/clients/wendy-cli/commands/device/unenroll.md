# `wendy device unenroll`

Removes a device's enrollment from Wendy Cloud. Cloud mode works when the device
is offline or its firmware cannot run.

## Usage

```sh
wendy device unenroll [--device <selector>] [--cloud-only] [--cloud-grpc <endpoint>] [--yes]
wendy cloud device unenroll --device <asset-id-or-name> [--cloud-grpc <endpoint>] [--yes]
```

## Remove an offline device

Find the old asset, then remove it using its asset ID, name, or PKI device ID:

```sh
wendy cloud discover --all --json
wendy cloud device unenroll --device <asset-id-or-name>
```

For example, an enrollment conflict for `lite-441bf6804824` can be resolved with:

```sh
wendy cloud device unenroll --device lite-441bf6804824
```

`wendy device unenroll --cloud-only --device <asset-id-or-name>` performs the same
operation. Cloud mode authenticates with your stored cloud session, revokes the
device's certificates and bootstrap credentials, and deletes its cloud asset.
It releases the asset's device ID for another enrollment without connecting to
the device. Local enrollment credentials remain on the device.

## Remove a reachable device

```sh
wendy device unenroll --device <device-selector>
```

For a WendyOS agent with numeric Cloud v1 enrollment, this clears the device's
enrollment and provisioning state, restarts the agent, and removes its cloud
enrollment. UUID/direct PKI enrollment first verifies the Cloud binding and
completes Cloud-owned revocation and deletion, then verifies the evidence before
resetting local state. This path requires compatible Cloud and Agent versions
and a directly verified mTLS connection. See the
[unenrollment compatibility requirements](./enroll.md#unenrollment-compatibility).

For Wendy Lite, this reads the hardware ID over the selected USB, LAN, or
Bluetooth connection and removes the matching cloud enrollment. The firmware
does not support erasing local identity through WendyCom, so the command leaves
the board's configuration and credentials in place. If a certificate was already
issued, operator recovery must erase or reset the dedicated `wendy_pki` identity
partition before the board can enroll again. Rebooting or replacing its
configuration does not clear that identity.

A `cloud://` selector supplied through `--device` or the saved default uses cloud
removal directly, without opening a device connection.

## Flags

| Flag | Description |
|------|-------------|
| `--device` | Device selector. In cloud mode, accepts a cloud asset ID, name, or PKI device ID. |
| `--cloud-only` | Remove the cloud enrollment without connecting to the device. Available on `wendy device unenroll`. |
| `--cloud-grpc` | Cloud gRPC endpoint for the stored session used to remove the enrollment. |
| `--asset-id` | Exact Cloud asset UUID for a directly connected WendyOS v2 enrollment. Otherwise resolved from its authenticated PKI binding. Unavailable for cloud mode and Wendy Lite. |
| `--check` | Check the binding and existing ACME account lookup for a directly connected WendyOS v2 enrollment without revoking, deleting, or resetting. Unavailable for cloud mode and Wendy Lite. |
| `--yes` | Skip confirmation. Required when running non-interactively. |

## Related

- [`wendy device enroll`](./enroll.md)
- [Wendy Lite enrollment](../../../../cli/lite-enrollment.md)
