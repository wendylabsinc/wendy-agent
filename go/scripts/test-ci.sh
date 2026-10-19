#!/bin/bash
# Test callbacks are invoked indirectly through run_test.
# shellcheck disable=SC2329
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$REPO_DIR/.." && pwd)"
TESTS_DIR="$REPO_ROOT/.github/ci-tests"
CLEANUP_RECEIPT="${WENDY_CI_CLEANUP_RECEIPT:-}"
# Never let an earlier invocation certify this run's cleanup, including a run
# that fails before argument parsing or trap installation.
if [[ -n "$CLEANUP_RECEIPT" ]]; then
    rm -f -- "$CLEANUP_RECEIPT" || exit 1
fi

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Run CI integration tests against a real WendyOS device. Each test deploys a
minimal app that exercises a specific entitlement and verifies it works.

Tests:
  swift-hello               Basic Swift containerized deployment (no entitlements)
  swift-network             Swift with network entitlement (WiFi connectivity)
  swift-bluetooth           Swift with bluetooth entitlement
  swift-resources           Swift app with bundled resources (verifies resource loading)
  python-hello              Basic Python deployment (no entitlements)
  python-network            Python with network entitlement (WiFi connectivity)
  python-gpu                Python with GPU entitlement (CUDA verification)
  python-onnx-gpu           Python with GPU entitlement (ONNX Runtime CUDA inference)
  python-bluetooth          Python with bluetooth entitlement
  python-audio              Python with audio entitlement
  python-notifications      Verify notifications System API socket access WITH entitlement
  python-no-notifications   Verify notifications System API access is blocked WITHOUT entitlement
  python-no-network         Verify network is blocked WITHOUT entitlement
  python-no-bluetooth       Verify bluetooth is blocked WITHOUT entitlement
  python-no-ptrace          Verify ptrace is blocked by default seccomp profile (WDY-1099)
  python-no-unshare         Verify unshare is blocked by default seccomp profile (WDY-1099)
  python-no-kexec-module    Verify kernel-module/kexec syscalls are blocked by seccomp (WDY-1012)
  python-network-host-admin Verify host-admin networking grants CAP_NET_ADMIN (WDY-1094)
  python-no-net-admin       Verify plain host networking does NOT grant CAP_NET_ADMIN (WDY-1094)
  python-resources          Verify app-level CPU/memory/PID limits are enforced via cgroups (WDY-1729)
  python-multiservice-resources  Verify per-service resource override + app-level inheritance (WDY-1729)
  python-multiservice       Multi-service wendy.json: parallel build + dep-order creation
  python-servicename        Single service with serviceName: verifies WENDY_HOSTNAME/WENDY_APP_GROUP env injection (WDY-878)
  python-env                Verify top-level wendy.json env reaches the container, incl. \${VAR} expansion (WDY-2040)
  python-env-flag           Verify 'wendy run --env' overrides wendy.json env per key (WDY-2040)
  python-multiservice-env   Verify app-level env is the per-service default and a service overrides it per key (WDY-2040)
  python-device-top         Deploy a long-running app and verify 'wendy device top --json' reports it (device top)
  compose-hello             docker-compose multi-service deployment with build: Dockerfiles
  compose-images            docker-compose multi-service deployment using public images
  otel-localhost-only       Verify OTEL receivers (4317/4318) are not reachable from the network
  host-sensor-loopback      Verify host loopback modules and device nodes on supported WendyOS boards

Device Selection:
  If --hostname is not provided, the script auto-discovers a device on the
  local network using 'wendy discover --json'. The first LAN device found
  is used.

Options:
  -h, --hostname HOST   Device hostname (skips auto-discovery)
  -w, --wendy PATH      Path to wendy binary (default: wendy on PATH)
  -t, --test NAME       Run only the named test (can be repeated)
  --help                Show this help message

Environment:
  WENDY_CI_LOG_DIR                  Directory for complete per-test logs
  WENDY_CI_COMMAND_TIMEOUT_SECONDS  Build/run deadline (default: 900)
  WENDY_CI_CONTROL_TIMEOUT_SECONDS  Device query/cleanup deadline (default: 30)
  WENDY_CI_CLEANUP_RECEIPT          Optional receipt written after verified cleanup

Examples:
  $(basename "$0")                                  # auto-discover, all tests
  $(basename "$0") -h wendyos-merry-aurora          # explicit host
  $(basename "$0") -t swift-hello -t python-hello   # specific tests only
  $(basename "$0") -w /path/to/wendy                # custom binary
EOF
    # Exit code is a parameter: --help is success, a bad invocation is not.
    # A usage error exiting 0 let CI report a green run that never started
    # a single test (WDY-2171).
    exit "${1:-0}"
}

