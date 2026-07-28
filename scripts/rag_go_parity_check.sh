#!/usr/bin/env bash
set -euo pipefail

# Minimal parity checker between:
# - Python prototype: /query
# - Go translation skeleton: /api/rag/go/query
#
# Usage:
#   ./scripts/rag_go_parity_check.sh
# Optional overrides:
#   PY_RAG_URL=http://127.0.0.1:8098/query GO_RAG_URL=http://127.0.0.1:8012/api/rag/go/query ./scripts/rag_go_parity_check.sh

PY_RAG_URL="${PY_RAG_URL:-http://127.0.0.1:8098/query}"
GO_RAG_URL="${GO_RAG_URL:-http://127.0.0.1:8012/api/rag/go/query}"
OUT_FILE="${OUT_FILE:-/tmp/rag_go_parity_result.json}"

queries=(
  "What is the annual limit for Prudential room and board?"
  "Blue Cross 包唔包獸醫診症？"
  "Compare Blue Cross and Prudential veterinary consultation limits."
  "What is the meaning of waiting period?"
)

json_escape() {
  python3 - <<'PY' "$1"
import json,sys
print(json.dumps(sys.argv[1]))
PY
}

results='[]'
for q in "${queries[@]}"; do
  q_escaped="$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$q")"

  py_resp="$(curl -sS -G "$PY_RAG_URL" --data-urlencode "q=$q" --data-urlencode "max_sources=3")"
  go_resp="$(curl -sS -G "$GO_RAG_URL" --data-urlencode "q=$q" --data-urlencode "max_sources=3")"

  item="$(python3 - <<'PY' "$q" "$py_resp" "$go_resp"
import json,sys
q=sys.argv[1]
py_raw=sys.argv[2]
go_raw=sys.argv[3]
try:
    py=json.loads(py_raw)
except Exception:
    py={"_raw":py_raw}
try:
    go=json.loads(go_raw)
except Exception:
    go={"_raw":go_raw}
print(json.dumps({
  "question": q,
  "python": {
    "answer_mode": py.get("answer_mode"),
    "answer": py.get("answer"),
    "sources_count": len(py.get("sources") or []),
  },
  "go": {
    "answer_mode": go.get("answer_mode"),
    "answer": go.get("answer"),
    "sources_count": len(go.get("sources") or []),
  }
}, ensure_ascii=False))
PY
)"

  results="$(python3 - <<'PY' "$results" "$item"
import json,sys
a=json.loads(sys.argv[1])
b=json.loads(sys.argv[2])
a.append(b)
print(json.dumps(a, ensure_ascii=False))
PY
)"
done

echo "$results" | python3 -m json.tool > "$OUT_FILE"
echo "Parity snapshot written to: $OUT_FILE"
cat "$OUT_FILE"
