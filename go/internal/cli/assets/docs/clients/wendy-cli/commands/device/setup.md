> **Note:** `wendy device setup` is an advanced command and is not listed in
> `wendy device --help`. It remains fully functional.

Interactive wizard that provisions the device, configures WiFi, and optionally updates the agent. Connects the device to [Wendy Cloud](../../../../cloud/) using the CLI's stored mTLS certificates.

A device obtains its own certificate from [pki-core](../../../../pki/) directly, over ACME (or EST on constrained hardware) — not from cloud. To enroll a device on its own, without the rest of the wizard, use [`wendy device enroll`](./enroll.md).