HOSTNAME=""
HOSTNAME_PROVIDED=false
WENDY="wendy"
SELECTED_TESTS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--hostname)      HOSTNAME="$2"; HOSTNAME_PROVIDED=true; shift 2 ;;
        -w|--wendy)         WENDY="$2"; shift 2 ;;
        -t|--test)          SELECTED_TESTS+=("$2"); shift 2 ;;
        --help)             usage ;;
        *)                  echo "Unknown option: $1" >&2; usage 2 ;;
    esac
done

# Add .local suffix only for bare mDNS hostnames (no dots or colons).
# Leave IPs (e.g. 192.168.1.1), FQDNs (device.example.com), and IPv6 alone.
if [[ "$HOSTNAME_PROVIDED" == true ]] && [[ "$HOSTNAME" != *.local ]] && [[ "$HOSTNAME" != *.* ]] && [[ "$HOSTNAME" != *:* ]]; then
    HOSTNAME="${HOSTNAME}.local"
fi

# ── Colors & test harness ────────────────────────────────────────────

GREEN="\033[0;32m"
RED="\033[0;31m"
YELLOW="\033[0;33m"
BOLD="\033[1m"
RESET="\033[0m"

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
TEST_SEQUENCE=0
LOG_DIR="${WENDY_CI_LOG_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/wendy-ci-logs.XXXXXX")}"
mkdir -p "$LOG_DIR" || exit 1
echo "Full test logs: $LOG_DIR"

# Keep a build or broken streaming RPC from consuming the whole rack job. A
# separate process group lets the deadline terminate the CLI and its builders.
ci_command() {
    local deadline="$1"; shift
    python3 - "$deadline" "$@" <<'PY'
import os
import signal
import subprocess
import sys

timeout = float(sys.argv[1])
if timeout <= 0:
    raise SystemExit("CI command timeout must be positive")
command = subprocess.Popen(sys.argv[2:], stdin=subprocess.DEVNULL, start_new_session=True)

def forward(signum, _frame=None):
    try:
        os.killpg(command.pid, signum)
    except ProcessLookupError:
        pass

def stop():
    forward(signal.SIGTERM)
    try:
        command.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass
    forward(signal.SIGKILL)
    command.wait()

def interrupted(signum, _frame):
    stop()
    sys.exit(128 + signum)

signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGINT, interrupted)
try:
    result = command.wait(timeout=timeout)
except subprocess.TimeoutExpired:
    print(f"Timed out after {timeout:g}s: {sys.argv[2]}", file=sys.stderr, flush=True)
    stop()
    result = 124
# Descendants may still hold the output pipe after the CLI has exited.
forward(signal.SIGKILL)
sys.exit(result if result >= 0 else 128 - result)
PY
}

wendy_cli() {
    ci_command "${WENDY_CI_COMMAND_TIMEOUT_SECONDS:-900}" "$WENDY" "$@"
}

wendy_control() {
    ci_command "${WENDY_CI_CONTROL_TIMEOUT_SECONDS:-30}" "$WENDY" "$@"
}

run_test() {
    local name="$1"
    shift
    printf "  %-50s " "$name"
    local logfile
    ((TEST_SEQUENCE++))
    logfile="$LOG_DIR/$(printf '%03d' "$TEST_SEQUENCE")-$(printf '%s' "$name" | tr -c 'A-Za-z0-9_.-' '_').log"
    "$@" >"$logfile" 2>&1
    local rc=$?
    if [[ $rc -eq 0 ]]; then
        echo -e "${GREEN}PASS${RESET}"
        ((PASS_COUNT++))
    else
        echo -e "${RED}FAIL${RESET} (exit $rc)"
        tail -10 "$logfile"
        echo "    Full log: $logfile"
        ((FAIL_COUNT++))
    fi
    return "$rc"
}

PENDING_APPS=()
CURRENT_APP_INDEX=""
DISCOVER_STDERR=""

cleanup_app() {
    local app_id="$1" listing
    listing=$(wendy_control device apps list --device "$HOSTNAME" --json) || return 1
    jq -e 'type == "array"' <<<"$listing" >/dev/null || return 1
    if jq -e --arg app "$app_id" 'any(.name == $app)' <<<"$listing" >/dev/null; then
        wendy_control device apps remove "$app_id" --device "$HOSTNAME" --force --cleanup || return 1
        listing=$(wendy_control device apps list --device "$HOSTNAME" --json) || return 1
        if ! jq -e --arg app "$app_id" 'type == "array" and all(.name != $app)' <<<"$listing" >/dev/null; then
            echo "App $app_id remains after cleanup; the device needs inspection"
            return 1
        fi
    fi
}

finish_current_app() {
    if [[ -n "$CURRENT_APP_INDEX" ]]; then
        if run_test "cleanup ${PENDING_APPS[$CURRENT_APP_INDEX]}" cleanup_app "${PENDING_APPS[$CURRENT_APP_INDEX]}"; then
            unset 'PENDING_APPS[CURRENT_APP_INDEX]'
        fi
        CURRENT_APP_INDEX=""
    fi
}

