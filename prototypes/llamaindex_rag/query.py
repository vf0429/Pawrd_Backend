from __future__ import annotations

import argparse
import json
import sys
import time

try:
    from .config import PrototypeConfig
    from .payloads import build_query_payload
    from .request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )
    from .runtime import DISCLAIMER_ZH, answer_query, configure_settings, detect_query_intent, ensure_index, intent_summary
except ImportError:
    from config import PrototypeConfig
    from payloads import build_query_payload
    from request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )
    from runtime import DISCLAIMER_ZH, answer_query, configure_settings, detect_query_intent, ensure_index, intent_summary


def main() -> None:
    parser = argparse.ArgumentParser(description="Query the PetWell LlamaIndex RAG prototype")
    parser.add_argument("question", help="Question to ask")
    parser.add_argument("--provider", help="Optional provider filter, e.g. bluecross")
    parser.add_argument("--language", help="Optional language filter, e.g. en or zh")
    parser.add_argument("--max-sources", help="Optional number of source snippets to return in JSON/text output")
    parser.add_argument("--json", action="store_true", help="Print a JSON payload instead of human-readable text")
    parser.add_argument("--debug-chunks", action="store_true", help="Print chunk metadata for retrieved nodes")
    args = parser.parse_args()

    cfg = PrototypeConfig.load()

    try:
        provider = validate_provider(args.provider)
        language = validate_language(args.language)
        max_sources = validate_max_sources(
            args.max_sources,
            default_max_sources=cfg.answer_max_sources,
            max_allowed=cfg.answer_max_sources,
        )
    except RequestValidationError as exc:
        if args.json:
            print(
                json.dumps(
                    build_validation_error_payload(exc, max_allowed_sources=cfg.answer_max_sources),
                    ensure_ascii=False,
                    indent=2,
                )
            )
            raise SystemExit(2)
        print(f"error: {exc.message}", file=sys.stderr)
        raise SystemExit(2)

    cfg.validate()
    configure_settings(cfg)
    index = ensure_index(cfg)
    intent = detect_query_intent(args.question)
    started_at = time.perf_counter()
    result = answer_query(
        cfg=cfg,
        index=index,
        question=args.question,
        provider=provider,
        language=language,
    )
    processing_ms = max(0, round((time.perf_counter() - started_at) * 1000))
    payload = build_query_payload(
        question=args.question,
        provider=provider,
        language=language,
        intent=intent_summary(intent),
        result=result,
        max_sources=max_sources,
        processing_ms=processing_ms,
    )

    if args.json:
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return

    print("\n=== Answer ===\n")
    print(result.text)
    print(f"\n免责声明：{DISCLAIMER_ZH}")
    print(f"\n=== Answer Mode ===\n\n{result.mode}")
    print(f"\n=== Intent ===\n\n{intent_summary(intent)}")

    print("\n=== Sources ===\n")
    for i, node in enumerate(result.nodes[: max_sources], start=1):
        meta = node.node.metadata
        print(
            f"[{i}] provider={meta.get('provider')} language={meta.get('language')} "
            f"product={meta.get('product')} source={meta.get('source_name')} score={node.score}"
        )
        if args.debug_chunks:
            print(
                "    "
                + f"clauses={meta.get('clauses', '')} "
                + f"unit_types={meta.get('unit_types', '')} "
                + f"section_path={meta.get('section_path', '')} "
                + f"token_estimate={meta.get('token_estimate', '')}"
            )
        snippet = node.node.text.strip().replace("\n", " ")
        print(snippet[:260])
        print()


if __name__ == "__main__":
    main()
