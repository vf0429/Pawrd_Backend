# Go Migration Plan for Insurance RAG

## Objective

Migrate the current Python-centered insurance RAG runtime to a Go-native implementation without losing current answer stability.

Core requirement:

- migration is behavior-preserving first, optimization second

## Migration Strategy

Use staged migration, not big-bang rewrite.

Why:

- current stable behavior contains many retrieval and metadata details
- direct rewrite risks large quality regression
- staged parity checks allow controlled rollout

Recommended strategy:

1. keep Python as reference runtime
2. build Go shadow runtime in parallel
3. run parity validation on fixed query sets
4. switch traffic gradually after parity thresholds are met

## Target Go Architecture

## 1) Data Contracts

Define explicit shared contracts in Go for:

- normalized chunk record
- metadata schema
- retrieval candidate
- deterministic fact structures
- API request/response payloads

Requirement:

- contracts must match current app-facing compatibility needs (`/api/chat` response shape)

## 2) Chunking and Ingestion in Go

Rebuild semantic chunking logic in Go, preserving key behaviors:

- clause anchor extraction
- heading/path handling
- list-aware splitting
- markdown table-aware splitting
- plan-limit isolation
- metadata extraction for retrieval signals

Do not start from generic paragraph splitters; port policy-specific rules directly.

## 3) Index and Retrieval Runtime in Go

Implement Go equivalents for:

- embedding requests (OpenAI-compatible API)
- vector index build/load
- retrieval + lexical backfill
- provider-aware comparison backfill
- rerank requests
- candidate rerank/selection helpers

Persist metadata parity:

- `chunker_version`
- `data_path`
- `chunk_size`
- `chunk_overlap`
- `corpus_fingerprint`
- build timestamp and counts

## 4) Deterministic Answer Layer in Go

Port deterministic paths in priority order:

1. waiting periods
2. consultation coverage/limits
3. benefit limits (single + comparison)
4. exclusion and pre-existing patterns
5. markdown table lookups
6. renewal/upgrade and add-on rules

Rule:

- deterministic output format should be schema-equivalent to current Python structured responses

## 5) API Layer in Go

Keep current frontend continuity:

- preserve `/api/chat` compatibility shape
- keep source list and active provider behavior
- preserve error semantics where possible

Introduce internal versioned endpoints for migration testing:

- `/api/rag/go/query` (candidate runtime)
- keep `/api/chat` routed to Python until parity gate passes

## 6) Observability and Ops

Build first-class diagnostics into Go runtime:

- health/readiness
- index freshness state
- build metadata endpoint
- structured logs with request IDs
- retrieval-stage timing metrics

## Phased Execution Plan

## Phase 0: Freeze Baseline

Deliverables:

- fixed Python baseline commit hash
- frozen test query set (EN + ZH)
- expected outputs and source expectations

Acceptance:

- baseline suite reproducible locally and in CI

## Phase 1: Contract and Fixtures

Deliverables:

- Go data contracts
- fixture corpus samples
- parser/chunker golden tests

Acceptance:

- chunk output parity on selected fixture files
- metadata parity for required keys

## Phase 2: Retrieval Parity Core

Deliverables:

- Go index build/load
- retrieval + rerank + backfill pipeline
- retrieval-stage test harness

Acceptance:

- retrieval candidate overlap parity vs Python baseline on fixed query suite
- source ranking quality within agreed threshold

## Phase 3: Deterministic Answer Parity

Deliverables:

- Go deterministic extractors for priority intents
- structured-answer schema parity tests

Acceptance:

- answer mode parity on target intent set
- answer correctness parity threshold met

## Phase 4: Shadow Mode in Backend

Deliverables:

- run Go and Python RAG in parallel for same requests
- compare outputs and log diffs

Acceptance:

- sustained low-diff window over real traffic sample

## Phase 5: Controlled Cutover

Deliverables:

- feature flag to switch `/api/chat` from Python proxy to Go runtime
- rollback toggle

Acceptance:

- post-cutover quality and latency within target bounds
- no critical regression in monitored flows

## Phase 6: Decommission Python Runtime

Deliverables:

- remove Python serving dependency from critical path
- keep archived reference artifacts/docs

Acceptance:

- Go runtime fully owns insurance RAG path

## Parity and Quality Gates

Define explicit gates before cutover:

- answer correctness score on fixed suite
- source relevance score
- deterministic mode coverage rate
- latency budget (p50/p95)
- error rate budget

Minimum migration rule:

- do not cut over on “looks similar”; cut over only after measurable parity

## Risks and Mitigations

## Risk 1: Hidden behavior drift in chunking

Mitigation:

- golden tests on fixture markdown
- metadata key-level diff tests

## Risk 2: Retrieval regression despite same models

Mitigation:

- stage-by-stage retrieval diagnostics
- candidate overlap and rank comparison tooling

## Risk 3: Deterministic extractor mismatch

Mitigation:

- intent-specific parity tests with structured outputs

## Risk 4: Operational complexity during dual runtime

Mitigation:

- strict feature flags
- clear shadow vs active routing visibility

## Suggested Repository Workstreams

## Workstream A: Contracts and Tests

- introduce shared Go schema package
- add golden fixtures and parity harness

## Workstream B: Chunker Port

- implement parser + splitter + metadata extraction

## Workstream C: Retrieval Port

- embeddings, index, rerank, selection pipeline

## Workstream D: Deterministic Answers

- port high-priority answer extractors

## Workstream E: Backend Integration

- shadow routing, feature flags, observability

## Immediate Next Steps

1. lock Python baseline and generate fixed migration query suite
2. implement Go chunking fixtures and metadata golden tests
3. build Go retrieval parity harness before any API cutover

This order minimizes rework and preserves current product stability while moving to Go.
