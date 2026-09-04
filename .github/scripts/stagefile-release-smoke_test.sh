#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /absolute/path/to/wendy" >&2
  exit 2
fi

wendy_binary="$1"
if [[ ! -x "$wendy_binary" ]]; then
  echo "wendy binary is not executable: $wendy_binary" >&2
  exit 2
fi

case "$wendy_binary" in
  /*) ;;
  *) wendy_binary="$(cd "$(dirname "$wendy_binary")" && pwd)/$(basename "$wendy_binary")" ;;
esac

fixture_dir="$(mktemp -d)"
trap 'rm -rf "$fixture_dir"' EXIT

mkdir -p "$fixture_dir/bin"

cat >"$fixture_dir/build.stagefile.yaml" <<'YAML'
version: 1
stages:
  - name: native
    from: busybox:1.37
    pin: false
  - name: app
    from: native
YAML

# The smoke test only needs the packaged CLI to reach the image-builder
# boundary. A fake Docker executable keeps the check fast and proves Stagefile
# compilation completed without contacting a registry for the local `native`
# stage.
cat >"$fixture_dir/bin/docker" <<'SH'
#!/usr/bin/env sh
: >"${WENDY_SMOKE_DOCKER_CALLED:?}"
exit 37
SH
chmod +x "$fixture_dir/bin/docker"

docker_called="$fixture_dir/docker-called"
build_output="$fixture_dir/build-output.txt"
(
  cd "$fixture_dir"
  PATH="$fixture_dir/bin:$PATH" \
    WENDY_SMOKE_DOCKER_CALLED="$docker_called" \
    "$wendy_binary" build --builder docker --dockerfile build.stagefile.yaml \
    >"$build_output" 2>&1 || true
)

if [[ ! -f "$docker_called" ]]; then
  echo "packaged CLI did not reach Docker after compiling the Stagefile" >&2
  cat "$build_output" >&2
  exit 1
fi

generated="$fixture_dir/Dockerfile.generated"
if [[ ! -f "$generated" ]]; then
  echo "packaged CLI did not generate Dockerfile.generated" >&2
  cat "$build_output" >&2
  exit 1
fi

if ! grep -Eq '^FROM( --platform=[^ ]+)? native AS app$' "$generated"; then
  echo "prior Stagefile stage was not preserved as the local base" >&2
  cat "$generated" >&2
  exit 1
fi

if grep -Eq '^FROM( --platform=[^ ]+)? native@' "$generated"; then
  echo "prior Stagefile stage was incorrectly pinned as an external image" >&2
  cat "$generated" >&2
  exit 1
fi

echo "packaged Stagefile inheritance smoke test passed"
