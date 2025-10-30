#!/bin/bash
set -euo pipefail

# Script to build Homebrew bottle and update formula
# Usage: ./Scripts/update-homebrew-bottle.sh VERSION TAP_PATH FORMULA_PATH [GH_TOKEN]
#
# Arguments:
#   VERSION      - Version to build (e.g., 2025.10.24-142919)
#   TAP_PATH     - Path to the homebrew-tap repository
#   FORMULA_PATH - Path to the formula file (e.g., homebrew-tap/Formula/wendy.rb)
#   GH_TOKEN     - GitHub token for uploading bottle (optional, uses gh auth if not provided)

VERSION="${1:?VERSION required}"
TAP_PATH="${2:?TAP_PATH required}"
FORMULA_PATH="${3:?FORMULA_PATH required}"
GH_TOKEN="${4:-}"

ROOT_URL="https://github.com/wendylabsinc/wendy-agent/releases/download"
TAP_NAME="wendylabsinc/tap"
FORMULA_NAME="wendy"

echo "==> Building bottle and updating formula for version $VERSION"

# Step 1: Ensure the tap is available
echo "==> Checking tap setup..."
if ! brew tap | grep -q "$TAP_NAME"; then
    echo "==> Tapping $TAP_NAME from $TAP_PATH"
    brew tap "$TAP_NAME" "$TAP_PATH"
else
    echo "==> Tap $TAP_NAME already available"
fi

# Step 2: Calculate checksums for source and Linux binaries
echo "==> Calculating checksums..."

# Source tarball SHA
curl -sL "${ROOT_URL}/${VERSION}/wendy-cli-macos-arm64-${VERSION}.tar.gz" -o source.tar.gz 2>/dev/null || \
    curl -sL "https://github.com/wendylabsinc/wendy-agent/archive/refs/tags/${VERSION}.tar.gz" -o source.tar.gz
SOURCE_SHA=$(shasum -a 256 source.tar.gz | awk '{print $1}')
echo "  Source SHA: $SOURCE_SHA"

# Linux ARM SHA
curl -sL "${ROOT_URL}/${VERSION}/wendy-cli-linux-static-musl-aarch64-${VERSION}.tar.gz" -o linux-arm.tar.gz
LINUX_ARM_SHA=$(shasum -a 256 linux-arm.tar.gz | awk '{print $1}')
echo "  Linux ARM SHA: $LINUX_ARM_SHA"

# Linux x86 SHA
curl -sL "${ROOT_URL}/${VERSION}/wendy-cli-linux-static-musl-x86_64-${VERSION}.tar.gz" -o linux-x86.tar.gz
LINUX_X86_SHA=$(shasum -a 256 linux-x86.tar.gz | awk '{print $1}')
echo "  Linux x86 SHA: $LINUX_X86_SHA"

# Clean up downloaded files
rm -f source.tar.gz linux-arm.tar.gz linux-x86.tar.gz

# Step 3: Uninstall existing version if present
if brew list --formula | grep -q "^${FORMULA_NAME}$"; then
    echo "==> Uninstalling existing $FORMULA_NAME"
    brew uninstall "$FORMULA_NAME" 2>/dev/null || true
fi

# Step 4: Build bottle
echo "==> Building bottle..."
brew install --build-bottle "$TAP_NAME/$FORMULA_NAME"

# Step 5: Create bottle
echo "==> Creating bottle tarball and JSON..."
brew bottle --json --root-url="${ROOT_URL}/${VERSION}" "$TAP_NAME/$FORMULA_NAME"

# Step 6: Extract bottle SHA from JSON
BOTTLE_SHA=$(jq -r ".\"$TAP_NAME/$FORMULA_NAME\".bottle.tags.arm64_tahoe.sha256" wendy--*.bottle.json)
echo "==> Bottle SHA: $BOTTLE_SHA"

# Step 7: Rename bottle file to single dash (for GitHub release)
echo "==> Renaming bottle file..."
mv wendy--${VERSION}.arm64_tahoe.bottle.*.tar.gz wendy-${VERSION}.arm64_tahoe.bottle.1.tar.gz

# Step 8: Upload bottle to GitHub release
echo "==> Uploading bottle to GitHub release..."
if [ -n "$GH_TOKEN" ]; then
    export GH_TOKEN
fi

gh release upload "${VERSION}" \
    wendy-${VERSION}.arm64_tahoe.bottle.1.tar.gz \
    --repo wendylabsinc/wendy-agent \
    --clobber

echo "==> Bottle uploaded successfully"

# Step 9: Update formula with all checksums
echo "==> Updating formula at $FORMULA_PATH"

# Create a temporary file
TEMP_FILE=$(mktemp)

