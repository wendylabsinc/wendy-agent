# Platform capabilities

Native adapters will implement only capabilities the Go core cannot safely or portably own, such as hardware-backed keys, platform credential stores, BLE discovery, push notification registration, and lifecycle/background execution.

The initial `WendyPlatform` table is intentionally empty. Adding a capability requires a versioned narrow table and contract review; this scaffold does not define auth, signing, credential, or tunnel behavior.
