# PetWell LlamaIndex RAG Prototype Handover

## 1. Current Status

This Python RAG prototype is no longer just a conceptual experiment. It can now run as a real local service and answer real queries against the normalized insurance corpus.

Current validated scope:

- Active providers:
  - `one_degree`
  - `bluecross`
  - `msig`
  - `prudential`
- `bolttech` files may still exist in the normalized corpus, but they are intentionally ignored for now.
- Current chunker version: `semantic-v9`
- Current persisted index size: `582` chunks

Current runnable entrypoints:

- `prototypes/llamaindex_rag/build_index.py`
- `prototypes/llamaindex_rag/query.py`
- `prototypes/llamaindex_rag/serve.py`
- `prototypes/llamaindex_rag/smoke_test.py`

## 2. RAG Architecture

### 2.1 Data Layer

Source corpus lives under:

- `assets/rag_normalized/hk_insurance/`

This prototype does not read raw PDFs directly. It reads normalized Markdown policy files that already contain structured content such as:

- headings
- clause anchors
- unit labels
- lists
- markdown tables
- provider / language / product metadata

### 2.2 Chunking Layer

Chunking is implemented in:

- `prototypes/llamaindex_rag/chunking.py`

This is intentionally not naive heading-only splitting.

Instead, the chunker uses a combination of:

- clause anchors
- unit types
- semantic body structure
- structured bullet lists
- markdown tables
- benefit / definition / exclusion / renewal / waiting-period patterns
- plan-limit line isolation

The main goal is to avoid:

- oversized chunks that contain too many unrelated facts
- undersized chunks that lose useful context
- mixed chunks that contain both coverage prose and numeric benefit limits in one block

`semantic-v9` specifically improved this by allowing one clause to split into:

- a prose / coverage chunk
- a `Plan A / Plan B` numeric limit chunk

This is important for insurance documents because many policy sections mix:

- what is covered
- how much is payable

If these stay fused, retrieval quality becomes worse.

### 2.3 Corpus Loading Layer

Corpus loading is implemented in:

- `prototypes/llamaindex_rag/corpus.py`

Responsibilities:

- walk normalized Markdown files
- ignore unsupported providers
- convert chunk records into LlamaIndex `Document` objects
- preserve metadata for downstream retrieval and reranking

### 2.4 Indexing Layer

Index build/runtime logic is implemented mainly in:

- `prototypes/llamaindex_rag/runtime.py`
- `prototypes/llamaindex_rag/build_index.py`

Responsibilities:

- configure embedding model
- configure reranker client
- configure answer model
- build `VectorStoreIndex`
- persist index artifacts to disk
- maintain prototype index metadata

Persisted store location:

- `artifacts/llamaindex_rag_store/`

Persisted metadata now includes:

- `chunker_version`
- `built_at_utc`
- `document_count`
- `chunk_size`
- `chunk_overlap`
- `data_path`
- `corpus_fingerprint`
- `source_markdown_file_count`
- `supported_provider_count`

This means the prototype can detect stale indexes when:

- Markdown corpus content changes
- chunk size changes
- chunk overlap changes
- data path changes

### 2.5 Retrieval Layer

Retrieval is implemented in:

- `prototypes/llamaindex_rag/runtime.py`

Current retrieval flow is not just plain vector search. It combines:

- vector retrieval
- lexical backfill
- provider-aware comparison backfill
- reranker-based reordering
- heuristic rerank bonuses / penalties
- answer-node selection

This is why the prototype behaves more like a task-oriented insurance retrieval system than a generic “embed and ask” demo.

### 2.6 Answer Layer

There are two answer paths:

1. Deterministic answer path
2. LLM fallback path

Deterministic path is preferred for high-frequency, policy-structured questions. This reduces hallucination risk and keeps source selection tighter.

Current deterministic answer coverage includes at least:

- waiting periods
- pre-existing-condition exclusions
- consultation coverage
- consultation limits
- generic benefit limits
- benefit-limit comparison across providers
- markdown-table lookups
- renewal / upgrade rules
- add-on age / eligibility rules

If deterministic extraction does not match, the system falls back to the configured LLM with retrieved context.

### 2.7 Service Layer

HTTP service is implemented in:

- `prototypes/llamaindex_rag/serve.py`

