# Current RAG Status

This document is the current boundary for the rebuilt HK insurance RAG work.

## Active RAG Scope

The old Go RAG implementation has been removed from the backend codebase.

The current RAG rebuild work only keeps:

- `prototypes/llamaindex_rag/`
- `assets/rag_normalized/hk_insurance/`
- `docs/rag-rebuild/`

## Supported Providers

Current supported providers for the rebuilt corpus are:

- `one_degree`
- `bluecross`
- `MSIG`
- `prudential`

`bolttech` is intentionally on hold and must not be treated as part of the current supported query scope.

## Answering Rule

Every RAG answer must include a disclaimer in substance equivalent to:

- the answer is for reference only
- it is not guaranteed to be 100% accurate, complete, or up to date
- the final position must be checked against the insurer's official website, formal policy wording, policy schedule, endorsements, and latest written confirmation

## Project Phase

The project is currently in the corpus-rebuild stage, not the retrieval-tuning stage.

Completed or mostly completed:

- normalized markdown schema
- standardized corpus rebuild for the currently supported providers
- provider-by-provider semantic proofreading

Not yet the main focus:

- provider-aware chunking rollout
- final production RAG integration

## Source of Truth

Use these files as the main status references:

- `docs/rag-rebuild/insurance_markdown_schema.md`
- `docs/rag-rebuild/provider_rebuild_plan.md`
- `docs/rag-rebuild/manual_review_master_checklist.md`
