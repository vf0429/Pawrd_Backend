# PetWell LlamaIndex RAG Prototype

This prototype is a parallel learning path for PetWell's insurance RAG. It is scoped to the rebuilt normalized corpus under `assets/rag_normalized/hk_insurance/` and reuses the same `HK_INSURANCE_RAG_*` environment variables where possible.

## Goal

Use a minimal LlamaIndex pipeline to understand the core RAG flow clearly:

1. load normalized markdown policy documents
2. chunk documents
3. build embeddings
4. retrieve relevant chunks
5. generate an answer with cited source chunks

## Files

- `requirements.txt`: Python dependencies
- `config.py`: environment-backed config
- `corpus.py`: normalized markdown loader and metadata tagging
- `chunking.py`: semantic markdown chunker for clause-aware pre-chunks
- `runtime.py`: LlamaIndex setup and persistence helpers
- `build_index.py`: build and persist the vector index
- `query.py`: run a question against the prototype
- `smoke_test.py`: run a few high-signal real-query smoke checks
- `serve.py`: minimal HTTP entrypoint for local service-style validation

## Setup

From `Pawrd-Backend/`:

```bash
python3 -m venv .venv-llamaindex
source .venv-llamaindex/bin/activate
pip install -r prototypes/llamaindex_rag/requirements.txt
```

This prototype reads:

- `HK_INSURANCE_RAG_DATA_PATH`
- `HK_INSURANCE_RAG_EMBEDDING_BASE_URL`
- `HK_INSURANCE_RAG_EMBEDDING_MODEL`
- `HK_INSURANCE_RAG_EMBEDDING_API_KEY`
- `HK_INSURANCE_RAG_RERANK_ENABLED`
- `HK_INSURANCE_RAG_RERANK_BASE_URL`
- `HK_INSURANCE_RAG_RERANK_MODEL`
- `HK_INSURANCE_RAG_RERANK_API_KEY`
- `HK_INSURANCE_RAG_RERANK_TOP_N`
- `HK_INSURANCE_RAG_LLM_BASE_URL`
- `HK_INSURANCE_RAG_LLM_MODEL`
- `HK_INSURANCE_RAG_LLM_API_KEY`
- `HK_INSURANCE_RAG_TOP_K`

Optional prototype-only variables:

- `LLAMAINDEX_RAG_CHUNK_SIZE`
- `LLAMAINDEX_RAG_CHUNK_OVERLAP`
- `HK_INSURANCE_RAG_EMBEDDING_BATCH_SIZE`
- `HK_INSURANCE_RAG_REQUEST_TIMEOUT_SECONDS`
- `HK_INSURANCE_RAG_REQUEST_MAX_RETRIES`

## Build the index

```bash
python prototypes/llamaindex_rag/build_index.py
```

The persisted store is written to:

```text
artifacts/llamaindex_rag_store/
```

## Run as a local service

```bash
python prototypes/llamaindex_rag/serve.py
```

Use `HK_INSURANCE_RAG_PORT` to avoid collisions with old local processes:

```bash
HK_INSURANCE_RAG_PORT=8102 python prototypes/llamaindex_rag/serve.py
```

Then query it over HTTP:

```bash
curl "http://127.0.0.1:8098/query?q=OneDegree%20%E7%9A%84%E7%99%8C%E7%97%87%E7%AD%89%E5%80%99%E6%9C%9F%E6%98%AF%E5%B9%BE%E5%A4%9A%E6%97%A5%EF%BC%9F&provider=one_degree&language=zh"
```

Inspect current service capabilities:

```bash
curl "http://127.0.0.1:8098/capabilities"
```

The capabilities payload includes `default_max_sources` and `max_allowed_sources` so callers can discover the source-count limit before issuing queries.
It now also exposes index freshness/build metadata, including whether the persisted index matches the current normalized corpus snapshot.

Health/readiness endpoints:

```bash
curl "http://127.0.0.1:8098/healthz"
curl "http://127.0.0.1:8098/readyz"
```

- `/healthz` is lightweight liveness and does not force index initialization
- `/readyz` reflects whether this process has already loaded a ready index/runtime

Or query it with JSON over `POST /query`:

```bash
curl -X POST "http://127.0.0.1:8098/query" \
  -H "Content-Type: application/json" \
  -d '{"question":"What is the annual limit for Prudential room and board?","provider":"prudential","language":"en","max_sources":1}'
```

## Query examples

Single provider:

```bash
python prototypes/llamaindex_rag/query.py "Blue Cross waiting period for injury?" --provider bluecross --language en
```