finish_ci() {
    local rc=$? index cleanup_ok=true interrupted=false
    [[ "$rc" -ne 130 && "$rc" -ne 143 ]] || interrupted=true
    trap - EXIT INT TERM
    for index in "${!PENDING_APPS[@]}"; do
        if ! run_test "cleanup ${PENDING_APPS[$index]}" cleanup_app "${PENDING_APPS[$index]}"; then
            rc=1
            cleanup_ok=false
        fi
    done
    [[ -z "$DISCOVER_STDERR" ]] || rm -f "$DISCOVER_STDERR"
    if [[ "$cleanup_ok" == true && "$interrupted" == false && -n "$CLEANUP_RECEIPT" ]]; then
        printf '{"cleanupVerified":true}\n' >"$CLEANUP_RECEIPT" || rc=1
    fi
    exit "$rc"
}

trap finish_ci EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# A per-run docker-container builder is shared by every integration fixture.
# Retry one failed OCI export because self-hosted runners occasionally lose a
# solve while otherwise remaining healthy. If buildkitd itself died, recreate
# the disposable builder first so one failure does not cascade through the
# remaining fixtures. This is deliberately gated on WENDY_BUILDX_BUILDER; local
# user builds are never retried or removed automatically.
prepare_ci_oci_retry() {
    local output="$1"
    [[ -n "${WENDY_BUILDX_BUILDER:-}" ]] || return 1

    if grep -qiE \
        'failed to receive status:.*(EOF|Unavailable)|error reading from server: EOF|/run/buildkit/buildkitd\.sock: connect: connection refused|failed to list workers:.*Unavailable|ResourceExhausted:.*cannot allocate memory' \
        <<<"$output"; then
        local builder="${WENDY_BUILDX_BUILDER}-oci"
        echo "    BuildKit daemon failed; recreating disposable CI builder $builder and retrying once"
        ci_command "${WENDY_CI_CONTROL_TIMEOUT_SECONDS:-30}" docker buildx rm "$builder" --force >/dev/null 2>&1 || true
        return 0
    fi

    # `wendy run` prints the builder's error in its build failure details
    # ("Error: docker buildx build (OCI export) failed: ...") and, when it found
    # the failing step, ends with "build failed at [<stage n/m>] ..." instead.
    grep -qE 'docker buildx build \(OCI export\) failed|build failed at \[' <<<"$output" || return 1
    echo "    OCI export failed on the disposable CI builder; retrying once"
}

# ── Container verdict ────────────────────────────────────────────────
# Require both a successful CLI command and an independently observed app
# result. Grouped services currently expose state but no individual exit code,
# so their stopped state must also be accompanied by each fixture's verdict.

# app_id_for_test prints the appId a test directory declares.
app_id_for_test() {
    if [[ -f "$1/wendy.json" ]]; then
        jq -r '.appId // empty' "$1/wendy.json" 2>/dev/null
    else
        # These Compose-only fixtures use the directory as their group ID.
        case "$(basename "$1")" in
            compose-hello|compose-images) basename "$1" ;;
        esac
    fi
}

