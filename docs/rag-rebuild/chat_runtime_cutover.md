# Chat Runtime Cutover (Python -> Go)

## Purpose

Keep `/api/chat` request/response compatibility while allowing controlled runtime switch between:

- Python prototype RAG
- Go direct-translation RAG

## New runtime switch

Environment variable:

- `CHAT_RAG_RUNTIME`
  - `go` (default)
  - `python`

## Related config

- `PYTHON_RAG_BASE_URL` (default `http://127.0.0.1:8098`)
- `PYTHON_RAG_TIMEOUT_SECONDS` (default `90`)
- `GO_RAG_BASE_URL` (default `http://127.0.0.1:8012/api/rag/go`)
- `GO_RAG_TIMEOUT_SECONDS` (default `90`)

## Behavior

- `/api/chat` keeps the same payload contract.
- Runtime routing is internal; frontend callers do not need to change.
- Invalid provider handling remains `400` (compatibility with prior path).
- If runtime is set to `python` but Python service is unavailable, backend auto-falls back to Go runtime for availability.

## Local verification

1. Start stack:

```bash
./scripts/start_rag_stack.sh --restart
```

2. Keep Python runtime:

```bash
CHAT_RAG_RUNTIME=python go run ./cmd/server
```

3. Switch to Go runtime:

```bash
CHAT_RAG_RUNTIME=go go run ./cmd/server
```

4. Call compatibility endpoint:

```bash
curl -sS -X POST 'http://127.0.0.1:8012/api/chat' \
  -H 'Content-Type: application/json' \
  -d '{"query":"Blue Cross 包唔包獸醫診症？","model":"insurance","provider":"bluecross"}'
```

## Rollback

Set:

```bash
CHAT_RAG_RUNTIME=python
```

No frontend rollback needed because API shape is unchanged.
