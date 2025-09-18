#!/bin/bash

# This scripts generates the EdgeAgentGRPC Swift code. It requires `protoc` to be available in the PATH.
# It should be run from the root of the project.

# Use system-installed protoc plugins (from Homebrew) instead of building locally
PROTOC_GEN_GRPC_PATH=$(which protoc-gen-grpc-swift-2 || echo "/opt/homebrew/bin/protoc-gen-grpc-swift-2")
PROTOC_GEN_SWIFT_PATH=$(which protoc-gen-swift || echo "/opt/homebrew/bin/protoc-gen-swift")

# Check if system protoc plugins are available
if [ ! -f "$PROTOC_GEN_GRPC_PATH" ]; then
    echo "protoc-gen-grpc-swift not found. Please install with: brew install protoc-gen-grpc-swift"
    exit 1
fi

rm -rf Sources/EdgeAgentGRPC/Proto
mkdir -p Sources/EdgeAgentGRPC/Proto

rm -rf Sources/ContainerdGRPC/Proto
mkdir -p Sources/ContainerdGRPC/Proto
rm -rf Sources/ContainerdGRPCTypes/Proto
mkdir -p Sources/ContainerdGRPCTypes/Proto

protoc \
    --plugin=protoc-gen-grpc-swift=$PROTOC_GEN_GRPC_PATH \
    --grpc-swift_out=Sources/EdgeAgentGRPC/Proto \
    --grpc-swift_opt=Visibility=Public \
    --grpc-swift_opt=Server=True \
    --grpc-swift_opt=Client=True \
    --include_imports \
    --descriptor_set_out=Sources/EdgeAgentGRPC/Proto/edge_agent.protoset \
    --experimental_allow_proto3_optional \
    -I=Proto \
    Proto/edge/agent/services/v1/*.proto

# Containerd Services
protoc \
    --plugin=protoc-gen-grpc-swift=$PROTOC_GEN_GRPC_PATH \
    --grpc-swift_out=Sources/ContainerdGRPC/Proto \
    --grpc-swift_opt=Visibility=Public \
    --grpc-swift_opt=Client=True \
    --include_imports \
    --descriptor_set_out=Sources/ContainerdGRPC/Proto/containerd.protoset \
    --experimental_allow_proto3_optional \
    -I=Proto \
    -I=/opt/homebrew/include \
    $(find Proto/github.com/containerd/containerd/api/services -iname "*.proto")

# Check if protoc-gen-swift exists
if [ ! -f "$PROTOC_GEN_SWIFT_PATH" ]; then
    echo "protoc-gen-swift not found. Please install with: brew install swift-protobuf"
    exit 1
fi

protoc \
    --plugin=protoc-gen-swift=$PROTOC_GEN_SWIFT_PATH \
    --swift_out=Sources/EdgeAgentGRPC/Proto \
    --swift_opt=Visibility=Public \
    --experimental_allow_proto3_optional \
    -I=Proto \
    Proto/edge/agent/services/v1/*.proto

# Containerd Services
protoc \
    --plugin=protoc-gen-swift=$PROTOC_GEN_SWIFT_PATH \
    --swift_out=Sources/ContainerdGRPC/Proto \
    --swift_opt=Visibility=Public \
    --experimental_allow_proto3_optional \
    -I=Proto \
    -I=/opt/homebrew/include \
    $(find Proto/github.com/containerd/containerd/api/services -iname "*.proto")

# Containerd Types
protoc \
    --plugin=protoc-gen-swift=$PROTOC_GEN_SWIFT_PATH \
    --swift_out=Sources/ContainerdGRPCTypes/Proto \
    --swift_opt=Visibility=Public \
    --experimental_allow_proto3_optional \
    -I=Proto \
    -I=/opt/homebrew/include \
    $(find Proto/github.com/containerd/containerd/api/types -iname "*.proto")

# Google Types (for Containerd)
protoc \
    --plugin=protoc-gen-swift=$PROTOC_GEN_SWIFT_PATH \
    --swift_out=Sources/ContainerdGRPCTypes/Proto \
    --swift_opt=Visibility=Public \
    --experimental_allow_proto3_optional \
    -I=Proto \
    -I=/opt/homebrew/include \
    $(find Proto/google -iname "*.proto")

# Workarounds for duplicate file names in protobuf
mv Sources/ContainerdGRPC/Proto/github.com/containerd/containerd/api/services/ttrpc/events/v1/events.grpc.swift Sources/ContainerdGRPC/Proto/github.com/containerd/containerd/api/services/ttrpc/events/v1/ttrpc_events.grpc.swift
mv Sources/ContainerdGRPC/Proto/github.com/containerd/containerd/api/services/ttrpc/events/v1/events.pb.swift Sources/ContainerdGRPC/Proto/github.com/containerd/containerd/api/services/ttrpc/events/v1/ttrpc_events.pb.swift