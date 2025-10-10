import Foundation

@testable import wendy_agent

/// Mock file system entry
public struct MockFileSystemEntry: Sendable {
    let isDirectory: Bool
    let content: String?
    let children: [String: MockFileSystemEntry]

    public init(
        isDirectory: Bool = false,
        content: String? = nil,
        children: [String: MockFileSystemEntry] = [:]
    ) {
        self.isDirectory = isDirectory
        self.content = content
        self.children = children
    }

    public static func file(_ content: String = "") -> MockFileSystemEntry {
        return MockFileSystemEntry(isDirectory: false, content: content)
    }

    public static func directory(
        _ children: [String: MockFileSystemEntry] = [:]
    ) -> MockFileSystemEntry {
        return MockFileSystemEntry(isDirectory: true, children: children)
    }
}

/// Mock FileSystemProvider for testing hardware scenarios
public struct MockFileSystemProvider: FileSystemProvider {
    // Note: We implement the FileSystemProvider methods manually below
    private let fileSystem: [String: MockFileSystemEntry]

    public enum HardwareScenario: CaseIterable {
        case raspberryPiZero2W
        case jetsonOrinNano
        case genericDebian
        case yoctoEmbedded
        case mixed
        case empty
    }

    public init(scenario: HardwareScenario = .mixed) {
        self.fileSystem = Self.createFileSystemForScenario(scenario)
    }

    public func fileExists(atPath path: String) -> Bool {
        return getEntry(at: path) != nil
    }

    public func contentsOfDirectory(atPath path: String) throws -> [String] {
        guard let entry = getEntry(at: path), entry.isDirectory else {
            throw NSError(
                domain: NSCocoaErrorDomain,
                code: NSFileReadNoSuchFileError,
                userInfo: nil
            )
        }
        return Array(entry.children.keys).sorted()
    }

    public func readFile(atPath path: String) throws -> String? {
        guard let entry = getEntry(at: path), !entry.isDirectory else {
            return nil
        }
        return entry.content
    }

    private func getEntry(at path: String) -> MockFileSystemEntry? {
        let components = path.components(separatedBy: "/").filter { !$0.isEmpty }
        var current = fileSystem

        for component in components {
            guard let entry = current[component] else {
                return nil
            }
            if entry.isDirectory {
                current = entry.children
            } else {
                return components.last == component ? entry : nil
            }
        }

        return MockFileSystemEntry.directory(current)
    }

    private static func createFileSystemForScenario(
        _ scenario: HardwareScenario
    ) -> [String: MockFileSystemEntry] {
        switch scenario {
        case .raspberryPiZero2W:
            return createRaspberryPiFileSystem()
        case .jetsonOrinNano:
            return createJetsonFileSystem()
        case .genericDebian:
            return createGenericDebianFileSystem()
        case .yoctoEmbedded:
            return createYoctoFileSystem()
        case .mixed:
            return createMixedFileSystem()
        case .empty:
            return createEmptyFileSystem()
        }
    }