assert_container_verdict() {
    local app_id="$1" out="$2" code="" state="" snapshot="" listing="" service
    shift 2
    for _ in 1 2 3 4 5 6; do
        listing=$(wendy_control device apps list --device "$HOSTNAME" --json) || return 1
        snapshot=$(jq -ce --arg app "$app_id" '
            [.[] | select(.name == $app)] |
            if length == 1 then .[0] else error("expected exactly one deployed app") end
        ' <<<"$listing") || return 1
        state=$(jq -r '.runningState // empty' <<<"$snapshot")
        if jq -e '([.runningState] + [.services[]?.runningState]) | any(. == "CRASH_LOOPING")' <<<"$snapshot" >/dev/null; then
            echo "App $app_id reports a crash: $snapshot"
            return 1
        fi
        if jq -e '.runningState == "STOPPED" and ([.services[]?.runningState] | all(. == "STOPPED"))' <<<"$snapshot" >/dev/null; then
            break
        fi
        sleep "${WENDY_CI_POLL_INTERVAL_SECONDS:-1}"
    done
    if ! jq -e '.runningState == "STOPPED" and ([.services[]?.runningState] | all(. == "STOPPED"))' <<<"$snapshot" >/dev/null; then
        echo "App $app_id did not finish (runningState=$state): $snapshot"
        return 1
    fi
    if [[ $# -gt 0 ]]; then
        local expected actual
        expected=$(printf '%s\n' "$@" | sort)
        actual=$(jq -r '.services[]?.name' <<<"$snapshot" | sort)
        if [[ "$actual" != "$expected" ]]; then
            echo "App $app_id services differ: expected [$expected], got [$actual]"
            return 1
        fi
        for service in "$@"; do
            if ! grep -qE "(^|[[:space:]])${service}: PASS([[:space:]]|$)" <<<"$out"; then
                echo "Missing successful verdict for service $service"
                return 1
            fi
        done
    fi
    if grep -qE '(^|[^[:alnum:]_])(FAIL|Traceback)(:|[[:space:]]|$)' <<<"$out"; then
        echo "App $app_id printed a failure verdict"
        return 1
    fi
    code=$(jq -r '.exitCode // empty' <<<"$snapshot")
    if [[ -n "$code" ]]; then
        if [[ "$code" != "0" ]]; then
            echo "App $app_id exited $code (runningState=$state) - its assertions failed"
            return 1
        fi
        return 0
    fi

    if ! grep -qE '(^|[[:space:]])PASS([[:space:]:]|$)' <<<"$out"; then
        echo "App $app_id reports no exit code (runningState=$state) and printed no PASS verdict"
        return 1
    fi
    return 0
}

# run_container_test deploys a single-container test and requires both that the
# deploy succeeded and that the app exited 0.
run_container_test() {
    local test_name="$1" test_dir="$2"; shift 2
    local app_id
    app_id=$(app_id_for_test "$test_dir")

    container_test_passes() {
        local out rc
        # --no-restart stops the agent restarting a test app that has done its
        # job and exited, which otherwise churns the recorded state (a
        # short-lived app reads back as CRASH_LOOPING).
        out=$(wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --no-restart "$@" 2>&1)
        rc=$?
        if [[ $rc -ne 0 ]] && prepare_ci_oci_retry "$out"; then
            echo "$out"
            out=$(wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --no-restart "$@" 2>&1)
            rc=$?
        fi
        echo "$out"
        if [[ $rc -ne 0 ]]; then
            return "$rc"
        fi
        if [[ -z "$app_id" ]]; then
            echo "Could not read appId from $test_dir/wendy.json; cannot verify the app's verdict"
            return 1
        fi
        assert_container_verdict "$app_id" "$out"
    }
    run_test "$test_name" container_test_passes "$@"
}

run_service_test() {
    local test_name="$1" test_dir="$2" expected_services="$3" app_id
    shift 3
    app_id=$(app_id_for_test "$test_dir")
    service_test_passes() {
        local out rc
        local services=()
        IFS=',' read -ra services <<<"$expected_services"
        out=$(wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --no-restart "$@" 2>&1)
        rc=$?
        echo "$out"
        [[ $rc -eq 0 ]] || return "$rc"
        assert_container_verdict "$app_id" "$out" "${services[@]}"
    }
    run_test "$test_name" service_test_passes "$@"
}

otel_port_closed() {
    # A failed DNS lookup or unreachable rack must never prove isolation.
    # Device info can be public during setup; app listing proves authenticated
    # access to the running agent before a failed port connection can count.
    local listing
    listing=$(wendy_control device apps list --device "$HOSTNAME" --json) || return 1
    jq -e 'type == "array"' <<<"$listing" >/dev/null || return 1
    python3 - "$HOSTNAME" "$1" <<'PY'
import errno
import signal
import socket
import sys

class ProbeDeadline(RuntimeError):
    pass

def deadline(_signum, _frame):
    raise ProbeDeadline("OTEL probe exceeded its 15s deadline")

signal.signal(signal.SIGALRM, deadline)
signal.alarm(15)
host, port = sys.argv[1], int(sys.argv[2])
try:
    addresses = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    if not addresses:
        raise RuntimeError("no addresses resolved")
    for family, kind, proto, _, address in addresses:
        with socket.socket(family, kind, proto) as probe:
            probe.settimeout(3)
            try:
                probe.connect(address)
            except (ConnectionRefusedError, TimeoutError):
                continue
            except OSError as error:
                if error.errno in (errno.ECONNREFUSED, errno.ETIMEDOUT):
                    continue
                raise
            raise RuntimeError(f"OTEL port {port} accepts network connections at {address}")
except (OSError, RuntimeError) as error:
    print(f"Could not verify OTEL isolation: {error}", file=sys.stderr)
    sys.exit(1)
print(f"OTEL port {port} refused or timed out on a reachable device")
PY
}

skip_test() {
    local name="$1"
    local reason="${2:-}"
    printf "  %-50s " "$name"
    if [[ -n "$reason" ]]; then
        echo -e "${YELLOW}SKIP${RESET} ($reason)"
    else
        echo -e "${YELLOW}SKIP${RESET}"
    fi
    ((SKIP_COUNT++))
}

# ── Wendy binary validation ─────────────────────────────────────────

if [[ "$WENDY" != "wendy" ]]; then
    if [[ ! -x "$WENDY" ]]; then
        echo -e "${RED}ERROR: wendy binary not found or not executable at $WENDY${RESET}"
        exit 1
    fi
    WENDY="$(cd "$(dirname "$WENDY")" && pwd)/$(basename "$WENDY")"
else
    if ! command -v wendy &>/dev/null; then
        echo -e "${RED}ERROR: 'wendy' not found on PATH${RESET}"
        echo "Hint: pass -w /path/to/wendy to specify the binary location."
        exit 1
    fi
    WENDY="$(command -v wendy)"
fi

echo -e "${BOLD}==> Using wendy: ${WENDY}${RESET}"
echo ""

# ── Device discovery ─────────────────────────────────────────────────

if [[ -z "$HOSTNAME" ]]; then
    echo -e "${BOLD}==> Auto-discovering device...${RESET}"
    DISCOVER_STDERR=$(mktemp -t wendy-discover-stderr.XXXXXX)
    DISCOVER_JSON=$(wendy_control discover --json --timeout 5s 2>"$DISCOVER_STDERR")
    cat "$DISCOVER_STDERR" >&2 || true
    DISCOVERED_HOST=$(echo "$DISCOVER_JSON" | jq -r '.lanDevices[0].hostname // empty' 2>/dev/null)
    if [[ -z "$DISCOVERED_HOST" ]]; then
        echo -e "${RED}ERROR: No LAN device found via 'wendy discover --json --timeout 5s'${RESET}"
        echo "    Output: $(echo "$DISCOVER_JSON" | head -5)"
        echo ""
        echo "Hint: pass -h <hostname> to skip auto-discovery."
        exit 1
    fi
    HOSTNAME="$DISCOVERED_HOST"
fi

echo -e "${BOLD}==> Target device: ${HOSTNAME}${RESET}"
echo ""

# ── Device under test ────────────────────────────────────────────────
# Recorded before anything runs, because a failure caused by an out-of-date
# agent is otherwise indistinguishable in the log from a product bug. The whole
# suite's verdict is only meaningful against a known agent, so identify it.

VERSION_JSON=$(wendy_control device info --json --device "$HOSTNAME") || exit 1
if ! jq -e 'type == "object"' <<<"$VERSION_JSON" >/dev/null; then
    echo "ERROR: device info did not return a JSON object" >&2
    exit 1
fi

# device_field prints a field of VERSION_JSON, or its fallback when the field is
# absent, null, or the empty string — a device reporting no deviceType at all is
# exactly the case worth seeing, so an empty value must not read as blank.
device_field() {
    local value=""
    if [[ -n "$VERSION_JSON" ]]; then
        value=$(echo "$VERSION_JSON" | jq -r --arg f "$1" '.[$f] // ""' 2>/dev/null || true)
    fi
    if [[ -n "$value" ]]; then
        echo "$value"
    else
        echo "$2"
    fi
}

if [[ -z "$VERSION_JSON" ]]; then
    echo -e "${YELLOW}==> WARNING: 'wendy device info' returned nothing — the agent under test cannot be identified${RESET}"
fi
DEVICE_TYPE=$(device_field deviceType '')
DEVICE_GPU_ARCH=$(device_field gpuArch '')

echo -e "${BOLD}==> Agent version: $(device_field version 'unknown')${RESET}"
echo -e "${BOLD}==> Device OS: $(device_field os 'unknown') $(device_field osVersion 'unknown')${RESET}"
echo -e "${BOLD}==> Device type: ${DEVICE_TYPE:-none reported} ($(device_field cpuArchitecture 'unknown'))${RESET}"

# ── Device capability detection ──────────────────────────────────────

# Both *-gpu tests are CUDA tests, so they need an NVIDIA GPU specifically —
# hasGpu is also true for any device with a /dev/dri node, which selected them
# on an amd64 host with an AMD iGPU where a CUDA test cannot pass.
DEVICE_GPU_VENDOR=$(device_field gpuVendor '')
DEVICE_HAS_CUDA=false
if [[ "$DEVICE_GPU_VENDOR" == "nvidia" ]]; then
    DEVICE_HAS_CUDA=true
fi

# GPU framework wheels are architecture-specific. Pick a userspace that
# matches the device instead of treating the NVIDIA vendor bit as sufficient:
# Orin (sm_87) uses the existing JetPack-6 / CUDA-12 fixture, while Thor
# (sm_110) uses the Ubuntu-24.04 / CUDA-13 fixture. Older agents did not report
# gpuArch, so identify Thor by device type and preserve CUDA-12 behavior for
# other older NVIDIA devices. Every other reported architecture remains opt-in
# after its fixture is hardware-verified.
DEVICE_GPU_FIXTURE_DOCKERFILE=""
if [[ "$DEVICE_HAS_CUDA" == "true" ]]; then
    case "$DEVICE_GPU_ARCH" in
        sm_87)          DEVICE_GPU_FIXTURE_DOCKERFILE="Dockerfile" ;;
        sm_110|sm_121)  DEVICE_GPU_FIXTURE_DOCKERFILE="Dockerfile.cuda13" ;;
        "")
            # gpuArch was added after Thor support. Device type lets those
            # older Thor agents select CUDA 13 without regressing older Orin
            # and generic NVIDIA agents, which retain the CUDA-12 fixture.
            if [[ "$DEVICE_TYPE" == "jetson-agx-thor" ]]; then
                DEVICE_GPU_FIXTURE_DOCKERFILE="Dockerfile.cuda13"
            else
                DEVICE_GPU_FIXTURE_DOCKERFILE="Dockerfile"
            fi
            ;;
    esac