Single provider with JSON output:

```bash
python prototypes/llamaindex_rag/query.py "What is the annual limit for Prudential room and board?" --provider prudential --language en --json
```

Single provider with one returned source snippet:

```bash
python prototypes/llamaindex_rag/query.py "What is the annual limit for Prudential room and board?" --provider prudential --language en --json --max-sources 1
```

Invalid providers now fail fast instead of silently returning weak/noisy results. The current hard-valid provider set is:

```text
one_degree
bluecross
msig
prudential
```

Chinese multi-provider style question without filters:

```bash
python prototypes/llamaindex_rag/query.py "OneDegree 的癌症等候期是幾多日？"
```

Smoke test:

```bash
python prototypes/llamaindex_rag/smoke_test.py
```

Run only one named case:

```bash
python prototypes/llamaindex_rag/smoke_test.py --case bluecross_consult_coverage_zh
```

Run smoke tests through the HTTP service instead of direct runtime calls:

```bash
python prototypes/llamaindex_rag/smoke_test.py --via-http --base-url http://127.0.0.1:8098
```

In HTTP mode, the smoke test now checks `/capabilities` first before validating `/query`.

## What to learn from this prototype

- how metadata filters change retrieval results
- how chunk size and overlap affect source quality
- how semantic pre-chunking differs from naive heading-only splitting
- how source nodes differ from the current Go fallback-based path
- how vector retrieval, model reranking, and heuristic tie-breaking interact
- how deterministic evidence extraction can sit in front of the LLM for high-frequency policy questions

## Current runtime behavior

- semantic markdown pre-chunking happens before LlamaIndex indexing
- the current verified chunker build is `semantic-v9`
- current persisted prototype index metadata:
  - `chunker_version=semantic-v9`
  - `document_count=582`
- retrieval combines vector search, lexical backfill, provider-aware comparison backfill, model rerank, and heuristic rerank
- deterministic answer paths now cover several high-frequency policy questions before falling back to the LLM:
  - standard and add-on waiting periods
  - pre-existing-condition exclusions
  - consultation coverage and consultation limits
  - generic benefit limits
  - renewal and upgrade rules
  - add-on age limits and add-on eligibility conditions
- `/query` HTTP responses include:
  - `answer_mode`
  - `structured_answer` when a deterministic path was used
  - `processing_ms`
  - cited source metadata for inspection
- persisted index metadata now includes a corpus fingerprint, so the prototype will rebuild automatically when normalized markdown files change even if the chunker version stays the same

## Current verified scope

- active provider scope is exactly:
  - `one_degree`
  - `bluecross`
  - `msig`
  - `prudential`
- `bolttech` files may still exist in `assets/rag_normalized/hk_insurance/`, but they are intentionally ignored by the prototype chunk loader for now
- this prototype has been live-verified against at least these query shapes:
  - consultation coverage
  - consultation limit comparison
  - generic benefit-limit lookup
  - waiting periods
  - exclusions
  - markdown-table lookups

## Current chunking direction

- chunking is intentionally not naive heading-only splitting
- the chunker uses clause anchors, unit types, structured lists, markdown tables, and semantic body components to decide split points
- `semantic-v9` adds plan-limit isolation so a clause can split into:
  - a coverage/prose chunk
  - a numeric `Plan A / Plan B` limit chunk
- this matters because many insurance clauses mix “what is covered” and “how much is payable” in one section, which hurts retrieval quality if they stay fused into a single chunk

## Important limits

- reranking is query-time only; there is no provider-tuned evaluation harness yet
- no standalone-query rewrite yet
- no multi-turn chat memory yet
- every answer must include a disclaimer that it is for reference only and final interpretation must be checked against official insurer documents

## What is still missing before Go-backend productionization

- an evaluation harness with repeatable offline regression cases instead of only ad hoc live probes
- a stable Go-side integration contract for:
  - request schema
  - answer-mode schema
  - structured-answer schema
  - source metadata schema
- a production data-refresh pipeline for normalized markdown updates, index rebuilds, version tracking, and rollback
- observability for retrieval quality and failure modes:
  - answer-mode distribution
  - deterministic vs LLM fallback rate
  - provider-level miss / timeout / rerank failure rates
- production fallback behavior when embeddings, reranker, or LLM calls fail or time out
- deployment and lifecycle decisions:
  - whether Python stays as a sidecar service
  - whether retrieval logic is ported into Go
  - how model credentials and index artifacts are managed in runtime environments

That is intentional. The point is to first make the baseline retrieval path real and inspectable before layering full PetWell production routing logic back in.
