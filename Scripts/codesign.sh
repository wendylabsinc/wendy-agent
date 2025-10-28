#!/bin/bash

# This script codesigns the wendy-agent executable

set -e

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 /path/to/binary" >&2
  exit 2
fi

# Get the executable path
EXECUTABLE_PATH="$1"
CODESIGN_IDENTITY="Developer ID Application: Wendy Labs Inc. (3YVC792H3S)"

# Codesign the executable
echo "Codesigning executable at $EXECUTABLE_PATH..."
codesign --force --options runtime --keychain notary-keychain --sign "$CODESIGN_IDENTITY" "$EXECUTABLE_PATH"

# Zip the executable
echo "Zipping executable..."
ditto -c -k --keepParent "$EXECUTABLE_PATH" "$EXECUTABLE_PATH.zip"

echo "Submitting executable for notarization..."
xcrun notarytool submit "$EXECUTABLE_PATH.zip" --keychain-profile notary-profile --wait

echo "Executable signed and notarized successfully."