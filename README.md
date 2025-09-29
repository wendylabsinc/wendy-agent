# Edge Agent & Edge CLI

## Build Requirements

- **Swift 6.2** or later (required)
- **Swift 6.2.1** or later (recommended for Span support)
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

**Note:** The project includes conditional compilation for Swift 6.2.1+ to enable Span support in the swift-subprocess package. When using Swift 6.2.1 or later, the `SubprocessSpan` trait will be automatically enabled for better subprocess management.

### Static Linux SDK

After installing the toolchain and exporting the `TOOLCHAINS` variable, you need to install the Swift Static Linux SDK. This step is necessary on all platforms (including macOS).

```sh
swift sdk install https://download.swift.org/swift-6.2-release/static-sdk/swift-6.2-RELEASE/swift-6.2-RELEASE_static-linux-0.0.1.artifactbundle.tar.gz
```

### Installing the CLI

We have a [Homebrew Tap](https://github.com/edgeengineer/homebrew-tap) to install the developer CLI on macOS.

```sh
brew tap edgeengineer/tap
brew install edge
```

To update the CLI on macOS:

```sh
brew upgrade edge
```

## Setting Up the Device

The device needs to run the `edge-agent` utility. We provide pre-build [EdgeOS](https://edgeos.io) images for the Raspberry Pi and the NVIDIA Jetson Orin Nano. These are preconfigured for remote debugging and have the edge-agent preinstalled.

### Network Manager Support

EdgeAgent supports both NetworkManager and ConnMan for WiFi configuration. The agent will automatically detect which network manager is available on the system:

- **ConnMan** is preferred for embedded/IoT devices due to its lighter resource usage
- **NetworkManager** is supported for desktop and server environments
- The agent will automatically detect and use the available network manager

#### Configuration

You can configure the network manager preference using the `EDGE_NETWORK_MANAGER` environment variable:

```sh
# Auto-detect (default)
export EDGE_NETWORK_MANAGER=auto

# Prefer ConnMan if available, fall back to NetworkManager
export EDGE_NETWORK_MANAGER=connman

# Prefer NetworkManager if available
export EDGE_NETWORK_MANAGER=networkmanager

# Force ConnMan (will fail if not available)
export EDGE_NETWORK_MANAGER=force-connman

# Force NetworkManager (will fail if not available)
export EDGE_NETWORK_MANAGER=force-networkmanager
```

If no environment variable is set, the agent will auto-detect the available network manager.

#### Manual Setup

The `edge` CLI communicates with an `edge-agent`. The agent needs uses Docker for running your apps, so Docker needs to be running.
On a Debian (or Ubuntu) based OS, you can do the following:

```sh
# Install Docker
sudo apt install docker.io
# Start Docker and keep running across reboots
sudo systemctl start docker
sudo systemctl enable docker
# Provide access to Docker from the current user
sudo usermod -aG docker $USER
```

Then, you can download and run your `edge-agent` on the device. We provide nightly tags with the latest `edge-agent` builds [in this repository](https://github.com/edgeengineer/edge-agent/tags).

If you're planning to test the edge-agent on macOS, you'll need to build and run the agent yourself from this repository.

```sh
swift run edge-agent
```

## Examples

### Hello, world!

You can run the hello world example by executing the following command:

```sh
cd Examples/HelloWorld
swift run --package-path ../../ -- edge run --device <hostname-of-device>
```

This will build the Edge CLI and execute it's `run` command. The Edge CLI will in turn build the
`HelloWorld` example using the Swift Static Linux SDK, and run it in a Docker container.

### Hello HTTP

A more advanced example demonstrating HTTP server capabilities is available in the `HelloHTTP` directory:

```sh
cd Examples/HelloHTTP
swift run --package-path ../../ -- edge run --device <hostname-of-device>
```

### Debugging

To debug examples, you can use the following command:

```sh
swift run --package-path ../../ -- edge run --device <hostname-of-device> --debug
```

You can now attach the LLDB debugger through using port `4242`.

#### LLDB

To start an LLDB debugging session from the CLI, run `lldb` from the Terminal:

```sh
lldb
```

Then, from within LLDB's prompt run the following to connect to your app's debugging session:

```sh
(lldb) target create .edge-build/debug/HelloWorld
(lldb) settings set target.sdk-path "<path-to-sdk.artifactbundle>/swift-6.2-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64"
(lldb) settings set target.swift-module-search-paths "<path-to-sdk.artifactbundle>/swift-6.2-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64/usr/lib/swift_static/linux-static"
(lldb) gdb-remote localhost:4242
```

Unfortunately, running expressions (e.g. `po`) doesn't work yet.
