# Edge Agent & Edge CLI

## Requirements

### Linux

For the cli to work properly on linux, `usbutils` needs to be installed.

### Swift Toolchain

The CLI currently assumes that the Swift toolchain is installed at `/Library/Developer/Toolchains/swift-6.0.3-RELEASE.xctoolchain`. You can obtain a copy of this toolchain [here](https://download.swift.org/swift-6.0.3-release/xcode/swift-6.0.3-RELEASE/swift-6.0.3-RELEASE-osx.pkg). During the installation of the toolchain pkg, you need to select "Install for all users of this computer".

Before installing the SDK in the next step, export the`TOOLCHAINS` environment variable:

```sh
export TOOLCHAINS=$(plutil -extract CFBundleIdentifier raw /Library/Developer/Toolchains/swift-6.0.3-RELEASE.xctoolchain/Info.plist)
```

### Static Linux SDK

After installing the toolchain and exporting the `TOOLCHAINS` variable, you need to install the Swift Static Linux SDK. This step is necessary on all platforms (including macOS).

```sh
swift sdk install https://download.swift.org/swift-6.0.3-release/static-sdk/swift-6.0.3-RELEASE/swift-6.0.3-RELEASE_static-linux-0.0.1.artifactbundle.tar.gz --checksum 67f765e0030e661a7450f7e4877cfe008db4f57f177d5a08a6e26fd661cdd0bd
```

### Installing the CLI

We have a [Homebrew Tap](https://github.com/apache-edge/homebrew-tap) to install the developer CLI on macOS.

```sh
brew tap apache-edge/tap
brew install edge
```

To update the CLI on macOS:

```sh
brew upgrade edge
```

## Setting Up the Device

The device needs to run the `edge-agent` utility. We provide pre-build [EdgeOS](https://edgeos.io) images for the Raspberry Pi and the NVIDIA Jetson Orin Nano. These are preconfigured for remote debugging and have the edge-agent preinstalled.

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

Then, you can download and run your `edge-agent` on the device. We provide nightly tags with the latest `edge-agent` builds [in this repository](https://github.com/apache-edge/edge-agent/tags).

If you're planning to test the edge-agent on macOS, you'll need to build and run the agent yourself from this repository.

```sh
swift run edge-agent
```

## Hello, world!

You can then run the hello world example by executing the following command:

```sh
cd Examples/HelloWorld
swift run --package-path ../../ -- edge run --agent <hostname-of-device>
```

This will build the Edge CLI and execute it's `run` command. The Edge CLI will in turn build the
`HelloWorld` example using the Swift Static Linux SDK, and run it in a Docker container.

### Debugging

To debug the `HelloWorld` example, you can use the following command:

```sh
swift run --package-path ../../ -- edge run --agent <hostname-of-device> --debug
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
(lldb) settings set target.sdk-path "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64"
(lldb) settings set target.swift-module-search-paths "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64/usr/lib/swift_static/linux-static"
(lldb) gdb-remote localhost:4242
```

- **target create** refers to the binary you've created
- **settings set** selects the swift-sdk you built the binary with. This is optional, but necessary for debugging support.
- **gdb-remote**'s value `localhost:4242` refers to the host (and port) where the debug server is running. If you're starting a remote debugging session on another machine, replace this with your device's hostname or IP.

Unfortunately, running expressions (e.g. `po`) doesn't work yet.