Current endpoints:

- `GET /healthz`
- `GET /readyz`
- `GET /capabilities`
- `GET /query`
- `POST /query`

Semantics:

- `/healthz`: lightweight liveness, does not force lazy index loading
- `/readyz`: indicates whether runtime/index is already loaded and query-ready
- `/capabilities`: reports supported providers/languages, source-count limits, and index freshness/build metadata
- `/query`: query interface for local integration

### 2.8 CLI / Payload Layer

Implemented in:

- `prototypes/llamaindex_rag/query.py`
- `prototypes/llamaindex_rag/payloads.py`
- `prototypes/llamaindex_rag/request_validation.py`

Responsibilities:

- validate provider
- validate language
- validate `max_sources`
- support human-readable CLI output
- support JSON output
- share payload-building logic between CLI and HTTP

## 3. Existing Features and Implemented Points

### 3.1 Provider Scope Control

Implemented:

- hard-valid provider list reduced to the four real providers
- invalid provider requests fail fast instead of silently returning weak retrieval

### 3.2 Semantic Chunking

Implemented:

- clause-aware splitting
- structured-list-aware splitting
- markdown-table-aware splitting
- plan-limit isolation
- benefit / exclusion / waiting-period / renewal / definition-aware chunk strategies

### 3.3 Retrieval Hardening

Implemented:

- lexical recovery when vector retrieval misses key clauses
- provider-aware comparison backfill
- better node selection for comparison questions
- table-specific retrieval support
- generic limit retrieval hardening
- exclusion label-aware retrieval improvements

### 3.4 Deterministic Answer Modes

Implemented:

- deterministic single-provider benefit limit answers
- deterministic comparison answers
- deterministic markdown-table answers
- deterministic exclusion / waiting-period / consult answers

This keeps common insurance questions grounded in extracted evidence instead of forcing every question through free-form generation.

### 3.5 Index Freshness

Implemented:

- corpus fingerprint stored with index metadata
- automatic stale-index detection
- index rebuild when corpus snapshot no longer matches persisted metadata

### 3.6 Build and Runtime Observability

Implemented:

- build metadata emitted after index build
- `/capabilities` exposes current index metadata
- health and readiness separated

### 3.7 Source Control in Responses

Implemented:

- `max_sources` for CLI JSON and HTTP query responses
- response payload includes:
  - `answer_mode`
  - `structured_answer`
  - `sources`
  - `processing_ms`

## 4. Testing Workflow

### 4.1 Unit / Regression Tests

Primary test file:

- `prototypes/llamaindex_rag/test_chunking.py`

This file covers more than chunking now. It includes regression coverage for:

- chunk splitting behavior
- metadata extraction
- benefit-limit logic
- exclusion logic
- markdown-table logic
- payload builders
- request validation
- capabilities payload
- index freshness logic
- runtime state semantics

Run:

```bash
./.venv-llamaindex/bin/python -m unittest prototypes.llamaindex_rag.test_chunking
```

Latest validated result:

- `130 tests OK`

### 4.2 Syntax / Import Validation

Run:

```bash
./.venv-llamaindex/bin/python -m py_compile prototypes/llamaindex_rag/*.py
```

Purpose:

- catch syntax errors
- catch import breakage
- ensure the prototype modules can at least compile cleanly

### 4.3 Index Build Validation

Run:

```bash
./.venv-llamaindex/bin/python prototypes/llamaindex_rag/build_index.py
```

Purpose:

- validate model configuration path
- validate corpus loading
- validate chunk generation
- validate embedding calls
- validate persistence
- validate index metadata emission

### 4.4 Smoke Test Validation

Run:

```bash
./.venv-llamaindex/bin/python prototypes/llamaindex_rag/smoke_test.py
```

Current smoke cases include:

- Prudential room-and-board limit lookup
- Blue Cross consultation coverage in Chinese
- Blue Cross vs Prudential consultation-limit comparison

Smoke test can also run through HTTP:

```bash
./.venv-llamaindex/bin/python prototypes/llamaindex_rag/smoke_test.py --via-http --base-url http://127.0.0.1:8098
```

### 4.5 Manual Service Validation

Start service:

```bash
./.venv-llamaindex/bin/python prototypes/llamaindex_rag/serve.py
```

