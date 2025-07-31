#!/bin/bash

# This script generates a version string from git information
# Format: <latest-tag>-<commit-count>-g<short-hash>-<timestamp>[-dirty]

set -e

# Get the latest git tag
LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")

# Get short commit hash
SHORT_HASH=$(git rev-parse --short HEAD)

# Get commit count since latest tag
COMMIT_COUNT=$(git rev-list --count ${LATEST_TAG}..HEAD 2>/dev/null || echo "0")

# Generate timestamp (UTC)
TIMESTAMP=$(date -u +"%Y%m%d%H%M%S")

# Check for dirty working directory
DIRTY=""
if [ -n "$(git status --porcelain)" ]; then
    DIRTY="-dirty"
fi

# Remove 'v' prefix from tag if it exists
TAG_WITHOUT_PREFIX=${LATEST_TAG#v}

# Construct version string
if [ "$COMMIT_COUNT" = "0" ]; then
    # On a tag, use just the tag with timestamp
    VERSION="${TAG_WITHOUT_PREFIX}-${TIMESTAMP}${DIRTY}"
else
    # Not on a tag, include commit count and hash
    VERSION="${TAG_WITHOUT_PREFIX}-${COMMIT_COUNT}-g${SHORT_HASH}-${TIMESTAMP}${DIRTY}"
fi

echo "$VERSION" 