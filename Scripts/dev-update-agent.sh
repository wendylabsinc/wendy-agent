#!/bin/bash

swiftly run swift build --product edge-agent --swift-sdk aarch64-swift-linux-musl && .build/arm64-apple-macosx/debug/edge agent update --binary .build/aarch64-swift-linux-musl/debug/edge-agent --agent ubuntu.orb.local
