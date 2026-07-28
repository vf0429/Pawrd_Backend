from __future__ import annotations

from typing import Any

try:
    from .runtime import DISCLAIMER_ZH
except ImportError:
    from runtime import DISCLAIMER_ZH


def build_query_payload(
    *,
    question: str,
    provider: str | None,
    language: str | None,
    intent: str,
    result: Any,
    max_sources: int,
    processing_ms: int | None = None,
) -> dict[str, Any]:
    payload = {
        "question": question,
        "provider": provider,
        "language": language,
        "intent": intent,
        "answer": result.text,
        "answer_mode": result.mode,
        "structured_answer": result.structured,
        "disclaimer": DISCLAIMER_ZH,
        "sources": [
            build_source_payload(node)
            for node in result.nodes[:max_sources]
        ],
    }
    if processing_ms is not None:
        payload["processing_ms"] = processing_ms
    return payload


def build_source_payload(node: Any) -> dict[str, Any]:
    metadata = node.node.metadata
    return {
        "provider": metadata.get("provider"),
        "language": metadata.get("language"),
        "product": metadata.get("product"),
        "source_name": metadata.get("source_name"),
        "section_path": metadata.get("section_path"),
        "clauses": metadata.get("clauses"),
        "unit_types": metadata.get("unit_types"),
        "topic_tags": metadata.get("topic_tags"),
        "score": node.score,
        "snippet": node.node.text.strip(),
    }