fi
echo -e "${BOLD}==> CUDA GPU: ${DEVICE_HAS_CUDA} (vendor: ${DEVICE_GPU_VENDOR:-none}, arch: ${DEVICE_GPU_ARCH:-unknown})${RESET}"

# The Swift tests build their image with swift-container-plugin, so this host
# needs a Swift toolchain — which the CLI provisions through swiftly, and
# refuses to proceed without. Checking swiftly rather than swift matters on
# macOS, where /usr/bin/swift is an Xcode shim that exists even with no usable
# toolchain behind it. Skipping here rather than listing tests per-runner in
# the workflow keeps one source of truth for what exists.
HOST_HAS_SWIFT=false
if command -v swiftly &>/dev/null; then
    HOST_HAS_SWIFT=true
fi
echo -e "${BOLD}==> Swift toolchain (swiftly): ${HOST_HAS_SWIFT}${RESET}"
echo ""

# ── Validate tests directory ─────────────────────────────────────────

if [[ ! -d "$TESTS_DIR" ]]; then
    echo -e "${RED}ERROR: CI tests directory not found at $TESTS_DIR${RESET}"
    exit 1
fi

# ── Ordered test list ────────────────────────────────────────────────
# Swift (containerized) first, then Python, basic → entitlements.

ALL_TESTS=(
    swift-hello
    swift-start-detach
    swift-network
    swift-bluetooth
    swift-resources
    python-hello
    python-network
    python-gpu
    python-onnx-gpu
    python-bluetooth
    python-audio
    python-notifications
    python-no-notifications
    python-no-network
    python-no-bluetooth
    python-no-ptrace
    python-no-unshare
    python-no-kexec-module
    python-network-host-admin
    python-no-net-admin
    python-resources
    python-multiservice
    python-multiservice-resources
    python-servicename
    python-env
    python-env-flag
    python-multiservice-env
    compose-hello
    compose-images
    compose-companion
    otel-localhost-only
    host-sensor-loopback
    python-device-top
)

