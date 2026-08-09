# Shared preamble for the integration proofs: locate the Command Line Interface
# under test and refuse to guess.
#
# These proofs must run the binary built from this worktree, not whatever
# version happens to be installed, since they exist to verify changes that are
# not released yet. Build it with:
#
#   cd go && CC=/usr/bin/clang make build
#
# The CC prefix is needed wherever clang resolves to a swiftly shim, which fails
# cgo compilation with a bare "exit status 2" and no diagnostic.
integration_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
WENDY_BIN="${WENDY_BIN:-$integration_root/go/bin/wendy}"
if [ ! -x "$WENDY_BIN" ]; then
  echo "no Command Line Interface at $WENDY_BIN; build it with 'cd go && CC=/usr/bin/clang make build', or set WENDY_BIN" >&2
  exit 1
fi

# require_exact_targets fails unless a dry run resolves precisely the devices
# named. A group pattern is a glob: if the network gains a device that matches,
# a proof would silently deploy to it, and on shared hardware that is somebody
# else's machine.
require_exact_targets() {
  local group="$1" template="$2"
  shift 2
  local rendered
  rendered="$("$WENDY_BIN" fleet train up --lan --group "$group" --template "$template" --dry-run 2>&1)" || {
    echo "$rendered" >&2
    return 1
  }
  local resolved
  resolved="$(echo "$rendered" | sed -n 's/^device \([^ ]*\) .*/\1/p' | sort | tr '\n' ' ')"
  local expected
  expected="$(printf '%s\n' "$@" | sort | tr '\n' ' ')"
  if [ "$resolved" != "$expected" ]; then
    echo "target set mismatch for group '$group'" >&2
    echo "  resolved: $resolved" >&2
    echo "  expected: $expected" >&2
    return 1
  fi
  echo "target gate: $group resolved to exactly $expected"
}

# fleet_token reads the token the deploy persisted, which is how collect.py and
# any manual request authenticate against a running fleet.
fleet_token() {
  local app_id="$1"
  python3 - "$app_id" <<'PY'
import glob, json, os, sys
app = sys.argv[1]
matches = glob.glob(os.path.expanduser(f"~/.wendy/train/*{app}.json"))
if not matches:
    sys.exit(f"no saved fleet state for {app}; deploy first")
print(json.load(open(matches[0]))["token"])
PY
}