# Generate the updated formula
cat > "$TEMP_FILE" << 'EOF'
class Wendy < Formula
  desc "CLI for building and running WendyOS applications"
  homepage "https://github.com/wendylabsinc/wendy-agent"
  version "VERSION_PLACEHOLDER"

  bottle do
    root_url "https://github.com/wendylabsinc/wendy-agent/releases/download/VERSION_PLACEHOLDER"
    rebuild 1
    sha256 cellar: :any_skip_relocation, arm64_tahoe: "BOTTLE_SHA_PLACEHOLDER"
  end

  # Use source tarball for macOS (needs to build from source)
  on_macos do
    url "https://github.com/wendylabsinc/wendy-agent/archive/refs/tags/VERSION_PLACEHOLDER.tar.gz"
    sha256 "SOURCE_SHA_PLACEHOLDER"
  end

  # Use pre-built binaries for Linux
  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/wendylabsinc/wendy-agent/releases/download/VERSION_PLACEHOLDER/wendy-cli-linux-static-musl-aarch64.tar.gz"
      sha256 "LINUX_ARM_SHA_PLACEHOLDER"
    else
      url "https://github.com/wendylabsinc/wendy-agent/releases/download/VERSION_PLACEHOLDER/wendy-cli-linux-static-musl-x86_64.tar.gz"
      sha256 "LINUX_X86_SHA_PLACEHOLDER"
    end
  end

  depends_on xcode: [">= 16.3", :build] if OS.mac?
  depends_on "pv" if OS.mac?
  depends_on "swiftly" # For managing Swift toolchains (kept after install)

  uses_from_macos "swift" => :build

  def install
    if OS.mac?
      # macOS: Build from source
      system "./Scripts/inject-version.sh", version.to_s

      # Optionally use Swiftly if available and already configured
      # Skip in CI or sandboxed environments to avoid permission issues
      if File.exist?(".swift-version") && ENV["HOMEBREW_SANDBOX"].nil?
        swift_version = File.read(".swift-version").strip

        # Check if Swiftly is already initialized
        config_path = "#{Dir.home}/.swiftly/config.json"

        if which("swiftly") && File.exist?(config_path)
          ohai "Using Swiftly to install Swift #{swift_version}..."
          system "swiftly", "install", swift_version
          system "swiftly", "use", swift_version

          # Update PATH to use swiftly's Swift
          swiftly_bin = "#{Dir.home}/Library/Developer/Toolchains/swift-#{swift_version}.xctoolchain/usr/bin"
          ENV.prepend_path "PATH", swiftly_bin if File.directory?(swiftly_bin)
        end
      end

      system "swift", "build", "--disable-sandbox", "-c", "release", "--product", "wendy"
      bin.install ".build/release/wendy"

      # Install macOS-specific bundle with resources (plist files, etc)
      bundle_path = ".build/release/wendy-agent_wendy.bundle"
      (lib/"wendy").install bundle_path if File.directory?(bundle_path)
    else
      # Linux: Use pre-built binary
      bin.install "wendy"
    end
  end

  test do
    # TODO: It would be better to actually build something, instead of just checking the help text.
    system bin/"wendy", "--help"
    assert_match "OVERVIEW: Wendy CLI", shell_output("#{bin}/wendy --help")
  end
end
EOF

# Replace placeholders
sed -i.bak \
  -e "s/VERSION_PLACEHOLDER/$VERSION/g" \
  -e "s/SOURCE_SHA_PLACEHOLDER/$SOURCE_SHA/g" \
  -e "s/LINUX_ARM_SHA_PLACEHOLDER/$LINUX_ARM_SHA/g" \
  -e "s/LINUX_X86_SHA_PLACEHOLDER/$LINUX_X86_SHA/g" \
  -e "s/BOTTLE_SHA_PLACEHOLDER/$BOTTLE_SHA/g" \
  "$TEMP_FILE"

# Move to destination
mv "$TEMP_FILE" "$FORMULA_PATH"
rm -f "${TEMP_FILE}.bak"

echo "==> Formula updated successfully at $FORMULA_PATH"
echo ""
echo "==> Summary:"
echo "  Version: $VERSION"
echo "  Source SHA: $SOURCE_SHA"
echo "  Linux ARM SHA: $LINUX_ARM_SHA"
echo "  Linux x86 SHA: $LINUX_X86_SHA"
echo "  Bottle SHA: $BOTTLE_SHA"
echo ""
echo "==> Next steps:"
echo "  1. Review the updated formula at $FORMULA_PATH"
echo "  2. Commit and push the changes to the homebrew-tap repository"
echo "  3. Test installation: brew reinstall $TAP_NAME/$FORMULA_NAME"

