#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

ROBOT_HOST="${ROBOT_HOST:-192.168.0.107}"
ROBOT_USER="${ROBOT_USER:-unitree}"
SDK_PATH="${SDK_PATH:-/home/unitree/unitree_sdk2_python}"
REMOTE_PATH="${REMOTE_PATH:-${SDK_PATH}/g1_lowcmd_jsonl_server.py}"

echo "Copying robot server to ${ROBOT_USER}@${ROBOT_HOST}:${REMOTE_PATH}"
scp "${ROOT_DIR}/robot/g1_lowcmd_jsonl_server.py" "${ROBOT_USER}@${ROBOT_HOST}:${REMOTE_PATH}"

echo "Checking syntax on robot"
ssh "${ROBOT_USER}@${ROBOT_HOST}" "cd '${SDK_PATH}' && python3 -m py_compile '${REMOTE_PATH}'"

echo "Installed ${REMOTE_PATH}"
