# Wendy Agent & Wendy CLI

## Build Requirements

- **Swift 6.2.1** or later (required)
- **macOS 15** (Sequoia) or later for development
- **Xcode 16.2** or later (if using Xcode)

## Requirements

### Linux

For the cli to work properly on linux, `usbutils` needs to be installed.

### Swift Toolchain

The CLI requires Swift 6.2 or later. The toolchain should be installed at `/Library/Developer/Toolchains/swift-6.2-RELEASE.xctoolchain`. You can obtain a copy of this toolchain [here](https://download.swift.org/swift-6.2-release/xcode/swift-6.2-RELEASE/swift-6.2-RELEASE-osx.pkg). During the installation of the toolchain pkg, you need to select "Install for all users of this computer".

Before installing the SDK in the next step, export the`TOOLCHAINS` environment variable:

```sh
export TOOLCHAINS=$(plutil -extract CFBundleIdentifier raw /Library/Developer/Toolchains/swift-6.2-RELEASE.xctoolchain/Info.plist)
```

### Installing the CLI

We have a [Homebrew](https://brew.sh) tap to install the developer CLI on macOS.

```sh
brew install wendylabsinc/tap/wendy
```

To update the CLI:

```sh
brew upgrade wendy
```

## Setting Up the Device

First, you need to setup a disk. NVME, USB, or an SD card will work.

```sh
# Spawns an interactive prompt to setup a disk and device.
wendy disk setup
```

Once the device is booting off your disk, you can start deploying code it.
You can run `Dockerfile` based apps or SwiftPM based apps.

```sh
wendy run
```

## Examples

We have various examples in the `Examples` directory to help you get started.