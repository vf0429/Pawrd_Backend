from __future__ import annotations

import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

try:
    from .config import PrototypeConfig
    from .constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS
    from .payloads import build_query_payload
    from .request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )
    from .runtime import (
        answer_query,
        compute_corpus_fingerprint,
        configure_settings,
        detect_query_intent,
        ensure_index,
        intent_summary,
    )
except ImportError:
    from config import PrototypeConfig
    from constants import SUPPORTED_LANGUAGES, SUPPORTED_PROVIDERS
    from payloads import build_query_payload
    from request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )
    from runtime import (
        answer_query,
        compute_corpus_fingerprint,
        configure_settings,
        detect_query_intent,
        ensure_index,
        intent_summary,
    )


def read_index_metadata(persist_dir: Path) -> dict[str, object]:
    metadata_path = persist_dir / "prototype_index_meta.json"
    if not metadata_path.exists():
        return {}
    try:
        payload = json.loads(metadata_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return payload if isinstance(payload, dict) else {}


def build_capabilities_payload(cfg: PrototypeConfig, index_metadata: dict[str, object]) -> dict[str, object]:
    current_fingerprint = compute_corpus_fingerprint(cfg.data_path)
    stored_fingerprint = str(index_metadata.get("corpus_fingerprint", ""))
    index_fresh = bool(stored_fingerprint) and stored_fingerprint == current_fingerprint
    return {
        "ok": True,
        "service": "llamaindex_rag_prototype",
        "supported_providers": list(SUPPORTED_PROVIDERS),
        "supported_languages": list(SUPPORTED_LANGUAGES),
        "default_max_sources": cfg.answer_max_sources,
        "max_allowed_sources": cfg.answer_max_sources,
        "query_methods": ["GET", "POST"],
        "query_routes": {
            "healthz": "/healthz",
            "readyz": "/readyz",
            "capabilities": "/capabilities",
            "query_get": "/query?q=...&provider=...&language=...",
            "query_post": "/query",
        },
        "index": {
            "persist_dir": str(cfg.persist_dir),
            "built_at_utc": index_metadata.get("built_at_utc", ""),
            "is_fresh": index_fresh,
            "chunker_version": index_metadata.get("chunker_version", ""),
            "document_count": index_metadata.get("document_count", 0),
            "chunk_size": index_metadata.get("chunk_size", 0),
            "chunk_overlap": index_metadata.get("chunk_overlap", 0),
            "data_path": index_metadata.get("data_path", str(cfg.data_path)),
            "source_markdown_file_count": index_metadata.get("source_markdown_file_count", 0),
            "supported_provider_count": index_metadata.get("supported_provider_count", len(SUPPORTED_PROVIDERS)),
        },
    }


class RAGHandler(BaseHTTPRequestHandler):
    _runtime_state: dict[str, object] | None = None

    @classmethod
    def runtime_loaded(cls) -> bool:
        return cls._runtime_state is not None

    @classmethod
    def get_runtime_state(cls) -> dict[str, object]:
        if cls._runtime_state is None:
            cfg = PrototypeConfig.load()
            cfg.validate()
            configure_settings(cfg)
            index = ensure_index(cfg)
            cls._runtime_state = {
                "cfg": cfg,
                "index": index,
                "index_metadata": read_index_metadata(cfg.persist_dir),
            }
        return cls._runtime_state

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        if parsed.path == "/healthz":
            self.respond_json(
                200,
                {
                    "ok": True,
                    "service": "llamaindex_rag_prototype",
                    "runtime_loaded": self.runtime_loaded(),
                    "index_ready": self.runtime_loaded(),
                },
            )
            return
        if parsed.path == "/readyz":
            if not self.runtime_loaded():
                self.respond_json(
                    503,
                    {
                        "ok": False,
                        "service": "llamaindex_rag_prototype",
                        "runtime_loaded": False,
                        "index_ready": False,
                    },
                )
                return
            self.respond_json(
                200,
                {
                    "ok": True,
                    "service": "llamaindex_rag_prototype",
                    "runtime_loaded": True,
                    "index_ready": True,
                },
            )
            return
        if parsed.path == "/capabilities":
            state = self.get_runtime_state()
            self.respond_json(200, build_capabilities_payload(state["cfg"], state["index_metadata"]))
            return
        if parsed.path != "/query":
            self.respond_json(404, {"error": "not_found"})
            return

        params = parse_qs(parsed.query)
        question = first_param(params, "q")
        if not question:
            self.respond_json(400, {"error": "missing_question"})
            return

        try:
            provider = validate_provider(first_param(params, "provider"))
            language = validate_language(first_param(params, "language"))
            max_sources = validate_max_sources(
                first_param(params, "max_sources"),
                default_max_sources=self.get_runtime_state()["cfg"].answer_max_sources,
                max_allowed=self.get_runtime_state()["cfg"].answer_max_sources,
            )
        except RequestValidationError as exc:
            self.respond_json(
                400,
                build_validation_error_payload(
                    exc,
                    max_allowed_sources=self.get_runtime_state()["cfg"].answer_max_sources,
                ),
            )
            return
        self.handle_query(question=question, provider=provider, language=language, max_sources=max_sources)

    def do_POST(self) -> None:
        parsed = urlparse(self.path)
        if parsed.path != "/query":
            self.respond_json(404, {"error": "not_found"})
            return

        payload = self.read_json_body()
        if payload is None:
            return

        question = str(payload.get("question", "")).strip()
        if not question:
            self.respond_json(400, {"error": "missing_question"})
            return

        try:
            provider = validate_provider(payload.get("provider"))
            language = validate_language(payload.get("language"))
            max_sources = validate_max_sources(
                payload.get("max_sources"),
                default_max_sources=self.get_runtime_state()["cfg"].answer_max_sources,
                max_allowed=self.get_runtime_state()["cfg"].answer_max_sources,
            )
        except RequestValidationError as exc:
            self.respond_json(
                400,
                build_validation_error_payload(
                    exc,
                    max_allowed_sources=self.get_runtime_state()["cfg"].answer_max_sources,
                ),
            )
            return
        self.handle_query(question=question, provider=provider, language=language, max_sources=max_sources)

    def handle_query(
        self,
        *,
        question: str,
        provider: str | None,
        language: str | None,
        max_sources: int,
    ) -> None:
        state = self.get_runtime_state()
        intent = detect_query_intent(question)
        started_at = time.perf_counter()
        result = answer_query(
            cfg=state["cfg"],
            index=state["index"],
            question=question,
            provider=provider,
            language=language,
        )
        processing_ms = max(0, round((time.perf_counter() - started_at) * 1000))
        self.respond_json(
            200,
            build_query_payload(
                question=question,
                provider=provider,
                language=language,
                intent=intent_summary(intent),
                result=result,
                max_sources=max_sources,
                processing_ms=processing_ms,
            ),
        )

    def read_json_body(self) -> dict | None:
        raw_length = self.headers.get("Content-Length", "").strip()
        if not raw_length:
            self.respond_json(400, {"error": "missing_body"})
            return None
        try:
            content_length = int(raw_length)
        except ValueError:
            self.respond_json(400, {"error": "invalid_content_length"})
            return None
        if content_length <= 0:
            self.respond_json(400, {"error": "missing_body"})
            return None

        raw_body = self.rfile.read(content_length)
        try:
            payload = json.loads(raw_body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self.respond_json(400, {"error": "invalid_json"})
            return None
        if not isinstance(payload, dict):
            self.respond_json(400, {"error": "invalid_json_object"})
            return None
        return payload

    def log_message(self, format: str, *args) -> None:
        return

    def respond_json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def first_param(params: dict[str, list[str]], key: str) -> str | None:
    values = params.get(key) or []
    for value in values:
        value = value.strip()
        if value:
            return value
    return None


def main() -> None:
    port = int(os.getenv("HK_INSURANCE_RAG_PORT", "8098"))
    server = ThreadingHTTPServer(("127.0.0.1", port), RAGHandler)
    print(f"Serving PetWell LlamaIndex RAG prototype on http://127.0.0.1:{port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
