import AppConfig
import Foundation

extension OCI {
    mutating func applyEntitlements(
        entitlements: [Entitlement],
        appName: String
    ) {
        for entitlement in entitlements {
            switch entitlement {
            case .network(let entitlement):
                switch entitlement.mode {
                case .host:
                    self.linux.networkMode = "host"
                case .none:
                    self.linux.networkMode = "none"
                    self.linux.namespaces.append(.init(type: "network"))
                }
            case .video:
                self.linux.devices.append(
                    .init(
                        path: "/dev/video0",
                        type: "c",
                        major: 81,
                        minor: 17,
                        fileMode: 0o666,
                        uid: 0,
                        gid: 0
                    )
                )

                let deviceCapabilities = [
                    "CAP_CHOWN",
                    "CAP_DAC_OVERRIDE",
                    "CAP_FSETID",
                    "CAP_FOWNER",
                    "CAP_MKNOD",
                    "CAP_NET_RAW",
                    "CAP_SETGID",
                    "CAP_SETUID",
                    "CAP_SETFCAP",
                    "CAP_SETPCAP",
                    "CAP_NET_BIND_SERVICE",
                    "CAP_SYS_CHROOT",
                    "CAP_KILL",
                    "CAP_AUDIT_WRITE",
                    "CAP_SYS_PTRACE",
                ]
                self.linux.capabilities.bounding.formUnion(deviceCapabilities)
                self.linux.capabilities.effective.formUnion(deviceCapabilities)
                self.linux.capabilities.inheritable.formUnion(deviceCapabilities)
                self.linux.capabilities.permitted.formUnion(deviceCapabilities)

                self.mounts.append(
                    .init(
                        destination: "/sys/fs/cgroup",
                        type: "cgroup",
                        source: "cgroup",
                        options: ["ro", "nosuid", "noexec", "nodev"]
                    )
                )

                self.mounts.append(
                    .init(
                        destination: "/dev/video0",
                        type: "bind",
                        source: "/dev/video0",
                        options: ["rbind", "nosuid", "noexec"]
                    )
                )

                if self.linux.resources == nil {
                    self.linux.resources = Resources()
                }

                if self.linux.resources?.devices == nil {
                    self.linux.resources?.devices = []
                }

                // Configure cgroup path and mode for device controller delegation
                let path = appName.replacingOccurrences(of: "-", with: "_")
                self.linux.cgroupsPath = "system.slice:edge-agent-running:\(path)"
                self.linux.namespaces.append(.init(type: "cgroup"))

                // Apply resources to container, these are applies in order
                do {
                    // Default deny all devices
                    self.linux.resources?.devices?.append(
                        DeviceAllowance(allow: false, access: "rwm")  // Default deny all
                    )

                    // Add device allowance for video device
                    self.linux.resources?.devices?.append(
                        DeviceAllowance(allow: true, type: "c", major: 81, minor: 17, access: "rwm")
                    )
                }
            }
        }
    }
}
