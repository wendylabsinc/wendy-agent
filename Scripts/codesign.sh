#!/bin/bash

# This script codesigns the wendy-agent executable

set -e

# Get the executable path
EXECUTABLE_PATH=".build/debug/wendy"
CODESIGN_IDENTITY="Developer ID Application: Wendy Labs Inc. (3YVC792H3S)"

# Codesign the executable
codesign --force --options runtime --sign "$CODESIGN_IDENTITY" "$EXECUTABLE_PATH"

# Zip the executable
ditto -c -k --keepParent "$EXECUTABLE_PATH" "$EXECUTABLE_PATH.zip"

xcrun notarytool submit "$EXECUTABLE_PATH.zip" --keychain-profile notary-profile --wait

echo "Executable signed and notarized successfully."