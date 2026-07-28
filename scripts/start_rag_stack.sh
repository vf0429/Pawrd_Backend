#!/usr/bin/env bash
set -euo pipefail

# One-command starter for:
# 1) Python RAG service (prototypes/llamaindex_rag/serve.py)
# 2) Go backend chat proxy (cmd/server)
#
# First run:
# - creates .venv-llamaindex if missing
# - installs Python deps once
#
# Later runs:
# - skips reinstall unless requirements.txt changed
#
# Usage:
#   ./scripts/start_rag_stack.sh
#   ./scripts/start_rag_stack.sh --stop
#   ./scripts/start_rag_stack.sh --restart
# Optional env overrides:
#   RAG_PORT=8098 BACKEND_PORT=8012 ./scripts/start_rag_stack.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VENV_DIR="${ROOT_DIR}/.venv-llamaindex"
REQ_FILE="${ROOT_DIR}/prototypes/llamaindex_rag/requirements.txt"
REQ_HASH_FILE="${VENV_DIR}/.requirements.sha256"

RAG_PORT="${RAG_PORT:-8098}"
BACKEND_PORT="${BACKEND_PORT:-8012}"

RAG_PID_FILE="${ROOT_DIR}/.rag_python.pid"
BACKEND_PID_FILE="${ROOT_DIR}/.rag_go_backend.pid"

RAG_LOG="${ROOT_DIR}/.rag_python.log"
BACKEND_LOG="${ROOT_DIR}/.rag_go_backend.log"
MODE="${1:-start}"

hash_cmd() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

is_port_in_use() {
  local port="$1"
  lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1
}

port_listener_pids() {
  local port="$1"
  lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true
}

kill_pid_file_if_alive() {
  local pid_file="$1"
  if [[ -f "${pid_file}" ]]; then
    local pid
    pid="$(cat "${pid_file}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]] && kill -0 "${pid}" >/dev/null 2>&1; then
      kill "${pid}" >/dev/null 2>&1 || true
    fi
    rm -f "${pid_file}"
  fi
}

stop_stack() {
  echo "[stop] stopping local RAG stack if running"
  kill_pid_file_if_alive "${RAG_PID_FILE}"
  kill_pid_file_if_alive "${BACKEND_PID_FILE}"
  echo "[stop] done"
}

kill_port_listeners_if_any() {
  local port="$1"
  local pids
  pids="$(port_listener_pids "${port}")"
  if [[ -n "${pids}" ]]; then
    echo "[stop] killing listeners on :${port} -> ${pids}"
    # shellcheck disable=SC2086
    kill ${pids} >/dev/null 2>&1 || true
    sleep 1
    if is_port_in_use "${port}"; then
      echo "[stop] force killing listeners on :${port}"
      # shellcheck disable=SC2086
      kill -9 ${pids} >/dev/null 2>&1 || true
    fi
  fi
}

if [[ "${MODE}" == "--stop" ]]; then
  stop_stack
  exit 0
fi

if [[ "${MODE}" == "--restart" ]]; then
  stop_stack
  # Restart mode should recover from orphan processes not tracked by PID files.
  kill_port_listeners_if_any "${RAG_PORT}"
  kill_port_listeners_if_any "${BACKEND_PORT}"
fi

if is_port_in_use "${RAG_PORT}"; then
  echo "Error: RAG port ${RAG_PORT} is already in use."
  echo "Hint: lsof -nP -iTCP:${RAG_PORT} -sTCP:LISTEN"
  exit 1
fi

if is_port_in_use "${BACKEND_PORT}"; then
  echo "Error: backend port ${BACKEND_PORT} is already in use."
  echo "Hint: lsof -nP -iTCP:${BACKEND_PORT} -sTCP:LISTEN"
  exit 1
fi

cd "${ROOT_DIR}"

if [[ ! -d "${VENV_DIR}" ]]; then
  echo "[init] creating Python venv at ${VENV_DIR}"
  python3 -m venv "${VENV_DIR}"
fi

REQ_HASH_NOW="$(hash_cmd "${REQ_FILE}")"
REQ_HASH_OLD=""
if [[ -f "${REQ_HASH_FILE}" ]]; then
  REQ_HASH_OLD="$(cat "${REQ_HASH_FILE}")"
fi

if [[ "${REQ_HASH_NOW}" != "${REQ_HASH_OLD}" ]]; then
  echo "[init] installing/updating Python dependencies"
  "${VENV_DIR}/bin/pip" install -r "${REQ_FILE}"
  echo "${REQ_HASH_NOW}" > "${REQ_HASH_FILE}"
else
  echo "[init] Python dependencies unchanged, skip install"
fi

# Build index only if missing.
if [[ ! -f "${ROOT_DIR}/artifacts/llamaindex_rag_store/docstore.json" ]]; then
  echo "[init] index not found, building once"
  "${VENV_DIR}/bin/python" prototypes/llamaindex_rag/build_index.py
else
  echo "[init] index exists, skip rebuild"
fi

echo "[start] launching Python RAG on :${RAG_PORT}"
HK_INSURANCE_RAG_PORT="${RAG_PORT}" \
  "${VENV_DIR}/bin/python" prototypes/llamaindex_rag/serve.py >"${RAG_LOG}" 2>&1 &
RAG_PID=$!
echo "${RAG_PID}" > "${RAG_PID_FILE}"

sleep 1
if ! kill -0 "${RAG_PID}" >/dev/null 2>&1; then
  echo "Error: Python RAG failed to start. See ${RAG_LOG}"
  exit 1
fi

echo "[start] launching Go backend on :${BACKEND_PORT}"
PORT="${BACKEND_PORT}" \
PYTHON_RAG_BASE_URL="http://127.0.0.1:${RAG_PORT}" \
go run ./cmd/server >"${BACKEND_LOG}" 2>&1 &
BACKEND_PID=$!
echo "${BACKEND_PID}" > "${BACKEND_PID_FILE}"

sleep 2
if ! kill -0 "${BACKEND_PID}" >/dev/null 2>&1; then
  echo "Error: Go backend failed to start. See ${BACKEND_LOG}"
  exit 1
fi

echo ""
echo "RAG stack started."
echo "Python RAG:  http://127.0.0.1:${RAG_PORT}"
echo "Go backend:  http://127.0.0.1:${BACKEND_PORT}"
echo "Test page:   http://127.0.0.1:${BACKEND_PORT}/rag-test"
echo ""
echo "Logs:"
echo "  ${RAG_LOG}"
echo "  ${BACKEND_LOG}"
echo ""
echo "Stop:"
echo "  kill \$(cat ${RAG_PID_FILE}) \$(cat ${BACKEND_PID_FILE})"
