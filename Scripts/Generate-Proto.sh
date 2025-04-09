#!/bin/bash

# This scripts generates the EdgeAgentGRPC Swift code. It requires `protoc` to be available in the PATH.
# It should be run from the root of the project.

# Get the binary path
BIN_PATH=$(swift build --show-bin-path)
PROTOC_GEN_GRPC_PATH="$BIN_PATH/protoc-gen-grpc-swift"
PROTOC_GEN_SWIFT_PATH="$BIN_PATH/protoc-gen-swift"

# Check if protoc-gen-grpc-swift exists, and build it if needed
if [ ! -f "$PROTOC_GEN_GRPC_PATH" ]; then
    echo "protoc-gen-grpc-swift not found. Building it now..."
    swift build --product protoc-gen-grpc-swift
fi

rm -rf Sources/EdgeAgentGRPC/Proto
mkdir -p Sources/EdgeAgentGRPC/Proto

protoc \
    --plugin $PROTOC_GEN_GRPC_PATH \
    --grpc-swift_out=Sources/EdgeAgentGRPC/Proto \
    --grpc-swift_opt=Visibility=Public \
    --grpc-swift_opt=Server=True \
    --grpc-swift_opt=Client=True \
    --include_imports \
    --descriptor_set_out=Sources/EdgeAgentGRPC/Proto/edge_agent.protoset \
    --experimental_allow_proto3_optional \
    -I=Proto \
    Proto/edge/agent/services/v1/*.proto

# Check if protoc-gen-swift exists
if [ ! -f "$PROTOC_GEN_SWIFT_PATH" ]; then
    echo "protoc-gen-swift not found. Building it now..."
    swift build --product protoc-gen-swift
fi

protoc \
    --plugin $PROTOC_GEN_SWIFT_PATH \
    --swift_out=Sources/EdgeAgentGRPC/Proto \
    --swift_opt=Visibility=Public \
    --experimental_allow_proto3_optional \
    -I=Proto \
    Proto/edge/agent/services/v1/*.proto
