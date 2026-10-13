# Platform capabilities

Native adapters will implement only capabilities the Go core cannot safely or portably own, such as hardware-backed keys, platform credential stores, BLE discovery, push notification registration, and lifecycle/background execution.

There is no platform SPI in the initial scalar-only Core release. A future capability requires a versioned narrow table and contract review; this scaffold does not define auth, signing, credential, or tunnel behavior.

Adapters are independent of the consumer language. `apple/common` holds family-level implementations; `apple/ios`, `apple/macos`, and `apple/watchos` hold variant differences. Android, Linux and Windows are independent families. Directory presence is not a support claim.
