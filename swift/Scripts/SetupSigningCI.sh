#!/bin/bash
set -euo pipefail

required=(
  DEVELOPER_ID_CERTIFICATE
  DEVELOPER_ID_KEY
  NOTARY_APPLE_ID
  NOTARY_TEAM_ID
  NOTARY_PASSWORD
)

for var in "${required[@]}"; do
  if [ -z "${!var:-}" ]; then
    echo "::error::Missing required macOS signing secret: ${var}"
    exit 1
  fi
done

TEMP_DIR="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
KEYCHAIN_PATH="${TEMP_DIR}/wendy-signing.keychain-db"
KEYCHAIN_PASSWORD="$(openssl rand -hex 24)"
CERT_PATH="${TEMP_DIR}/developer-id-application.cer"
KEY_PATH="${TEMP_DIR}/developer-id-application.key"
DEVELOPER_ID_CA_PATH="${TEMP_DIR}/developer-id-g2-ca.cer"
DEVELOPER_ID_CA_URL="https://www.apple.com/certificateauthority/DeveloperIDG2CA.cer"
DEVELOPER_ID_CA_SHA256="f16cd3c54c7f83cea4bf1a3e6a0819c8aaa8e4a1528fd144715f350643d2df3a"
NOTARY_PROFILE="${NOTARY_PROFILE:-wendy-notary-profile}"

cleanup() {
  rm -f "$CERT_PATH" "$KEY_PATH" "$DEVELOPER_ID_CA_PATH"
}
trap cleanup EXIT

printf '%s' "$DEVELOPER_ID_CERTIFICATE" | base64 --decode > "$CERT_PATH"
printf '%s' "$DEVELOPER_ID_KEY" | base64 --decode > "$KEY_PATH"

security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security set-keychain-settings -lut 21600 "$KEYCHAIN_PATH"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"

curl -fsSL "$DEVELOPER_ID_CA_URL" -o "$DEVELOPER_ID_CA_PATH"
ACTUAL_DEVELOPER_ID_CA_SHA256=$(shasum -a 256 "$DEVELOPER_ID_CA_PATH" | awk '{ print $1 }')
if [ "$ACTUAL_DEVELOPER_ID_CA_SHA256" != "$DEVELOPER_ID_CA_SHA256" ]; then
  echo "::error::Apple Developer ID intermediate certificate checksum mismatch"
  exit 1
fi
security import "$DEVELOPER_ID_CA_PATH" -k "$KEYCHAIN_PATH"

security import "$CERT_PATH" \
  -k "$KEYCHAIN_PATH" \
  -T /usr/bin/codesign \
  -T /usr/bin/security \
  -T /usr/bin/xcrun
security import "$KEY_PATH" \
  -k "$KEYCHAIN_PATH" \
  -T /usr/bin/codesign \
  -T /usr/bin/security \
  -T /usr/bin/xcrun
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: \
  -s \
  -k "$KEYCHAIN_PASSWORD" \
  "$KEYCHAIN_PATH"
SIGNING_IDENTITY=$(security find-identity -v -p codesigning "$KEYCHAIN_PATH" | awk -F '"' '/Developer ID Application/ { print $2; exit }')
if [ -z "$SIGNING_IDENTITY" ]; then
  echo "::error::Could not find a Developer ID Application identity in the imported certificate"
  exit 1
fi

xcrun notarytool store-credentials "$NOTARY_PROFILE" \
  --apple-id "$NOTARY_APPLE_ID" \
  --team-id "$NOTARY_TEAM_ID" \
  --password "$NOTARY_PASSWORD" \
  --keychain "$KEYCHAIN_PATH"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "keychain_path=$KEYCHAIN_PATH"
    echo "signing_identity=$SIGNING_IDENTITY"
    echo "notary_profile=$NOTARY_PROFILE"
  } >> "$GITHUB_OUTPUT"
fi

echo "Imported signing identity: $SIGNING_IDENTITY"
