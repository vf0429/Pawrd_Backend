from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from dotenv import load_dotenv


ROOT = Path(__file__).resolve().parents[2]
load_dotenv(ROOT / ".env")


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError:
        return default
    return value if value > 0 else default


def _env_bool(name: str, default: bool) -> bool:
    raw = os.getenv(name, "").strip().lower()
    if not raw:
        return default
    if raw in {"1", "true", "yes", "on"}:
        return True
    if raw in {"0", "false", "no", "off"}:
        return False
    return default


def _env_or_default(name: str, default: str) -> str:
    value = os.getenv(name, "").strip()
    return value or default


def _derive_openai_base_url(endpoint: str) -> str:
    endpoint = endpoint.strip().rstrip("/")
    if not endpoint:
        return ""
    for suffix in ("/chat/completions", "/completions"):
        if endpoint.endswith(suffix):
            return endpoint.removesuffix(suffix)
    return endpoint


@dataclass
class PrototypeConfig:
    data_path: Path
    persist_dir: Path
    top_k: int
    chunk_size: int
    chunk_overlap: int
    initial_retrieval_k: int
    lexical_backfill_k: int
    answer_max_sources: int
    embedding_batch_size: int
    request_timeout_seconds: float
    request_max_retries: int
    embedding_base_url: str
    embedding_model: str
    embedding_api_key: str
    rerank_enabled: bool
    rerank_base_url: str
    rerank_model: str
    rerank_api_key: str
    rerank_top_n: int
    rerank_instruction: str
    llm_base_url: str
    llm_model: str
    llm_api_key: str

    @classmethod
    def load(cls) -> "PrototypeConfig":
        report_agent_endpoint = os.getenv("REPORT_AGENT_1_ENDPOINT", "").strip()
        report_agent_api_key = os.getenv("REPORT_AGENT_1_API_KEY", "").strip()
        report_agent_base_url = _derive_openai_base_url(report_agent_endpoint) or "https://api.siliconflow.cn/v1"

        configured_data_path = Path(
            _env_or_default("HK_INSURANCE_RAG_DATA_PATH", "assets/rag_normalized/hk_insurance")
        )
        data_path = configured_data_path
        if not data_path.is_absolute():
            data_path = ROOT / data_path

        normalized_fallback = ROOT / "assets" / "rag_normalized" / "hk_insurance"
        if not data_path.exists() and normalized_fallback.exists():
            data_path = normalized_fallback

        persist_dir = ROOT / "artifacts" / "llamaindex_rag_store"

        return cls(
            data_path=data_path,
            persist_dir=persist_dir,
            top_k=_env_int("HK_INSURANCE_RAG_TOP_K", 6),
            chunk_size=_env_int("LLAMAINDEX_RAG_CHUNK_SIZE", 700),
            chunk_overlap=_env_int("LLAMAINDEX_RAG_CHUNK_OVERLAP", 120),
            initial_retrieval_k=_env_int("HK_INSURANCE_RAG_INITIAL_RETRIEVAL_K", 24),
            lexical_backfill_k=_env_int("HK_INSURANCE_RAG_LEXICAL_BACKFILL_K", 12),
            answer_max_sources=_env_int("HK_INSURANCE_RAG_MAX_SOURCES", 6),
            embedding_batch_size=_env_int("HK_INSURANCE_RAG_EMBEDDING_BATCH_SIZE", 16),
            request_timeout_seconds=_env_float("HK_INSURANCE_RAG_REQUEST_TIMEOUT_SECONDS", 120.0),
            request_max_retries=_env_int("HK_INSURANCE_RAG_REQUEST_MAX_RETRIES", 3),
            embedding_base_url=_env_or_default("HK_INSURANCE_RAG_EMBEDDING_BASE_URL", report_agent_base_url),
            embedding_model=_env_or_default(
                "HK_INSURANCE_RAG_EMBEDDING_MODEL",
                "Qwen/Qwen3-Embedding-8B",
            ),
            embedding_api_key=_env_or_default("HK_INSURANCE_RAG_EMBEDDING_API_KEY", report_agent_api_key),
            rerank_enabled=_env_bool("HK_INSURANCE_RAG_RERANK_ENABLED", True),
            rerank_base_url=_env_or_default("HK_INSURANCE_RAG_RERANK_BASE_URL", report_agent_base_url),
            rerank_model=_env_or_default("HK_INSURANCE_RAG_RERANK_MODEL", "BAAI/bge-reranker-v2-m3"),
            rerank_api_key=_env_or_default("HK_INSURANCE_RAG_RERANK_API_KEY", report_agent_api_key),
            rerank_top_n=_env_int("HK_INSURANCE_RAG_RERANK_TOP_N", 6),
            rerank_instruction=_env_or_default(
                "HK_INSURANCE_RAG_RERANK_INSTRUCTION",
                (
                    "Rank the insurance policy passages by how directly they answer the user question. "
                    "Prefer the clause with the most direct rule, waiting period, eligibility requirement, "
                    "coverage statement, exclusion, or payout condition over related add-on or nearby clauses."
                ),
            ),
            llm_base_url=_env_or_default(
                "HK_INSURANCE_RAG_LLM_BASE_URL",
                "https://api.stepfun.com/step_plan/v1",
            ),
            llm_model=_env_or_default("HK_INSURANCE_RAG_LLM_MODEL", "step-3.5-flash"),
            llm_api_key=os.getenv("HK_INSURANCE_RAG_LLM_API_KEY", "").strip(),
        )

    def validate(self) -> None:
        missing = []
        if not self.data_path.exists():
            missing.append(f"data_path not found: {self.data_path}")
        if not self.embedding_api_key:
            missing.append("HK_INSURANCE_RAG_EMBEDDING_API_KEY is empty")
        if not self.llm_api_key:
            missing.append("HK_INSURANCE_RAG_LLM_API_KEY is empty")
        if missing:
            raise RuntimeError("\n".join(missing))
