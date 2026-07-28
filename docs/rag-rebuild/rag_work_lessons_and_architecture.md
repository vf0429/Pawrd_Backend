# RAG Work Notes: Lessons Learned and Current Architecture

## Scope and Goal

This document summarizes what was learned while rebuilding the insurance RAG path, and what the current architecture actually looks like in production-like local runs.

Target achieved in this phase:

- move away from fragile legacy Go RAG flow
- stand up a realistic Python prototype path with real model calls
- stabilize behavior on high-frequency insurance query shapes
- keep provider scope intentionally narrow to 4 active providers

Active provider scope in this phase:

- `one_degree`
- `bluecross`
- `msig`
- `prudential`

## Trial-and-Error Lessons

## 1) Chunk quality matters more than model swapping

Early intuition was to improve answers mainly by changing model names or reranker settings.  
The strongest gains came from chunk design and metadata, not from swapping base models.

What worked:

- semantic chunking from markdown structure, not heading-only split
- preserving clause anchors and unit labels
- splitting mixed coverage prose and numeric limits into separate chunks
- table-aware chunking for markdown table blocks

What did not work well:

- naive heading-level chunking
- oversized mixed chunks containing multiple unrelated facts
- chunks without clear clause/unit metadata

## 2) Metadata is a first-class retrieval signal

Performance improved when retrieval/rerank consumed metadata signals explicitly.

High-value metadata examples:

- `provider`, `language`, `source_name`
- `clauses`, `unit_types`, `topic_tags`
- table metadata (`table_headers`, `table_row_labels`)
- plan-limit metadata (`has_plan_limit_lines`, `plan_limit_component_kind`)
- benefit labels and alias expansion

Practical takeaway:

- if markdown normalization and metadata quality are poor, model quality gains are capped
- metadata consistency is a core data-engineering problem, not a prompt-only problem

## 3) Deterministic extraction before LLM fallback is worth it

A deterministic first-pass was added for repeated insurance question shapes.

This reduced:

- hallucination risk
- unstable source selection
- answer variance between runs

Current deterministic coverage includes:

- waiting periods
- pre-existing condition exclusions
- consultation coverage / consultation limits
- benefit limits and comparison
- markdown table lookups
- renewal/upgrade and add-on eligibility patterns

Practical takeaway:

- keep deterministic paths for frequent policy question types
- fall back to LLM only when no reliable deterministic pattern matches

## 4) Retrieval flow must be layered, not single-shot

Pure vector top-k was not reliable enough on policy language and bilingual queries.

Current retrieval reliability improved by combining:

- vector retrieval
- lexical backfill
- provider-aware comparison backfill
- model rerank
- heuristic rerank/selection logic

Practical takeaway:

- policy RAG quality depends on retrieval pipeline design, not only embeddings

## 5) Observability and index freshness are required

Two recurring failure modes were stale indexes and unknown runtime state.

Fixes that helped:

- persisted index metadata with corpus fingerprint
- automatic stale-index detection
- capabilities endpoint exposing freshness/build metadata
- separated liveness and readiness checks

Practical takeaway:

- RAG runtime needs deployment observability like any other service

## Current Architecture (Implemented)

## 1) Data and Normalization

Input corpus:

- `assets/rag_normalized/hk_insurance/`

This layer is the canonical source for chunking and indexing in the current path.

## 2) Chunking Layer

Implementation:

- `prototypes/llamaindex_rag/chunking.py`

Key properties:

- semantic, clause-aware chunking
- list/table-aware splitting
- plan-limit isolation
- metadata extraction for retrieval and rerank signals

## 3) Index Build and Runtime Layer

Implementation:

- `prototypes/llamaindex_rag/build_index.py`
- `prototypes/llamaindex_rag/runtime.py`

Artifacts:

- persisted under `artifacts/llamaindex_rag_store/`

Freshness controls:

- chunker version checks
- data path checks
- chunk config checks
- corpus fingerprint checks

## 4) Query and API Layer

Implementation:

- `prototypes/llamaindex_rag/query.py`
- `prototypes/llamaindex_rag/serve.py`

HTTP endpoints:

- `/query` (GET and POST in prototype flow)
- `/capabilities`
- `/healthz`
- `/readyz`

## 5) Backend Compatibility Bridge

Implementation:

- `internal/handlers/chat_proxy.go`

Role:

- keep existing `/api/chat` app-facing shape
- proxy insurance chat requests to Python RAG `/query`
- preserve short-term compatibility for frontend testing

## 6) Test and Verification Layer

Implementation:

- `prototypes/llamaindex_rag/test_chunking.py`
- `prototypes/llamaindex_rag/smoke_test.py`

Verification model used:

- unit/regression coverage for chunking + retrieval helpers + request validation
- smoke checks for high-signal real queries
- runtime compile checks for Python modules

## What This Architecture Optimizes For

- fast iteration speed on RAG quality
- high observability for debugging
- stable app integration path without immediate frontend rewrite
- controlled provider scope and reduced drift

## Known Limits

- Python prototype is still the runtime core, not Go-native yet
- no full benchmark harness with scored acceptance thresholds yet
- no production-grade deployment SLOs documented yet
- medical/tool flows are not fully mirrored in the new insurance proxy path

## Recommended Near-Term Principles

- keep improving markdown and metadata quality before deep model changes
- gate behavior changes with regression tests and smoke checks
- keep deterministic patterns only where evidence is consistently reliable
- avoid hardcoded answer text; harden retrieval and metadata instead
