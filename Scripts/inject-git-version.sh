#!/bin/bash

# This script generates and injects git-based version information
# Usage: ./inject-git-version.sh [--dev] [version]

set -e

SOURCE_FILE="Sources/WendyShared/Version.swift"

# Check if we're in development mode
if [ "$1" = "--dev" ]; then
    # Generate development version from git
    VERSION=$(./Scripts/generate-git-version.sh)
else
    # Use provided version or generate from git
    if [ $# -eq 1 ] && [ "$1" != "--dev" ]; then
        VERSION=$1
        # Remove the 'v' prefix if it exists
        VERSION=${VERSION#v}
    else
        VERSION=$(./Scripts/generate-git-version.sh)
    fi
fi

echo "Injecting version $VERSION into $SOURCE_FILE"

# Make the script executable if it isn't already
chmod +x Scripts/generate-git-version.sh

if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS requires empty string for in-place edit without backup
    sed -i '' -E "s/static let current = \"[^\"]*\"/static let current = \"$VERSION\"/" "$SOURCE_FILE"
else
    # Linux and other Unix systems
    sed -i -E "s/static let current = \"[^\"]*\"/static let current = \"$VERSION\"/" "$SOURCE_FILE"
fi

echo "Version updated to: $VERSION" 