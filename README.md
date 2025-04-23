# Edge Agent & Edge CLI

## Requirements

### Swift Toolchain

The CLI currently assumes that the Swift toolchain is installed at `/Library/Developer/Toolchains/swift-6.0.3-RELEASE.xctoolchain`. You can obtain a copy of this toolchain [here](https://download.swift.org/swift-6.0.3-release/xcode/swift-6.0.3-RELEASE/swift-6.0.3-RELEASE-osx.pkg). During the installation of the toolchain pkg, you need to select "Install for all users of this computer".

Before installing the SDK in the next step, export the`TOOLCHAINS` environment variable:

```sh
export TOOLCHAINS=$(plutil -extract CFBundleIdentifier raw /Library/Developer/Toolchains/swift-6.0.3-RELEASE.xctoolchain/Info.plist)
```

### Static Linux SDK

After installing the toolchain and exporting the `TOOLCHAINS` variable, you need to install the Swift Static Linux SDK.

```sh
swift sdk install https://download.swift.org/swift-6.0.3-release/static-sdk/swift-6.0.3-RELEASE/swift-6.0.3-RELEASE_static-linux-0.0.1.artifactbundle.tar.gz --checksum 67f765e0030e661a7450f7e4877cfe008db4f57f177d5a08a6e26fd661cdd0bd
```

### Developing on macOS

We have a [Homebrew Tap](https://github.com/apache-edge/homebrew-tap) to install the developer CLI.

```sh
brew tap apache-edge/tap
brew install edge
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
swift run --package-path ../../ -- edge run --agent-host <hostname-of-device>
```

This will build the Edge CLI and execute it's `run` command. The Edge CLI will in turn build the
`HelloWorld` example using the Swift Static Linux SDK, and run it in a Docker container.

### Debugging

To debug the `HelloWorld` example, you can use the following command:

```sh
swift run --package-path ../../ -- edge run --agent-host <hostname-of-device> --debug
```

You can now attach the LLDB debugger using port `4242` like this:

```sh
lldb
(lldb) target create .build/debug/HelloWorld
(lldb) settings set target.sdk-path "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64"
(lldb) settings set target.swift-module-search-paths "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64/usr/lib/swift_static/linux-static"
(lldb) gdb-remote localhost:4242
```

Unfortunately, running expressions (e.g. `po`) doesn't work yet.
