# Wendy Agent & Wendy CLI

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

We have a [Homebrew Tap](https://github.com/wendyengineer/homebrew-tap) to install the developer CLI on macOS.

```sh
brew tap wendyengineer/tap
brew install wendy
```

To update the CLI on macOS:

```sh
brew upgrade wendy
```

## Setting Up the Device

The device needs to run the `wendy-agent` utility. We provide pre-build [Wendy](https://wendyos.io) images for the Raspberry Pi and the NVIDIA Jetson Orin Nano. These are preconfigured for remote debugging and have the wendy-agent preinstalled.

#### Manual Setup

The `wendy` CLI communicates with a `wendy-agent`. The agent needs uses Docker for running your apps, so Docker needs to be running.
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

Then, you can download and run your `wendy-agent` on the device. We provide nightly tags with the latest `wendy-agent` builds [in this repository](https://github.com/wendyengineer/wendy-agent/tags).

If you're planning to test the wendy-agent on macOS, you'll need to build and run the agent yourself from this repository.

```sh
swift run wendy-agent
```

## Examples

### Hello, world!

You can run the hello world example by executing the following command:

```sh
cd Examples/HelloWorld
swift run --package-path ../../ -- wendy run --device <hostname-of-device>
```

This will build the Wendy CLI and execute it's `run` command. The Wendy CLI will in turn build the
`HelloWorld` example using the Swift Static Linux SDK, and run it in a Docker container.

### Hello HTTP

A more advanced example demonstrating HTTP server capabilities is available in the `HelloHTTP` directory:

```sh
cd Examples/HelloHTTP
swift run --package-path ../../ -- wendy run --device <hostname-of-device>
```

### Debugging

To debug examples, you can use the following command:

```sh
swift run --package-path ../../ -- wendy run --device <hostname-of-device> --debug
```

You can now attach the LLDB debugger through using port `4242`.

#### LLDB

To start an LLDB debugging session from the CLI, run `lldb` from the Terminal:

```sh
lldb
```

Then, from within LLDB's prompt run the following to connect to your app's debugging session:

```sh
(lldb) target create .wendy-build/debug/HelloWorld
(lldb) settings set target.sdk-path "<path-to-sdk.artifactbundle>/swift-6.2-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64"
(lldb) settings set target.swift-module-search-paths "<path-to-sdk.artifactbundle>/swift-6.2-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64/usr/lib/swift_static/linux-static"
(lldb) gdb-remote localhost:4242
```

Unfortunately, running expressions (e.g. `po`) doesn't work yet.
