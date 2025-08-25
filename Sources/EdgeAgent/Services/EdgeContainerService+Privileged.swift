import Foundation
import Logging

extension EdgeContainerService {
    /// Build OCI runtime spec with conditional privileged mode
    /// - Parameters:
    ///   - privileged: Whether to use privileged configuration
    ///   - appName: The application/container name
    ///   - command: The command to run in the container
    /// - Returns: OCI runtime specification as JSON data
    func buildOCISpec(privileged: Bool, appName: String, command: String) throws -> Data {
        // Build mounts array based on privileged mode
        var mounts: [[String: Any]] = [
            [
                "destination": "/proc",
                "type": "proc",
                "source": "proc",
            ],
            [
                "destination": "/dev/pts",
                "type": "devpts",
                "source": "devpts",
                "options": [
                    "nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620",
                ],
            ],
            [
                "destination": "/dev/shm",
                "type": "tmpfs",
                "source": "shm",
                "options": ["nosuid", "noexec", "nodev", "mode=1777", "size=65536k"],
            ],
            [
                "destination": "/dev/mqueue",
                "type": "mqueue",
                "source": "mqueue",
                "options": ["nosuid", "noexec", "nodev"],
            ],
        ]

        // Add privileged-specific mounts
        if privileged {
            // Replace tmpfs /dev with bind mount for hardware access
            mounts.insert(
                [
                    "destination": "/dev",
                    "type": "bind",
                    "source": "/dev",
                    "options": ["bind", "rw"],
                ],
                at: 1
            )

            // Mount /sys for hardware visibility (GPU, USB controllers, etc.)
            mounts.append([
                "destination": "/sys",
                "type": "sysfs",
                "source": "sysfs",
                "options": ["nosuid", "noexec", "nodev", "ro"],
            ])

            // Mount /sys/fs/cgroup for device cgroup access
            mounts.append([
                "destination": "/sys/fs/cgroup",
                "type": "cgroup",
                "source": "cgroup",
                "options": ["nosuid", "noexec", "nodev", "relatime", "ro"],
            ])
        } else {
            // Use tmpfs /dev for non-privileged containers
            mounts.insert(
                [
                    "destination": "/dev",
                    "type": "tmpfs",
                    "source": "tmpfs",
                    "options": ["nosuid", "strictatime", "mode=755", "size=65536k"],
                ],
                at: 1
            )
        }

        // Build Linux configuration based on privileged mode
        var linuxConfig: [String: Any] = [
            "namespaces": [
                ["type": "pid"],
                ["type": "ipc"],
                ["type": "uts"],
                ["type": "mount"],
            ],
            "networkMode": "host",
        ]

        // Build process configuration with capabilities
        var processConfig: [String: Any] = [
            "terminal": false,
            "user": ["uid": 0, "gid": 0],
            "args": command.split(separator: " ").map(String.init),
            "env": [
                "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
            ],
            "cwd": "/",
        ]

        if privileged {
            // Add capabilities to process section (OCI spec location for capabilities)
            // OCI spec requires CAP_ prefix for capability names
            processConfig["capabilities"] = [
                "bounding": [
                    "CAP_SYS_PTRACE",  // For debugging
                    "CAP_SYS_RAWIO",  // For raw I/O port access
                    "CAP_SYS_ADMIN",  // For device management
                    "CAP_MKNOD",  // For creating device nodes
                    "CAP_DAC_OVERRIDE",  // For accessing device files
                ],
                "effective": [
                    "CAP_SYS_PTRACE",
                    "CAP_SYS_RAWIO",
                    "CAP_SYS_ADMIN",
                    "CAP_MKNOD",
                    "CAP_DAC_OVERRIDE",
                ],
                "inheritable": [
                    "CAP_SYS_PTRACE",
                    "CAP_SYS_RAWIO",
                    "CAP_SYS_ADMIN",
                    "CAP_MKNOD",
                    "CAP_DAC_OVERRIDE",
                ],
                "permitted": [
                    "CAP_SYS_PTRACE",
                    "CAP_SYS_RAWIO",
                    "CAP_SYS_ADMIN",
                    "CAP_MKNOD",
                    "CAP_DAC_OVERRIDE",
                ],
            ]

            // Allow all syscalls in privileged mode
            linuxConfig["seccomp"] = [
                "defaultAction": "SCMP_ACT_ALLOW",
                "architectures": ["SCMP_ARCH_AARCH64"],
                "syscalls": [],
            ]

            // Allow access to all device types for hardware access
            linuxConfig["devices"] = [
                // Allow all character and block devices
                [
                    "allow": true,
                    "type": "c",  // Character devices
                    "access": "rwm",
                ],
                [
                    "allow": true,
                    "type": "b",  // Block devices
                    "access": "rwm",
                ],
            ]

            // Let containerd manage the cgroup path
            linuxConfig["resources"] = [
                "devices": [
                    [
                        "allow": true,
                        "type": "a",  // All types
                        "access": "rwm",
                    ]
                ]
            ]
        } else {
            // Minimal capabilities for non-privileged containers
            processConfig["capabilities"] = [
                "bounding": ["CAP_SYS_PTRACE"],
                "effective": ["CAP_SYS_PTRACE"],
                "inheritable": ["CAP_SYS_PTRACE"],
                "permitted": ["CAP_SYS_PTRACE"],
            ]
        }

        // Build the complete OCI spec
        let spec = try JSONSerialization.data(withJSONObject: [
            "ociVersion": "1.0.3",
            "process": processConfig,
            "root": [
                "path": "rootfs",
                "readonly": false,
            ],
            "hostname": appName,
            "mounts": mounts,
            "linux": linuxConfig,
        ])

        return spec
    }
}
