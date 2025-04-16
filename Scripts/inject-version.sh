#!/bin/bash

# This script injects the version information into the edge binary
# It's intended to be called from the GitHub Actions workflow or during other release processes

set -e

if [ $# -ne 2 ]; then
    echo "Usage: $0 <source-file> <version>"
    exit 1
fi

SOURCE_FILE=$1
VERSION=$2

# Remove the 'v' prefix if it exists
VERSION_WITHOUT_PREFIX=${VERSION#v}

echo "Injecting version $VERSION_WITHOUT_PREFIX into $SOURCE_FILE"

if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS requires empty string for in-place edit without backup
    sed -i '' -E "s/static let current = \"[^\"]*\"/static let current = \"$VERSION_WITHOUT_PREFIX\"/" "$SOURCE_FILE"
else
    # Linux and other Unix systems
    sed -i -E "s/static let current = \"[^\"]*\"/static let current = \"$VERSION_WITHOUT_PREFIX\"/" "$SOURCE_FILE"
fi

echo "Version injection complete"
