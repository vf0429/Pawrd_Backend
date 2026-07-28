# Normalized HK Insurance Corpus

This directory is the target location for the rebuilt PDF-first insurance corpus.

Rules:

- Do not overwrite legacy files under `assets/rag/hk_insurance/`.
- Use the schema defined in `docs/rag-rebuild/insurance_markdown_schema.md`.
- Current supported providers in this normalized corpus are `one_degree`, `bluecross`, `MSIG`, and `prudential`.
- `bolttech` is on hold and must not be treated as part of the current active rebuild scope.
- Keep one language per normalized file.
- Preserve source page anchors for every major semantic unit.