    // MARK: - Raspberry Pi Zero 2W Scenario
    private static func createRaspberryPiFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([
                // GPIO chips
                "gpiochip0": .file(""),
                "gpiochip1": .file(""),

                // I2C buses
                "i2c-1": .file(""),

                // SPI devices
                "spidev0.0": .file(""),
                "spidev0.1": .file(""),

                // Video/camera devices
                "video0": .file(""),

                // Storage devices (SD card)
                "mmcblk0": .file(""),

                // Audio devices directory
                "snd": .directory([
                    "controlC0": .file(""),
                    "pcmC0D0p": .file(""),
                    "pcmC0D0c": .file(""),
                ]),

                // Standard devices
                "null": .file(""),
                "zero": .file(""),
                "urandom": .file(""),
            ]),
            "sys": .directory([
                "class": .directory([
                    "gpio": .directory([
                        "gpiochip0": .directory([
                            "label": .file("pinctrl-bcm2835"),
                            "ngpio": .file("54"),
                        ])
                    ]),
                    "i2c-adapter": .directory([
                        "i2c-1": .directory([
                            "name": .file("bcm2835 (i2c@7e804000)")
                        ])
                    ]),
                    "video4linux": .directory([
                        "video0": .directory([
                            "name": .file("bcm2835-camera")
                        ])
                    ]),
                ]),
                "block": .directory([
                    "mmcblk0": .directory([
                        "size": .file("62914560")  // 32GB SD card in 512-byte blocks
                    ])
                ]),
            ]),
        ]
    }

    // MARK: - Jetson Orin Nano Scenario
    private static func createJetsonFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([
                // NVIDIA GPU devices
                "nvidia0": .file(""),
                "nvidiactl": .file(""),
                "nvidia-uvm": .file(""),

                // Multiple GPIO chips
                "gpiochip0": .file(""),
                "gpiochip1": .file(""),
                "gpiochip2": .file(""),

                // Multiple I2C buses
                "i2c-0": .file(""),
                "i2c-1": .file(""),
                "i2c-7": .file(""),
                "i2c-8": .file(""),

                // SPI devices
                "spidev0.0": .file(""),
                "spidev1.0": .file(""),

                // Video devices
                "video0": .file(""),
                "video1": .file(""),

                // DRM devices
                "dri": .directory([
                    "card0": .file(""),
                    "renderD128": .file(""),
                ]),

                // USB bus structure
                "bus": .directory([
                    "usb": .directory([
                        "001": .directory([
                            "001": .file("")
                        ])
                    ])
                ]),

                // Audio devices
                "snd": .directory([
                    "controlC0": .file(""),
                    "controlC1": .file(""),
                ]),
            ]),
            "sys": .directory([
                "class": .directory([
                    "gpio": .directory([
                        "gpiochip0": .directory([
                            "label": .file("tegra234-gpio"),
                            "ngpio": .file("164"),
                        ]),
                        "gpiochip1": .directory([
                            "label": .file("tegra234-gpio-aon"),
                            "ngpio": .file("32"),
                        ]),
                    ]),
                    "drm": .directory([
                        "card0": .directory([
                            "device": .directory([
                                "vendor": .file("0x10de"),
                                "device": .file("0x2204"),
                            ])
                        ])
                    ]),
                ])
            ]),
        ]
    }

    // MARK: - Generic Debian Scenario
    private static func createGenericDebianFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([
                // USB bus structure
                "bus": .directory([
                    "usb": .directory([
                        "001": .directory([
                            "001": .file(""),
                            "002": .file(""),
                        ]),
                        "002": .directory([
                            "001": .file("")
                        ]),
                    ])
                ]),

                // Audio devices
                "snd": .directory([
                    "controlC0": .file(""),
                    "pcmC0D0p": .file(""),
                    "pcmC0D0c": .file(""),
                    "seq": .file(""),
                ]),

                // Network interfaces (as device files for this simulation)
                "eth0": .file(""),
                "wlan0": .file(""),

                // Input devices
                "input": .directory([
                    "mouse0": .file(""),
                    "event0": .file(""),
                    "event1": .file(""),
                ]),

                // Storage devices
                "sda": .file(""),
                "sda1": .file(""),
                "sdb": .file(""),

                // Serial devices
                "ttyUSB0": .file(""),
                "ttyS0": .file(""),
            ]),
            "sys": .directory([
                "class": .directory([
                    "net": .directory([
                        "eth0": .directory([
                            "address": .file("00:11:22:33:44:55"),
                            "type": .file("1"),
                        ]),
                        "wlan1": .directory([
                            "address": .file("bb:cc:dd:ee:ff:00"),
                            "type": .file("778"),
                        ]),
                        "ppp0": .directory([
                            "address": .file("cc:dd:ee:ff:00:11"),
                            "type": .file("512"),
                        ]),
                    ]),
                    "block": .directory([
                        "sda": .directory([
                            "size": .file("1953525168")
                        ])
                    ]),
                ])
            ]),
        ]
    }

    // MARK: - Yocto Embedded Scenario
    private static func createYoctoFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([
                // GPIO
                "gpiochip0": .file(""),

                // I2C buses
                "i2c-0": .file(""),
                "i2c-1": .file(""),

                // SPI devices
                "spidev0.0": .file(""),

                // Serial devices
                "ttyS0": .file(""),
                "ttyS1": .file(""),

                // Storage
                "mmcblk0": .file(""),
                "mmcblk0p1": .file(""),

                // Network
                "eth0": .file(""),
            ]),
            "sys": .directory([
                "class": .directory([
                    "gpio": .directory([
                        "gpiochip0": .directory([
                            "label": .file("embedded-gpio"),
                            "ngpio": .file("32"),
                        ])
                    ])
                ])
            ]),
        ]
    }

    // MARK: - Mixed/Complete Scenario
    private static func createMixedFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([
                // NVIDIA GPU devices
                "nvidia0": .file(""),
                "nvidia1": .file(""),
                "nvidiactl": .file(""),
                "nvidia-uvm": .file(""),

                // DRM devices
                "dri": .directory([
                    "card0": .file(""),
                    "card1": .file(""),
                    "renderD128": .file(""),
                ]),

                // GPIO chips
                "gpiochip0": .file(""),
                "gpiochip1": .file(""),
                "gpiochip2": .file(""),

                // I2C buses
                "i2c-0": .file(""),
                "i2c-1": .file(""),
                "i2c-2": .file(""),
                "i2c-7": .file(""),
                "i2c-8": .file(""),

                // SPI devices
                "spidev0.0": .file(""),
                "spidev0.1": .file(""),
                "spidev1.0": .file(""),
                "spidev2.0": .file(""),

                // Video devices
                "video0": .file(""),
                "video1": .file(""),
                "video2": .file(""),

                // Audio devices
                "snd": .directory([
                    "controlC0": .file(""),
                    "controlC1": .file(""),
                    "controlC2": .file(""),
                    "pcmC0D0p": .file(""),
                    "pcmC0D0c": .file(""),
                    "pcmC1D0p": .file(""),
                    "seq": .file(""),
                    "timer": .file(""),
                ]),

                // Network interfaces
                "eth0": .file(""),
                "eth1": .file(""),
                "wlan0": .file(""),
                "can0": .file(""),

                // Storage devices
                "sda": .file(""),
                "sda1": .file(""),
                "sda2": .file(""),
                "sdb": .file(""),
                "mmcblk0": .file(""),
                "mmcblk0p1": .file(""),
                "nvme0n1": .file(""),

                // USB bus structure
                "bus": .directory([
                    "usb": .directory([
                        "001": .directory([
                            "001": .file(""),
                            "002": .file(""),
                            "003": .file(""),
                        ]),
                        "002": .directory([
                            "001": .file(""),
                            "002": .file(""),
                        ]),
                    ])
                ]),

                // Serial devices
                "ttyUSB0": .file(""),
                "ttyUSB1": .file(""),
                "ttyS0": .file(""),
                "ttyS1": .file(""),
                "ttyACM0": .file(""),

                // Input devices
                "input": .directory([
                    "mouse0": .file(""),
                    "mouse1": .file(""),
                    "event0": .file(""),
                    "event1": .file(""),
                    "event2": .file(""),
                    "js0": .file(""),
                ]),
            ]),
            "sys": .directory([
                "class": .directory([
                    "gpio": .directory([
                        "gpiochip0": .directory([
                            "label": .file("tegra234-gpio"),
                            "ngpio": .file("164"),
                        ]),
                        "gpiochip1": .directory([
                            "label": .file("tegra234-gpio-aon"),
                            "ngpio": .file("32"),
                        ]),
                    ]),
                    "drm": .directory([
                        "card0": .directory([
                            "device": .directory([
                                "vendor": .file("0x10de"),
                                "device": .file("0x2560"),
                            ])
                        ]),
                        "card1": .directory([
                            "device": .directory([
                                "vendor": .file("0x1002"),
                                "device": .file("0x1638"),
                            ])
                        ]),
                    ]),
                    "i2c-adapter": .directory([
                        "i2c-0": .directory([
                            "name": .file("SMBus I801 adapter")
                        ]),
                        "i2c-1": .directory([
                            "name": .file("bcm2835 (i2c@7e804000)")
                        ]),
                    ]),
                    "video4linux": .directory([
                        "video0": .directory([
                            "name": .file("UVC Camera")
                        ]),
                        "video1": .directory([
                            "name": .file("bcm2835-camera")
                        ]),
                    ]),
                    "net": .directory([
                        "eth0": .directory([
                            "address": .file("00:11:22:33:44:55"),
                            "type": .file("1"),
                        ]),
                        "wlan0": .directory([
                            "address": .file("aa:bb:cc:dd:ee:ff"),
                            "type": .file("772"),
                        ]),
                    ]),
                    "block": .directory([
                        "sda": .directory([
                            "size": .file("1953525168")
                        ]),
                        "nvme0n1": .directory([
                            "size": .file("1000204886016")
                        ]),
                    ]),
                ])
            ]),
        ]
    }

    // MARK: - Empty Scenario
    private static func createEmptyFileSystem() -> [String: MockFileSystemEntry] {
        return [
            "dev": .directory([:]),
            "sys": .directory([
                "class": .directory([:])
            ]),
        ]
    }
}
