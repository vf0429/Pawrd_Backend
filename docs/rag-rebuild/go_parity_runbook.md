# Go RAG Parity Runbook

## Goal

Run Python and Go RAG endpoints side-by-side and get a repeatable parity snapshot.

## 1) Start services

Open one terminal:

```bash
cd /Users/vfzzz/Desktop/PetWell_Project/apps/Pawrd-Backend
./scripts/start_rag_stack.sh --restart
```

This starts:

- Python RAG on `127.0.0.1:8098`
- Go backend on `127.0.0.1:8012`

## 2) Verify endpoints

```bash
curl -sS http://127.0.0.1:8098/healthz
curl -sS http://127.0.0.1:8012/api/rag/go/healthz
curl -sS http://127.0.0.1:8012/api/rag/go/capabilities
```

## 3) Run parity comparison

```bash
cd /Users/vfzzz/Desktop/PetWell_Project/apps/Pawrd-Backend
go run ./cmd/rag_parity
```

Default compare targets:

- Python: `http://127.0.0.1:8098/query`
- Go: `http://127.0.0.1:8012/api/rag/go/query`

If needed, override:

```bash
PY_RAG_URL=http://127.0.0.1:8098/query \
GO_RAG_URL=http://127.0.0.1:8012/api/rag/go/query \
go run ./cmd/rag_parity
```

Optional parity thresholds:

```bash
MIN_GO_SOURCES=1 MAX_GO_SOURCES=6 go run ./cmd/rag_parity
```

The parity command exits non-zero if:

- Python/Go endpoint errors occur
- `answer_mode` does not match per case
- Go sources count is outside `[MIN_GO_SOURCES..MAX_GO_SOURCES]`
- Go answer text is empty

## 4) Run Go smoke checks (no Python dependency)

```bash
cd /Users/vfzzz/Desktop/PetWell_Project/apps/Pawrd-Backend
go run ./cmd/rag_smoke
```

Default target:

- Go: `http://127.0.0.1:8012/api/rag/go/query`

Override:

```bash
GO_RAG_URL=http://127.0.0.1:8012/api/rag/go/query go run ./cmd/rag_smoke
```

`rag_smoke` exits non-zero if any case fails expected mode / empty answer / zero sources.

## 5) Interpret output quickly

For each case, check:

- `python_mode`
- `go_mode`
- `mode_match`
- source counts

## 6) If parity tool reports connection errors

- `dial tcp 127.0.0.1:8098: connect: connection refused`
  - Python RAG not running
- `http 404` on Go URL
  - Go backend not running on expected port
  - or wrong `GO_RAG_URL`

## 7) Stop services

```bash
cd /Users/vfzzz/Desktop/PetWell_Project/apps/Pawrd-Backend
./scripts/start_rag_stack.sh --stop
```

## 8) `/api/chat` runtime switch (compat mode)

`/api/chat` keeps old response shape and can now switch backend runtime via env:

- `CHAT_RAG_RUNTIME=python` (default)
- `CHAT_RAG_RUNTIME=go`

Example (Go runtime on a separate test port):

```bash
CHAT_RAG_RUNTIME=go GO_RAG_BASE_URL=http://127.0.0.1:8012/api/rag/go PORT=8020 go run ./cmd/server
```

Then test:

```bash
curl -sS -X POST 'http://127.0.0.1:8020/api/chat' \
  -H 'Content-Type: application/json' \
  -d '{"query":"Blue Cross 包唔包獸醫診症？","model":"insurance","provider":"bluecross"}'
```
