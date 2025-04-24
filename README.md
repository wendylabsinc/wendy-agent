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

### Docker

Currently, the `run` command targets a local Docker daemon instead of a remote EdgeOS device, so Docker needs to be running.

## Hello, world!

You can then run the hello world example by executing the following command:

```sh
cd Examples/HelloWorld
swift run --package-path ../../ -- edge run
```

This will build the Edge CLI and execute it's `run` command. The Edge CLI will in turn build the
`HelloWorld` example using the Swift Static Linux SDK, and run it in a Docker container.

### Debugging

To debug the `HelloWorld` example, you can use the following command:

```sh
swift run --package-path ../../ -- edge run --debug
```

You can now attach the LLDB debugger using port `4242` like this:

```sh
lldb
(lldb) target create .build/debug/HelloWorld
(lldb) settings set target.sdk-path "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64"
(lldb) settings set target.swift-module-search-paths "<path-to-sdk.artifactbundle>/swift-6.0.3-RELEASE_static-linux-0.0.1/swift-linux-musl/musl-1.2.5.sdk/aarch64/usr/lib/swift_static/linux-static"
(lldb) gdb-remote localhost:4242
```

### Imager

The Imager is a tool for writing EdgeOS images to USB drives. You can use it to write an EdgeOS image to a USB drive by running the following command:

```sh
swift run --package-path ../../ -- imager write <image-path> <drive-id>
```

### List

You can list available external drives by running the following command:

```sh
swift run --package-path ../../ -- imager list
```

Unfortunately, running expressions (e.g. `po`) doesn't work yet.