# If specific tests were requested via -t, filter the list.
if [[ ${#SELECTED_TESTS[@]} -gt 0 ]]; then
    TESTS=()
    for sel in "${SELECTED_TESTS[@]}"; do
        found=false
        for t in "${ALL_TESTS[@]}"; do
            if [[ "$t" == "$sel" ]]; then
                TESTS+=("$t")
                found=true
                break
            fi
        done
        if [[ "$found" == false ]]; then
            echo -e "${RED}ERROR: Unknown test '$sel'${RESET}"
            echo "Available tests: ${ALL_TESTS[*]}"
            exit 1
        fi
    done
else
    TESTS=("${ALL_TESTS[@]}")
fi


echo -e "${BOLD}==> Running ${#TESTS[@]} test(s)${RESET}"
echo ""

# ── Test loop ────────────────────────────────────────────────────────

for test_name in "${TESTS[@]}"; do
    finish_current_app
    test_dir="$TESTS_DIR/$test_name"

    # This checks the device host through authenticated mTLS, not a container's
    # /lib/modules or device mounts. Missing required image support is a failure.
    if [[ "$test_name" == "host-sensor-loopback" ]]; then
        device_os=$(device_field os '')
        loopback_scope=""
        if [[ "$device_os" == "wendyos" ]]; then
            case "$DEVICE_TYPE" in
                dragonwing-iq-8275|dragonwing-iq-9075|arduino-uno-q) loopback_scope="video-audio" ;;
                jetson-orin-nano|jetson-agx-orin|jetson-agx-orin-emmc|jetson-agx-thor) loopback_scope="video" ;;
                raspberry-pi-3|raspberry-pi-4|raspberry-pi-5|generic-x86-64|vm-arm64|vm-x86-64|qemu-arm64)
                    skip_test "$test_name" "no loopback image contract for $DEVICE_TYPE"
                    continue
                    ;;
                *)
                    unknown_loopback_board() { echo "Cannot verify loopback image contract for unknown WendyOS device type '${DEVICE_TYPE:-missing}'"; return 1; }
                    run_test "$test_name" unknown_loopback_board
                    continue
                    ;;
            esac
        elif [[ -n "$device_os" ]]; then
            skip_test "$test_name" "requires a supported WendyOS image; device reports $device_os"
            continue
        else
            unknown_loopback_os() { echo "Cannot identify device OS for host loopback verification"; return 1; }
            run_test "$test_name" unknown_loopback_os
            continue
        fi
        run_test "$test_name" python3 "$test_dir/run_host_check.py" "$WENDY" "$HOSTNAME" "$loopback_scope"
        continue
    fi

    if [[ "$test_name" == swift-* ]] && [[ "$HOST_HAS_SWIFT" != "true" ]]; then
        skip_test "$test_name" "no Swift toolchain (swiftly) on this host"
        continue
    fi

    if [[ "$test_name" == *"-gpu"* ]] && [[ "$DEVICE_HAS_CUDA" != "true" ]]; then
        skip_test "$test_name" "no NVIDIA GPU (vendor: ${DEVICE_GPU_VENDOR:-none})"
        continue
    fi

    if [[ "$test_name" == *"-gpu"* ]] && [[ -z "$DEVICE_GPU_FIXTURE_DOCKERFILE" ]]; then
        skip_test "$test_name" "CI GPU fixtures support sm_87, sm_110, and sm_121; device reports ${DEVICE_GPU_ARCH:-an unknown architecture}"
        continue
    fi

    app_id=$(app_id_for_test "$test_dir")
    if [[ -n "$app_id" ]]; then
        CURRENT_APP_INDEX="${#PENDING_APPS[@]}"
        PENDING_APPS[CURRENT_APP_INDEX]="$app_id"
        if ! run_test "prepare $test_name" cleanup_app "$app_id"; then
            continue
        fi
    fi

    # ── Security: OTEL ports must not be reachable from the network ──────
    if [[ "$test_name" == "otel-localhost-only" ]]; then
        run_test "otel-localhost-only (gRPC 4317 not reachable)" otel_port_closed 4317
        run_test "otel-localhost-only (HTTP 4318 not reachable)" otel_port_closed 4318
        continue
    fi

    # ── Multi-service test ───────────────────────────────────────────────
    if [[ "$test_name" == "python-multiservice" ]]; then
        echo -e "${BOLD}── $test_name${RESET}"

        # 1. Full multi-service deploy: both services build & run.
        run_service_test "python-multiservice (all services)" "$test_dir" db,api

        # 2. --service filtering: deploy only 'api' (and its dep 'db').
        run_service_test "python-multiservice (--service api)" "$test_dir" db,api --service api

        # 3. --service with unknown name must fail with a clear error.
        unknown_service_fails() {
            local out
            out=$(wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --deploy --service ghost 2>&1)
            local rc=$?
            if [[ $rc -ne 0 ]] && echo "$out" | grep -qi "ghost"; then
                return 0
            fi
            echo "Expected non-zero exit and 'ghost' in output; got rc=$rc output=$out"
            return 1
        }
        run_test "python-multiservice (--service ghost -> error)" unknown_service_fails

        continue
    fi

    # ── Multi-service resource limits (WDY-1729) ─────────────────────────
    # Deploys two services: 'db' inherits the app-level memory limit (256Mi),
    # 'api' overrides it to 128Mi while inheriting pids. Each service asserts
    # its own cgroup limits and prints "<svc>: PASS" / "<svc>: FAIL". We read
    # both stopped service states and their PASS lines with no FAIL.
    if [[ "$test_name" == "python-multiservice-resources" ]]; then
        app_id="sh.wendy.ci.python-multiservice-resources"

        # Attached, not --deploy: --deploy creates the containers without
        # starting them, so neither service would run and neither verdict
        # would exist to read (WDY-2171). Attached mode streams both services'
        # stdout, and per-service exit codes are not reported by the device, so
        # assert on the verdict lines.
        run_service_test "python-multiservice-resources (per-service limits)" "$test_dir" db,api

        continue
    fi

    # ── Multi-service env (WDY-2040) ─────────────────────────────────────
    # 'alpha' declares no env and must inherit both app-level keys; 'beta'
    # overrides one and adds its own, and must still inherit the other. Each
    # service prints "<svc>: PASS" / "<svc>: FAIL" and exits.
    #
    # Deliberately an attached run, not --deploy: --deploy creates the
    # containers without starting them, so neither service would ever run.
    # Attached mode streams both services' stdout, and the per-service exit
    # codes do not reach the CLI, so assert on the verdict lines.
    if [[ "$test_name" == "python-multiservice-env" ]]; then
        app_id="sh.wendy.ci.python-multiservice-env"

        run_service_test "$test_name" "$test_dir" alpha,beta

        continue
    fi

    # ── device top (live monitor) ────────────────────────────────────────
    # Deploy a long-running app, then verify `wendy device top --json` reports
    # the host snapshot and lists the deployed container.
    if [[ "$test_name" == "python-device-top" ]]; then
        echo -e "${BOLD}── $test_name${RESET}"
        app_id="sh.wendy.ci.python-device-top"

        run_test "python-device-top (deploy)" \
            wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --detach

        device_top_snapshot() {
            # stderr stays out of the jq input: in JSON mode it can carry
            # notices, progress, or the error envelope.
            local out err rc errf
            errf=$(mktemp)
            out=$(wendy_control device top --device "$HOSTNAME" --json 2>"$errf")
            rc=$?
            err=$(cat "$errf"); rm -f "$errf"
            if [[ $rc -ne 0 ]]; then
                echo "wendy device top --json failed (rc=$rc): $err $out"
                return 1
            fi
            if ! echo "$out" | jq -e '.host.cpuCount > 0 and .host.memTotalBytes > 0' >/dev/null 2>&1; then
                echo "host.cpuCount / host.memTotalBytes missing or zero: $out"
                return 1
            fi
            if ! echo "$out" | jq -e --arg a "$app_id" \
				'(.containers // []) | any(.name == $a and .state == "running")' >/dev/null 2>&1; then
				echo "running app '$app_id' with populated state not found in containers[]: $out"
                return 1
            fi
            return 0
        }
        run_test "python-device-top (snapshot reports host + container)" device_top_snapshot

        continue
    fi

    # ── apps start reporting (detached start must confirm the start) ──────
    # A detached `apps start` must return only after the agent confirms the
    # container started (deploy first WITHOUT starting so no prior restart
    # policy masks the result), and must fail — not falsely succeed — when the
    # target app does not exist.
    if [[ "$test_name" == "swift-start-detach" ]]; then
        echo -e "${BOLD}── $test_name${RESET}"
        app_id="sh.wendy.ci.swift-start-detach"

        run_test "swift-start-detach (deploy, not started)" \
            wendy_cli run --device "$HOSTNAME" --prefix "$test_dir" --deploy

        detach_start_reaches_running() {
            if ! wendy_control device apps start "$app_id" --device "$HOSTNAME" --detach; then
                echo "apps start --detach returned non-zero"
                return 1
            fi
            local out err errf
            errf=$(mktemp)
            for _ in 1 2 3 4 5; do
                # stderr stays out of the jq input (notices, error envelope),
                # but is kept for the failure message.
                out=$(wendy_control device apps list --device "$HOSTNAME" --json 2>"$errf")
                if echo "$out" | jq -e --arg a "$app_id" \
                    '(.[] | select(.name==$a) | .runningState) == "RUNNING"' >/dev/null 2>&1; then
                    rm -f "$errf"
                    return 0
                fi
                sleep 1
            done
            err=$(cat "$errf"); rm -f "$errf"
            echo "app '$app_id' never reached RUNNING after detached start: $out $err"
            return 1
        }
        run_test "swift-start-detach (detached start reaches RUNNING)" detach_start_reaches_running

        detach_start_missing_app_fails() {
            if wendy_control device apps start sh.wendy.ci.does-not-exist --device "$HOSTNAME" --detach >/dev/null 2>&1; then
                echo "apps start --detach of a nonexistent app unexpectedly reported success"
                return 1
            fi
            return 0
        }
        run_test "swift-start-detach (missing app start fails)" detach_start_missing_app_fails

        continue
    fi

    # ── env tests ────────────────────────────────────────────────────────
    if [[ "$test_name" == python-env* ]]; then
        if [[ "$test_name" == "python-env-flag" ]]; then
            # The overrides are the thing under test, so the runner supplies
            # them: wendy.json sets CI_ENV_LEVEL=info and the flag must win,
            # CI_ENV_REGION must survive, CI_ENV_ONLY_FLAG is flag-only.
            run_container_test "$test_name" "$test_dir" --env CI_ENV_LEVEL=debug --env CI_ENV_ONLY_FLAG=1
        else
            run_container_test "$test_name" "$test_dir"
        fi
        continue
    fi

    # ── compose tests ────────────────────────────────────────────────────
    if [[ "$test_name" == compose-* ]]; then
        if [[ "$test_name" == "compose-companion" ]]; then
            run_service_test "$test_name" "$test_dir" greeter
        else
            run_service_test "$test_name" "$test_dir" greeter,reporter
        fi
        continue
    fi

    # ── Standard single-container tests ─────────────────────────────────
    if [[ "$test_name" == *"-gpu"* ]]; then
        # Both GPU test directories contain architecture-specific Dockerfiles,
        # so name the selected one explicitly and avoid an interactive picker.
        run_container_test "$test_name" "$test_dir" --dockerfile "$DEVICE_GPU_FIXTURE_DOCKERFILE"
    else
        run_container_test "$test_name" "$test_dir"
    fi
done
finish_current_app

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo -e "${BOLD}==> Results: ${GREEN}${PASS_COUNT} passed${RESET}, ${RED}${FAIL_COUNT} failed${RESET}, ${YELLOW}${SKIP_COUNT} skipped${RESET}"
echo ""

if [[ $FAIL_COUNT -gt 0 ]]; then
    exit 1
fi
exit 0
