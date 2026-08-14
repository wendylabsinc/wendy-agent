# `--device` option

## Introduction

The Wendy CLI `--device` option is primarily used to connect to a target by hostname, which is probably how most of us use it today.
However, `--device` also supports device names, typically for `wendy run` and `wendy cloud ...` commands, as well as special identifiers such as `docker` or `wendy-lite:...`.
It is not yet clear how to use `--device` to connect to devices via USB or Bluetooth. In some cases the CLI needs to guess and run several attempts to determine whether `--device` is providing a hostname or a device name.

The goal of this document is to clarify the use of `--device` and propose changes that make it easier to use device names instead of hostnames, whether the device is reached via USB, BLE, LAN, or Cloud.

## Base concept

`--device` defines a device, and that device can be one of the following:

1. A predefined target known intrinsically by the CLI, such as `docker` or `apple-container`.
2. A target defined by a category (or provider) prefix, such as `wendy-lite:...`. These categories are also known intrinsically by the CLI, which never treats them as a hostname:port pattern.
3. A hostname or IP address, with an optional port number. Examples: `192.168.1.23:50052`, `wendy-macmini.local`, `[2001:db8::8]:50051`. **A hostname MUST always include a dot**. To connect to localhost, for instance (even though this rarely makes sense), you need to use `localhost.`. Without the trailing dot, `localhost` is considered a device name, not a hostname.
4. A device name, such as `my-jetson-board` or `go2-dog`. These names contain no dots, which distinguishes them from hostnames.

We could allow other syntaxes in the future, as long as they remain unambiguous with the rules described above. The category prefix is the simplest way to add new device identification methods.

## Allow device names everywhere

Given the rules defined above, using device names becomes the primary way of using the `--device` option. More precisely:

* You can reach a device by its name, whether it is on USB, BLE, LAN, or Cloud.
* All Wendy CLI commands become usable on cloud devices, without needing a `wendy cloud ...` command. `wendy cloud ...` still makes sense for commands that are cloud specific, such as `wendy cloud login`.

To achieve this, we need:

* A device resolution mechanism that searches for reachable devices through different paths or resolvers.
* Resolutions must run in parallel, so we do not wait for one path to fail before trying the next.
* All resolvers must identify a device consistently, so that the same device is recognized regardless of which path found it.
  * Each device must have an ID. Today we use the device name as an ID, but this is probably too weak.
  * When a device is enrolled, its ID must match the one used in certificates and in the cloud.
* When the same device can be reached through several paths, we need to select the correct one based on simple priority rules.
* Resolution results must be cached.
  * The cache allows prioritizing a connection path before resolving all of them.
  * Even when the cache allows us to quickly pick the right connection path, resolution must continue in the background and keep the cache up to date.
  * The caching algorithm has not been defined yet, but we need a way to confirm that the cached connection path is still valid and fall back to the next one if it is not.