Validate health:

```bash
curl "http://127.0.0.1:8098/healthz"
curl "http://127.0.0.1:8098/readyz"
curl "http://127.0.0.1:8098/capabilities"
```

Validate query:

```bash
curl -X POST "http://127.0.0.1:8098/query" \
  -H "Content-Type: application/json" \
  -d '{"question":"What is the annual limit for Prudential room and board?","provider":"prudential","language":"en","max_sources":1}'
```

## 5. Future Extension Directions

The prototype is already good enough for initial testing, but these are the most practical next directions if work resumes later.

### 5.1 Evaluation Harness

Highest-value next step for quality control:

- build a repeatable offline evaluation set
- define expected answer mode / source quality / answer quality
- compare retrieval and answer regressions between changes

Right now the system has strong unit coverage and smoke coverage, but not a full benchmark harness.

### 5.2 Better Corpus / Metadata Quality

Still one of the highest-leverage improvements:

- keep improving normalized Markdown quality
- improve metadata consistency
- make clause / unit labeling more uniform
- make table normalization more stable

For this project, better corpus quality often gives more value than adding more model tricks.

### 5.3 More Deterministic Answer Types

Potential expansion:

- deductible
- co-insurance
- reimbursement ratio
- more add-on conditions
- more multi-provider comparison types

This should only be added for high-frequency insurance question shapes, not as a shortcut for every query.

### 5.4 Better Retrieval Evaluation

Possible later work:

- retrieval recall analysis
- per-provider failure clustering
- failure-case tagging by query type
- explicit source-quality scoring

### 5.5 Service Hardening

Potential later work:

- explicit reload endpoint for local development
- request logging / structured logs
- timeout / retry observability
- better startup and warmup controls
- containerization / process supervision

### 5.6 Multi-turn / Session Behavior

Not currently implemented:

- chat memory
- session state
- query rewrite across turns

This should only be added if product scope really needs conversational behavior.

## 6. What Is Still Needed To Integrate With The Go Backend

This prototype is runnable, but it is not yet production-integrated into the Go backend.

### 6.1 Define the Integration Model

Need to choose one of these:

1. Keep Python as a sidecar / separate local service and let Go call it over HTTP
2. Port the proven Python logic back into Go
3. Keep only the indexing / retrieval logic in Python and use Go as the main API facade

For speed, the simplest near-term path is usually:

- Python service as sidecar
- Go backend calls `/query`

### 6.2 Stabilize Request/Response Contract

Go integration needs a frozen contract for:

- request schema
  - `question`
  - `provider`
  - `language`
  - `max_sources`
- response schema
  - `answer`
  - `answer_mode`
  - `structured_answer`
  - `sources`
  - `processing_ms`
  - validation errors

This contract is mostly present already, but it should be explicitly documented and treated as stable before Go depends on it.

### 6.3 Add Go-side Client / Adapter

Need:

- Go HTTP client wrapper
- timeout handling
- retry policy
- error mapping
- response parsing
- fallback behavior when Python service is unavailable

### 6.4 Decide Index Lifecycle Ownership

Need to define:

- who runs index rebuild
- when rebuild happens
- whether rebuild is manual or automated
- where persisted artifacts live in deployment
- how model/API credentials are injected

### 6.5 Add Production Guards

Before real backend rollout, need at least:

- structured logs
- request tracing
- service health monitoring
- alerting / failure visibility
- concurrency/load behavior check
- latency budget check

### 6.6 Add Real Evaluation Gate

Before Go production integration, it is better to have:

- fixed evaluation set
- pre-release regression check
- basic acceptance criteria for answer correctness and source quality

### 6.7 Align Disclaimer and Product Behavior

Because this is insurance-policy QA, backend integration should make product rules explicit:

- answers are reference-only
- final interpretation must defer to official insurer documents
- source display behavior should be consistent across clients

## 7. Recommended Near-Term Plan

If work resumes later, the most practical order is:

1. Use the current prototype for real manual testing and collect concrete bad cases
2. Build a small offline evaluation set from those bad cases
3. Freeze the HTTP contract
4. Add a thin Go adapter to call the Python service
5. Only then decide whether a Go rewrite is worth it

This keeps the project simple and avoids over-engineering before real failure cases are known.
