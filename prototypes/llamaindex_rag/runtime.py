from __future__ import annotations

import json
import re
import shutil
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, List

from openai import OpenAI as OpenAIClient

from llama_index.core import Settings, StorageContext, VectorStoreIndex, load_index_from_storage
from llama_index.core.base.embeddings.base import BaseEmbedding
from llama_index.core.bridge.pydantic import Field, PrivateAttr
from llama_index.core.llms import CompletionResponse, LLMMetadata
from llama_index.core.llms.callbacks import llm_completion_callback
from llama_index.core.llms.custom import CustomLLM
from llama_index.core.node_parser import SentenceSplitter
from llama_index.core.schema import NodeWithScore
from llama_index.core.vector_stores import ExactMatchFilter, MetadataFilters

try:
    from .config import PrototypeConfig
    from .chunking import CHUNKER_VERSION, parse_markdown_tables
    from .corpus import load_documents
except ImportError:
    from config import PrototypeConfig
    from chunking import CHUNKER_VERSION, parse_markdown_tables
    from corpus import load_documents

INDEX_METADATA_FILENAME = "prototype_index_meta.json"
DISCLAIMER_ZH = (
    "仅供参考，不保证 100% 准确、完整或最新。"
    "最终以保险公司官网、正式保单、承保表、批单及最新书面说明为准。"
)

PROVIDER_ALIASES: dict[str, tuple[str, ...]] = {
    "one_degree": ("one degree", "onedegree", "one_degree"),
    "bluecross": ("blue cross", "bluecross", "藍十字", "蓝十字"),
    "prudential": ("prudential", "保誠", "保诚"),
    "msig": ("msig",),
}

PROVIDER_DISPLAY_NAMES: dict[str, str] = {
    "one_degree": "OneDegree",
    "bluecross": "Blue Cross",
    "prudential": "Prudential",
    "msig": "MSIG",
}


@dataclass(frozen=True)
class QueryIntent:
    raw_question: str
    normalized_question: str
    wants_waiting_period: bool
    wants_coverage: bool
    wants_exclusion: bool
    wants_pre_existing: bool
    wants_comparison: bool
    wants_cancer: bool
    wants_injury: bool
    wants_consult: bool
    asks_limit: bool
    asks_cost_share: bool
    asks_renewal: bool
    asks_upgrade: bool
    asks_cash_benefit: bool
    asks_addon_benefit: bool
    asks_chronic_condition: bool
    asks_age_limit: bool
    asks_eligibility: bool
    target_providers: tuple[str, ...]


@dataclass(frozen=True)
class WaitingPeriodFact:
    provider: str
    clauses: str
    source_name: str
    cancer_days: int | None = None
    injury_days: int | None = None
    illness_days: int | None = None
    general_days: int | None = None
    no_waiting_period: bool = False


@dataclass(frozen=True)
class PreExistingFact:
    provider: str
    clauses: str
    source_name: str
    excluded: bool
    definition_like: bool


@dataclass(frozen=True)
class ExclusionItemFact:
    provider: str
    clauses: str
    source_name: str
    excluded: bool
    matched_label: str


@dataclass(frozen=True)
class ConsultCoverageFact:
    provider: str
    clauses: str
    source_name: str
    covered: bool
    consultation_label: str


@dataclass(frozen=True)
class ConsultLimitFact:
    provider: str
    clauses: str
    source_name: str
    consultation_label: str
    plan_limits: dict[str, dict[str, str]]
    has_explicit_limit: bool


@dataclass(frozen=True)
class BenefitLimitFact:
    provider: str
    clauses: str
    source_name: str
    benefit_label: str
    plan_limits: dict[str, dict[str, str]]
    has_explicit_limit: bool


@dataclass(frozen=True)
class MarkdownTableFact:
    provider: str
    clauses: str
    source_name: str
    row_label: str
    column_label: str
    value: str
    score: float = 0.0


@dataclass(frozen=True)
class RenewalRuleFact:
    provider: str
    clauses: str
    source_name: str
    auto_renew: bool | None = None
    guaranteed_age: int | None = None
    approval_age_threshold: int | None = None
    can_upgrade: bool | None = None
    upgrade_waiting_period: bool | None = None
    downgrade_no_waiting: bool | None = None
    age_upgrade_block: int | None = None


@dataclass(frozen=True)
class AddonWaitingPeriodFact:
    provider: str
    clauses: str
    source_name: str
    addon_label: str
    days: int | None


@dataclass(frozen=True)
class AddonEligibilityFact:
    provider: str
    clauses: str
    source_name: str
    addon_label: str
    min_age: int | None
    max_age: int | None
    renewal_cutoff_age: int | None = None
    renewal_no_age_limit: bool = False
    new_purchase_only: bool = False


@dataclass(frozen=True)
class AddonEligibilityConditionsFact:
    provider: str
    clauses: str
    source_name: str
    addon_label: str
    conditions: tuple[str, ...]


@dataclass(frozen=True)
class ChronicConditionRuleFact:
    provider: str
    clauses: str
    source_name: str
    rule_kind: str
    age_threshold: int | None = None
    full_coverage: bool = False
    first_policy_year_only: bool = False
    excluded_after_renewal: bool = False
    waiting_period_required: bool = False
    additional_coverage_subject_to_age_rule: bool = False
    original_coverage_unaffected: bool = False


@dataclass(frozen=True)
class CostShareFact:
    provider: str
    clauses: str
    source_name: str
    kind: str
    value: str | None
    scope: str | None = None
    note: str | None = None
    surface_label: str | None = None


@dataclass(frozen=True)
class AnswerResult:
    text: str
    nodes: list[NodeWithScore]
    mode: str
    structured: dict[str, Any] | None = None


class OpenAICompatibleEmbedding(BaseEmbedding):
    api_base: str = Field()
    api_key: str = Field()
    model_name: str = Field()
    batch_size: int = Field(default=16)
    timeout_seconds: float = Field(default=120.0)
    max_retries: int = Field(default=3)
    _client: OpenAIClient = PrivateAttr()

    def __init__(self, **data: Any) -> None:
        super().__init__(**data)
        self._client = OpenAIClient(
            base_url=self.api_base,
            api_key=self.api_key,
            timeout=self.timeout_seconds,
            max_retries=self.max_retries,
        )

    def _embed(self, texts: List[str]) -> List[List[float]]:
        if not texts:
            return []

        embeddings: list[list[float]] = []
        step = max(1, self.batch_size)
        for start in range(0, len(texts), step):
            batch = texts[start : start + step]
            response = retry_request(
                lambda: self._client.embeddings.create(
                    model=self.model_name,
                    input=batch,
                ),
                max_attempts=self.max_retries + 1,
            )
            ordered = sorted(response.data, key=lambda item: item.index)
            embeddings.extend(item.embedding for item in ordered)
        return embeddings

    def _get_query_embedding(self, query: str) -> List[float]:
        return self._embed([query])[0]

    async def _aget_query_embedding(self, query: str) -> List[float]:
        return self._get_query_embedding(query)

    def _get_text_embedding(self, text: str) -> List[float]:
        return self._embed([text])[0]

    def _get_text_embeddings(self, texts: List[str]) -> List[List[float]]:
        return self._embed(texts)


class OpenAICompatibleLLM(CustomLLM):
    api_base: str = Field()
    api_key: str = Field()
    model_name: str = Field()
    temperature: float = Field(default=0.1)
    max_tokens: int = Field(default=1024)
    timeout_seconds: float = Field(default=120.0)
    max_retries: int = Field(default=3)
    _client: OpenAIClient = PrivateAttr()

    def __init__(self, **data: Any) -> None:
        super().__init__(**data)
        self._client = OpenAIClient(
            base_url=self.api_base,
            api_key=self.api_key,
            timeout=self.timeout_seconds,
            max_retries=self.max_retries,
        )

    @property
    def metadata(self) -> LLMMetadata:
        return LLMMetadata(
            context_window=32000,
            num_output=self.max_tokens,
            is_chat_model=False,
            model_name=self.model_name,
        )

    @llm_completion_callback()
    def complete(self, prompt: str, formatted: bool = False, **kwargs: Any) -> CompletionResponse:
        response = retry_request(
            lambda: self._client.chat.completions.create(
                model=self.model_name,
                messages=[
                    {
                        "role": "system",
                        "content": "Answer only from the retrieved insurance policy context. If evidence is insufficient, say so clearly.",
                    },
                    {
                        "role": "user",
                        "content": prompt,
                    },
                ],
                temperature=self.temperature,
                max_tokens=self.max_tokens,
            ),
            max_attempts=self.max_retries + 1,
        )
        text = response.choices[0].message.content or ""
        return CompletionResponse(text=text, raw=response.model_dump())

    @llm_completion_callback()
    def stream_complete(self, prompt: str, formatted: bool = False, **kwargs: Any):
        raise NotImplementedError("Streaming is not implemented in this prototype")


class OpenAICompatibleReranker:
    def __init__(self, cfg: PrototypeConfig) -> None:
        self._enabled = bool(cfg.rerank_enabled and cfg.rerank_api_key and cfg.rerank_model)
        self._instruction = cfg.rerank_instruction.strip()
        self._top_n = max(1, cfg.rerank_top_n)
        self._client = OpenAIClient(
            base_url=cfg.rerank_base_url,
            api_key=cfg.rerank_api_key,
            timeout=cfg.request_timeout_seconds,
            max_retries=cfg.request_max_retries,
        )
        self._model = cfg.rerank_model
        self._max_attempts = cfg.request_max_retries + 1

    @property
    def enabled(self) -> bool:
        return self._enabled

    @property
    def top_n(self) -> int:
        return self._top_n

    def rerank(self, query: str, nodes: list[NodeWithScore]) -> list[NodeWithScore]:
        if not self._enabled or not nodes:
            return list(nodes)

        documents = [build_rerank_document(node) for node in nodes]
        payload: dict[str, Any] = {
            "model": self._model,
            "query": query,
            "documents": documents,
            "top_n": min(self._top_n, len(documents)),
            "return_documents": False,
        }
        if self._instruction:
            payload["instruction"] = self._instruction

        response = retry_request(
            lambda: self._client.post("/rerank", cast_to=dict, body=payload),
            max_attempts=self._max_attempts,
        )
        data = response.get("results") or response.get("data") or []
        if not isinstance(data, list) or not data:
            return list(nodes)

        scored: list[tuple[float, int, NodeWithScore]] = []
        for item in data:
            if not isinstance(item, dict):
                continue
            index = item.get("index")
            if not isinstance(index, int) or index < 0 or index >= len(nodes):
                continue
            relevance = float(
                item.get("relevance_score")
                or item.get("score")
                or item.get("similarity")
                or 0.0
            )
            scored.append((relevance, index, nodes[index]))

        if not scored:
            return list(nodes)

        scored.sort(key=lambda item: item[0], reverse=True)
        ordered = [node for _, _, node in scored]
        seen = {index for _, index, _ in scored}
        ordered.extend(node for idx, node in enumerate(nodes) if idx not in seen)
        return ordered


def configure_settings(cfg: PrototypeConfig) -> None:
    Settings.embed_model = OpenAICompatibleEmbedding(
        api_base=cfg.embedding_base_url,
        api_key=cfg.embedding_api_key,
        model_name=cfg.embedding_model,
        batch_size=cfg.embedding_batch_size,
        timeout_seconds=cfg.request_timeout_seconds,
        max_retries=cfg.request_max_retries,
    )
    Settings.llm = OpenAICompatibleLLM(
        api_base=cfg.llm_base_url,
        api_key=cfg.llm_api_key,
        model_name=cfg.llm_model,
        temperature=0.1,
        timeout_seconds=cfg.request_timeout_seconds,
        max_retries=cfg.request_max_retries,
    )
    # Documents are pre-chunked semantically before indexing. Keep a large parser
    # budget here so LlamaIndex does not re-split them into generic sentence windows.
    Settings.node_parser = SentenceSplitter(
        chunk_size=max(4096, cfg.chunk_size * 6),
        chunk_overlap=0,
    )


def build_index(cfg: PrototypeConfig) -> VectorStoreIndex:
    cfg.persist_dir.parent.mkdir(parents=True, exist_ok=True)
    documents = load_documents(
        cfg.data_path,
        chunk_size=cfg.chunk_size,
        chunk_overlap=cfg.chunk_overlap,
    )
    index = VectorStoreIndex.from_documents(documents, show_progress=True)
    persist_index_atomically(cfg, index, len(documents))
    return index


def load_index(cfg: PrototypeConfig) -> VectorStoreIndex:
    storage_context = StorageContext.from_defaults(persist_dir=str(cfg.persist_dir))
    return load_index_from_storage(storage_context)


def index_exists(cfg: PrototypeConfig) -> bool:
    if not (cfg.persist_dir / "docstore.json").exists():
        return False
    metadata_path = cfg.persist_dir / INDEX_METADATA_FILENAME
    if not metadata_path.exists():
        return False
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    if metadata.get("chunker_version") != CHUNKER_VERSION:
        return False
    if metadata.get("chunk_size") != cfg.chunk_size:
        return False
    if metadata.get("chunk_overlap") != cfg.chunk_overlap:
        return False
    if metadata.get("data_path") != str(cfg.data_path):
        return False
    return metadata.get("corpus_fingerprint") == compute_corpus_fingerprint(cfg.data_path)


def ensure_index(cfg: PrototypeConfig) -> VectorStoreIndex:
    configure_settings(cfg)
    if index_exists(cfg):
        try:
            return load_index(cfg)
        except Exception:
            return build_index(cfg)
    return build_index(cfg)


def write_index_metadata(cfg: PrototypeConfig, document_count: int) -> None:
    write_index_metadata_to_dir(cfg.persist_dir, cfg, document_count)


def write_index_metadata_to_dir(target_dir: Path, cfg: PrototypeConfig, document_count: int) -> None:
    markdown_files = [
        path for path in sorted(cfg.data_path.rglob("*.md"))
        if not path.name.startswith(".") and path.name != "README.md"
    ]
    payload = {
        "built_at_utc": datetime.now(timezone.utc).isoformat(),
        "chunker_version": CHUNKER_VERSION,
        "data_path": str(cfg.data_path),
        "corpus_fingerprint": compute_corpus_fingerprint(cfg.data_path),
        "chunk_size": cfg.chunk_size,
        "chunk_overlap": cfg.chunk_overlap,
        "document_count": document_count,
        "source_markdown_file_count": len(markdown_files),
        "supported_provider_count": 4,
    }
    (target_dir / INDEX_METADATA_FILENAME).write_text(
        json.dumps(payload, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )


def compute_corpus_fingerprint(data_path: Path) -> str:
    parts: list[str] = []
    for path in sorted(data_path.rglob("*.md")):
        if path.name.startswith(".") or path.name == "README.md":
            continue
        relative = path.relative_to(data_path)
        stat = path.stat()
        parts.append(
            f"{relative}|{stat.st_size}|{stat.st_mtime_ns}"
        )
    return "|".join(parts)


def answer_query(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    question: str,
    provider: str | None = None,
    language: str | None = None,
) -> AnswerResult:
    intent = detect_query_intent(question)
    nodes = retrieve_nodes(
        cfg=cfg,
        index=index,
        question=question,
        intent=intent,
        provider=provider,
        language=language,
    )
    answer_nodes = select_answer_nodes(cfg, intent, nodes)
    deterministic_answer = build_deterministic_answer(intent, answer_nodes)
    if deterministic_answer is not None:
        text, structured = deterministic_answer
        structured_type = (structured or {}).get("type", "deterministic")
        return AnswerResult(
            text=text,
            nodes=answer_nodes,
            mode=f"deterministic_{structured_type}",
            structured=structured,
        )
    prompt = build_answer_prompt(intent, answer_nodes)
    response = Settings.llm.complete(prompt)
    answer = response.text.strip() if response.text else ""
    return AnswerResult(
        text=answer,
        nodes=answer_nodes,
        mode="llm",
        structured=None,
    )


def retrieve_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    question: str,
    intent: QueryIntent,
    provider: str | None = None,
    language: str | None = None,
) -> list[NodeWithScore]:
    filters = []
    if provider:
        filters.append(ExactMatchFilter(key="provider", value=provider.lower()))
    if language:
        filters.append(ExactMatchFilter(key="language", value=language))

    retriever = index.as_retriever(
        similarity_top_k=max(cfg.top_k, cfg.initial_retrieval_k),
        filters=MetadataFilters(filters=filters) if filters else None,
    )
    nodes = merge_candidate_nodes(
        list(retriever.retrieve(question)),
        lexical_backfill_nodes(
            cfg=cfg,
            index=index,
            question=question,
            intent=intent,
            provider=provider,
            language=language,
        ),
    )
    if is_general_age_limit_query(intent):
        nodes = merge_candidate_nodes(
            nodes,
            general_age_limit_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    if intent.wants_consult:
        nodes = merge_candidate_nodes(
            nodes,
            consult_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    if intent.asks_limit and not intent.wants_consult:
        nodes = merge_candidate_nodes(
            nodes,
            generic_limit_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    if intent.asks_cost_share:
        nodes = merge_candidate_nodes(
            nodes,
            cost_share_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    if should_try_generic_exclusion(intent):
        nodes = merge_candidate_nodes(
            nodes,
            generic_exclusion_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    nodes = merge_candidate_nodes(
        nodes,
        markdown_table_backfill_nodes(
            cfg=cfg,
            index=index,
            intent=intent,
            provider=provider,
            language=language,
        ),
    )
    if intent.asks_chronic_condition:
        nodes = merge_candidate_nodes(
            nodes,
            chronic_condition_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                provider=provider,
                language=language,
            ),
        )
    if intent.wants_comparison and intent.target_providers and not provider:
        nodes = merge_candidate_nodes(
            nodes,
            comparison_provider_backfill_nodes(
                cfg=cfg,
                index=index,
                intent=intent,
                language=language,
            ),
        )
        target_set = set(intent.target_providers)
        nodes = [node for node in nodes if (node.node.metadata or {}).get("provider") in target_set]
    reranked = rerank_nodes(cfg, intent, nodes)
    if intent.asks_addon_benefit and intent.asks_eligibility:
        reranked = reorder_addon_conditions_candidates(intent, reranked)
    if intent.asks_cash_benefit and intent.asks_age_limit:
        reranked = reorder_addon_eligibility_candidates(intent, reranked)
    if intent.asks_addon_benefit and intent.wants_waiting_period:
        reranked = reorder_addon_waiting_period_candidates(intent, reranked)
    if intent.wants_consult:
        reranked = reorder_consult_candidates(intent, reranked)
    if intent.asks_limit and not intent.wants_consult:
        reranked = reorder_generic_limit_candidates(intent, reranked)
    if intent.asks_cost_share:
        reranked = reorder_cost_share_candidates(intent, reranked)
    return reranked[: cfg.answer_max_sources]


def rerank_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    model_reranked = list(nodes)
    model_rank_by_key: dict[tuple[str, str], int] = {}
    if cfg.rerank_enabled:
        try:
            model_reranked = OpenAICompatibleReranker(cfg).rerank(intent.raw_question, nodes)
            model_rank_by_key = {
                node_key(node): idx
                for idx, node in enumerate(model_reranked[: max(cfg.rerank_top_n, cfg.answer_max_sources)])
            }
        except Exception:
            model_reranked = list(nodes)
            model_rank_by_key = {}

    scored: list[tuple[float, float, int, NodeWithScore]] = []
    rerank_window = max(cfg.answer_max_sources, min(len(model_reranked), cfg.rerank_top_n))
    for idx, node in enumerate(nodes):
        heuristic = rerank_score(intent, node)
        model_rank = model_rank_by_key.get(node_key(node))
        model_bonus = float(rerank_window - model_rank) * 0.15 if model_rank is not None and model_rank < rerank_window else 0.0
        scored.append((heuristic, model_bonus, idx, node))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, _, node in scored]


def reorder_cost_share_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = cost_share_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def reorder_generic_limit_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = generic_limit_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def reorder_consult_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = consult_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def reorder_addon_waiting_period_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_waiting_period_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def reorder_addon_eligibility_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_eligibility_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def reorder_addon_conditions_candidates(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_conditions_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return list(nodes)

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    seen = {node_key(node) for node in ordered}
    ordered.extend(node for node in nodes if node_key(node) not in seen)
    return ordered


def select_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    if not nodes:
        return []

    if intent.wants_comparison:
        return select_comparison_answer_nodes(cfg, intent, nodes)

    if intent.wants_pre_existing:
        return select_pre_existing_answer_nodes(cfg, intent, nodes)

    if intent.asks_cost_share:
        return select_cost_share_answer_nodes(cfg, intent, nodes)

    if intent.asks_chronic_condition:
        return select_chronic_condition_answer_nodes(cfg, intent, nodes)

    if intent.asks_addon_benefit and intent.asks_eligibility:
        return select_addon_conditions_answer_nodes(cfg, intent, nodes)

    if intent.asks_cash_benefit and intent.asks_age_limit:
        return select_addon_eligibility_answer_nodes(cfg, intent, nodes)

    if intent.asks_cash_benefit and intent.wants_waiting_period:
        return select_addon_waiting_period_answer_nodes(cfg, intent, nodes)

    if intent.wants_waiting_period and not intent.asks_addon_benefit and not intent.asks_upgrade and not intent.asks_renewal:
        return select_waiting_period_answer_nodes(cfg, intent, nodes)

    if intent.asks_renewal or intent.asks_upgrade:
        return select_renewal_upgrade_answer_nodes(cfg, intent, nodes)

    if is_general_age_limit_query(intent):
        return select_general_age_limit_answer_nodes(cfg, intent, nodes)

    if intent.wants_consult and (intent.wants_coverage or intent.asks_limit):
        return select_consult_answer_nodes(cfg, intent, nodes)

    if intent.asks_limit:
        return select_generic_limit_answer_nodes(cfg, intent, nodes)

    if should_try_generic_exclusion(intent):
        exclusion_nodes = select_generic_exclusion_answer_nodes(cfg, intent, nodes)
        if exclusion_nodes:
            return exclusion_nodes

    markdown_table_nodes = select_markdown_table_answer_nodes(cfg, intent, nodes)
    if markdown_table_nodes:
        return markdown_table_nodes

    top_score = rerank_score(intent, nodes[0])
    selected: list[NodeWithScore] = []
    for node in nodes:
        if len(selected) >= cfg.answer_max_sources:
            break

        score = rerank_score(intent, node)
        if not selected:
            selected.append(node)
            continue

        if should_keep_supporting_node(intent, top_score, score, node):
            selected.append(node)

    return selected or nodes[: cfg.answer_max_sources]


def select_pre_existing_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    primary = best_pre_existing_node(intent, nodes, require_exclusion=True)
    if primary is None:
        primary = best_pre_existing_node(intent, nodes, require_exclusion=False)
    if primary is None:
        return nodes[: min(2, cfg.answer_max_sources)]

    selected = [primary]
    primary_provider = (primary.node.metadata or {}).get("provider", "")
    definition = best_pre_existing_definition_node(intent, nodes, primary_provider)
    if definition is not None and node_key(definition) != node_key(primary) and len(selected) < cfg.answer_max_sources:
        selected.append(definition)
    return selected


def select_consult_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    primary = best_consult_node(intent, nodes)
    if primary is None:
        return nodes[: min(2, cfg.answer_max_sources)]
    selected = [primary]
    primary_provider = (primary.node.metadata or {}).get("provider", "")
    primary_label = consultation_label_for_text(primary.node.text, prefer_zh=question_prefers_zh(intent.raw_question))
    for node in nodes:
        if len(selected) >= min(2, cfg.answer_max_sources):
            break
        if node_key(node) == node_key(primary):
            continue
        metadata = node.node.metadata or {}
        if primary_provider and metadata.get("provider", "") != primary_provider:
            continue
        if consult_answer_score(intent, node) < 3.0:
            continue
        label = consultation_label_for_text(node.node.text, prefer_zh=question_prefers_zh(intent.raw_question))
        if label == primary_label:
            continue
        selected.append(node)
    return selected


def select_generic_exclusion_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = generic_exclusion_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return []

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [ranked[0][2]]


def select_cost_share_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    requested_kind = cost_share_kind_requested(intent)
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        metadata = node.node.metadata or {}
        candidate_kind = infer_cost_share_kind_from_node(metadata, node.node.text)
        if not node_matches_cost_share_kind(requested_kind, candidate_kind):
            continue
        score = cost_share_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ordered = [node for _, _, node in ranked]
    primary = ordered[0]

    # Most cost-share answers only need the strongest single clause. Keep
    # multiple nodes only when they provide distinct deductible values that
    # together form the answer.
    if requested_kind != "deductible":
        support_candidates = [node for node in nodes if node_key(node) != node_key(primary)]
        support = select_cost_share_dependency_support_node(intent, primary, support_candidates)
        if support is not None and cfg.answer_max_sources > 1:
            return [primary, support]
        return [primary]

    selected: list[NodeWithScore] = []
    seen_fact_keys: set[tuple[str, str, str, str]] = set()
    for node in ordered:
        fact = cost_share_fact_from_node(intent, node)
        if fact is None or not fact.value:
            continue
        fact_key = (fact.provider, fact.kind, fact.scope or "", fact.value or "")
        if fact_key in seen_fact_keys:
            continue
        seen_fact_keys.add(fact_key)
        selected.append(node)
        if len(selected) >= cfg.answer_max_sources:
            break

    return selected or [primary]


def select_addon_waiting_period_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_waiting_period_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in ranked[: min(2, cfg.answer_max_sources)]]


def select_addon_eligibility_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_eligibility_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in ranked[: min(2, cfg.answer_max_sources)]]


def select_addon_conditions_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_conditions_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in ranked[: min(2, cfg.answer_max_sources)]]


def waiting_period_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    fact = extract_waiting_period_fact(intent, node)
    if fact is None:
        return 0.0

    score = rerank_score(intent, node)
    if intent.wants_cancer and fact.cancer_days is not None:
        score += 2.0
    elif intent.wants_injury and fact.injury_days is not None:
        score += 2.0
    elif not intent.wants_cancer and not intent.wants_injury:
        if fact.illness_days is not None:
            score += 1.8
        elif fact.general_days is not None:
            score += 1.2
        elif fact.cancer_days is not None:
            score += 0.6
        elif fact.injury_days is not None:
            score += 0.3
    return score


def select_waiting_period_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, NodeWithScore]] = []
    for node in nodes:
        score = waiting_period_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]

    ranked.sort(key=lambda item: item[0], reverse=True)
    if not intent.wants_comparison:
        return [ranked[0][1]]

    selected: list[NodeWithScore] = []
    seen_providers: set[str] = set()
    for _, node in ranked:
        provider = (node.node.metadata or {}).get("provider", "")
        if provider in seen_providers:
            continue
        seen_providers.add(provider)
        selected.append(node)
        if len(selected) >= cfg.answer_max_sources:
            break
    return selected


def select_chronic_condition_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = chronic_condition_answer_score(intent, node)
        if score <= 0.0:
            continue
        scored.append((score, rerank_score(intent, node), node))
    if not scored:
        return nodes[: min(2, cfg.answer_max_sources)]

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ranked = [node for _, _, node in scored]

    selected: list[NodeWithScore] = []
    preferred_rule_kind = preferred_chronic_rule_kind(intent)
    if preferred_rule_kind is not None:
        primary = best_chronic_condition_node(intent, ranked, preferred_rule_kind=preferred_rule_kind)
        if primary is not None:
            selected.append(primary)
    else:
        for rule_kind in ("age_4_or_below", "age_5_or_above"):
            candidate = best_chronic_condition_node(intent, ranked, preferred_rule_kind=rule_kind)
            if candidate is not None and all(node_key(candidate) != node_key(existing) for existing in selected):
                selected.append(candidate)

    if intent.asks_upgrade:
        upgrade = best_chronic_condition_node(intent, ranked, preferred_rule_kind="upgrade_additional_coverage")
        if upgrade is not None and (not selected or node_key(upgrade) != node_key(selected[0])):
            selected.append(upgrade)

    if not selected:
        return ranked[: min(2, cfg.answer_max_sources)]

    seen = {node_key(node) for node in selected}
    for node in ranked:
        if len(selected) >= min(3, cfg.answer_max_sources):
            break
        if node_key(node) in seen:
            continue
        selected.append(node)
        seen.add(node_key(node))
    return selected


def select_generic_limit_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = generic_limit_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in ranked[: min(2, cfg.answer_max_sources)]]


def select_markdown_table_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        scored = markdown_table_fact_from_node(intent, node)
        if scored is None:
            continue
        score, _ = scored
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return []
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [ranked[0][2]]


def select_general_age_limit_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = general_age_limit_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    selected = [ranked[0][2]]
    primary_provider = (selected[0].node.metadata or {}).get("provider", "")
    primary_clause = (selected[0].node.metadata or {}).get("clauses", "")
    primary_score = ranked[0][0]

    for score, _, node in ranked[1:]:
        if len(selected) >= min(2, cfg.answer_max_sources):
            break
        metadata = node.node.metadata or {}
        if primary_provider and metadata.get("provider", "") != primary_provider:
            continue
        if metadata.get("clauses", "") == primary_clause:
            continue
        if score < max(2.8, primary_score * 0.82):
            continue
        if not node_has_general_age_limit_evidence(node):
            continue
        selected.append(node)
    return selected


def select_renewal_upgrade_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = renewal_upgrade_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return nodes[: min(2, cfg.answer_max_sources)]
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in ranked[: min(2, cfg.answer_max_sources)]]


def select_comparison_answer_nodes(
    cfg: PrototypeConfig,
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[NodeWithScore]:
    selected: list[NodeWithScore] = []
    used_providers: set[str] = set()
    provider_order = list(intent.target_providers) if intent.target_providers else []

    def best_for_provider(provider_name: str) -> NodeWithScore | None:
        ranked: list[tuple[float, float, NodeWithScore]] = []
        for node in nodes:
            metadata = node.node.metadata or {}
            if metadata.get("provider") != provider_name:
                continue
            answer_score = comparison_provider_answer_score(intent, node)
            base_score = rerank_score(intent, node)
            if answer_score <= 0.0 and base_score < 1.2:
                continue
            ranked.append((answer_score, base_score, node))
        if not ranked:
            return None
        ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
        return ranked[0][2]

    for provider_name in provider_order:
        if len(selected) >= cfg.answer_max_sources:
            break
        chosen = best_for_provider(provider_name)
        if chosen is None:
            continue
        selected.append(chosen)
        used_providers.add(provider_name)

    if provider_order:
        return selected

    for node in nodes:
        if len(selected) >= cfg.answer_max_sources:
            break
        metadata = node.node.metadata or {}
        provider_name = metadata.get("provider", "")
        if provider_name in used_providers:
            continue
        if rerank_score(intent, node) < 1.2:
            continue
        selected.append(node)
        used_providers.add(provider_name)

    return selected or nodes[: cfg.answer_max_sources]


def comparison_provider_answer_score(intent: QueryIntent, node: NodeWithScore) -> float:
    if intent.wants_consult:
        return consult_answer_score(intent, node)
    if intent.wants_waiting_period:
        return waiting_period_answer_score(intent, node)
    if intent.asks_limit and not intent.wants_consult:
        return generic_limit_answer_score(intent, node)
    return rerank_score(intent, node)


def rerank_score(intent: QueryIntent, node: NodeWithScore) -> float:
    text = node.node.text
    metadata = node.node.metadata or {}
    lower_text = normalize_text(text)
    base = float(node.score or 0.0)
    bonus = 0.0

    unit_types = normalize_text(metadata.get("unit_types", ""))
    section_path = normalize_text(metadata.get("section_path", ""))
    clauses = normalize_text(metadata.get("clauses", ""))
    topic_tags = metadata_tags(metadata)

    if intent.wants_waiting_period:
        if "waiting_period" in unit_types:
            bonus += 2.2
        if contains_any(section_path, "waiting period", "等候期"):
            bonus += 1.4
        if "standard_waiting_period" in topic_tags:
            bonus += 1.2
        if contains_any(unit_types, "claim_rule", "renewal_rule", "definition"):
            bonus -= 0.8
        if contains_any(lower_text, "waiting period", "等候期"):
            bonus += 0.9
        if contains_any(lower_text, "valid only if selected", "只适用于选择购买", "只適用於選擇購買"):
            bonus -= 0.6
        if "addon_waiting_period" in topic_tags or contains_any(lower_text, "附加保障", "高級危疾現金保障", "危疾現金保障之保障期", "選擇此附加保障"):
            bonus -= 1.1
        if "mri_ct" in topic_tags or contains_mri_ct_reference(lower_text):
            bonus -= 1.4
        if contains_any(lower_text, "不設等候期", "不设等候期", "no waiting period"):
            bonus -= 1.2
        if not intent.asks_renewal and ("renewal" in topic_tags or contains_any(lower_text, "續保", "续保", "renewal")):
            bonus -= 1.0
        if not intent.asks_upgrade and ("upgrade" in topic_tags or contains_any(lower_text, "升級", "升级", "upgrade")):
            bonus -= 0.8
        if not intent.wants_pre_existing and ("pre_existing" in topic_tags or contains_any(lower_text, "投保前已存在", "pre-existing", "pre existing")):
            bonus -= 0.9

    if intent.wants_coverage:
        if contains_any(unit_types, "benefit", "coverage_definition", "eligibility", "payout"):
            bonus += 1.0
        if contains_any(unit_types, "claim_rule", "renewal_rule"):
            bonus -= 0.7

    if intent.asks_chronic_condition:
        if contains_any(lower_text, "慢性病況", "慢性病况", "慢性疾病", "chronic medical conditions", "chronic condition"):
            bonus += 2.4
        else:
            bonus -= 1.6
        if "eligibility" in unit_types:
            bonus += 1.6
        if "renewal_rule" in unit_types and intent.asks_upgrade:
            bonus += 1.1
        if contains_any(lower_text, "4 歲或以下", "5 歲或以上", "4 years old or below", "5 years old or above"):
            bonus += 1.3
        if contains_any(lower_text, "全面保障", "only provide coverage", "不再受保", "excluded from the subsequent renewal", "original coverage is unaffected", "原保障不受影響"):
            bonus += 1.4

    if intent.wants_exclusion and "exclusion" in unit_types:
        bonus += 1.0

    if intent.wants_pre_existing:
        if "pre_existing" in topic_tags:
            bonus += 1.8
        elif "exclusion" in unit_types:
            bonus += 0.5

    if intent.wants_cancer:
        if "cancer" in topic_tags or contains_any(lower_text, "cancer", "癌症", "恶性肿瘤", "惡性腫瘤"):
            bonus += 1.1
        else:
            bonus -= 1.5
        if "critical_illness" in topic_tags or contains_any(lower_text, "critical illness", "危疾"):
            bonus -= 0.55
        if ("critical_illness" in topic_tags or "fip" in topic_tags or contains_any(lower_text, "癌症現金保障", "癌症现金保障", "cancer cash benefit", "貓傳染性腹膜炎", "feline infectious peritonitis")) and not intent.asks_addon_benefit:
            bonus -= 1.0

    if intent.wants_injury:
        if contains_any(lower_text, "injury", "injuries", "accident", "受伤", "受傷", "身体损伤", "身體損傷"):
            bonus += 0.7

    if intent.asks_renewal:
        if "renewal_rule" in unit_types:
            bonus += 2.0
        if contains_any(lower_text, "續保", "续保", "renewal", "renewed", "renew automatically", "自動續保", "自动续保"):
            bonus += 1.3

    if intent.asks_upgrade:
        if contains_any(lower_text, "升級", "升级", "upgrade", "downgrade", "降級", "降级"):
            bonus += 1.8
        if "renewal_rule" in unit_types and contains_any(lower_text, "升級", "升级", "upgrade"):
            bonus += 1.4
        if intent.wants_waiting_period and contains_any(lower_text, "等候期", "waiting period"):
            bonus += 0.9

    if intent.asks_eligibility:
        if "eligibility" in unit_types:
            bonus += 1.8
        if contains_any(lower_text, "受保條件", "eligibility", "只供", "must not develop", "註冊獸醫", "registered veterinary surgeon"):
            bonus += 1.2
        if contains_any(lower_text, "年齡限制", "age limit") and not intent.asks_age_limit:
            bonus -= 0.7

    if is_general_age_limit_query(intent):
        if contains_any(unit_types, "eligibility", "general_condition"):
            bonus += 1.4
        if contains_any(section_path, "age limit", "pet eligibility", "eligibility", "年齡限制", "年龄限制"):
            bonus += 1.6
        if contains_any(
            lower_text,
            "aged between",
            "at least",
            "weeks old",
            "years old",
            "年齡為",
            "年龄为",
            "星期以上",
            "歲以下",
            "岁以下",
        ):
            bonus += 1.9
        if contains_any(
            lower_text,
            "table of benefits",
            "annual coverage",
            "per year",
            "per visit",
            "總年度保障",
            "总年度保障",
            "最高賠償額",
            "最高赔偿额",
        ) or has_explicit_limit_pattern(text):
            bonus -= 1.8

    if intent.wants_consult:
        if "consult" in topic_tags:
            bonus += 1.9
        if contains_any(section_path, "consultation", "獸醫診症", "兽医诊症", "診金"):
            bonus += 1.2
        if contains_any(lower_text, "consult", "consultation", "診症", "诊症", "診金"):
            bonus += 1.1
        elif contains_any(lower_text, "獸醫", "兽医", "vet"):
            bonus += 0.3
        if "coverage_definition" in unit_types and "consult" not in topic_tags:
            bonus -= 0.6
        subtype = consult_subtype_requested(intent)
        if subtype and consult_text_matches_subtype(lower_text, subtype):
            bonus += 1.2
        elif subtype and "consult" in topic_tags:
            bonus -= 0.7

    if intent.asks_limit:
        if has_explicit_limit_pattern(text):
            bonus += 1.7
        elif intent.wants_consult and "consult" in topic_tags:
            bonus -= 0.4

    if intent.asks_cost_share:
        if contains_cost_share_reference(lower_text):
            bonus += 2.2
        if contains_any(lower_text, "30%", "table of benefits", "承保表", "policy schedule"):
            bonus += 0.8

    if intent.wants_comparison and ("comparison" in topic_tags or "definition" in unit_types):
        bonus += 0.2

    bonus += list_item_label_rerank_bonus(intent, node)
    bonus += markdown_table_rerank_bonus(intent, node)

    lexical = lexical_overlap_score(intent.normalized_question, lower_text)
    clause_bonus = 0.15 if clauses else 0.0
    return base + bonus + lexical + clause_bonus


def list_item_label_rerank_bonus(intent: QueryIntent, node: NodeWithScore) -> float:
    metadata = node.node.metadata or {}
    labels = metadata_list_item_labels(metadata)
    if not labels:
        return 0.0

    question = intent.normalized_question
    unit_types = normalize_text(metadata.get("unit_types", ""))
    best_overlap = max((semantic_label_match_score(question, label) for label in labels), default=0.0)
    if best_overlap <= 0.0:
        return 0.0

    bonus = best_overlap * 5.0
    if intent.wants_exclusion and "exclusion" in unit_types:
        bonus += best_overlap * 2.0
    if intent.wants_coverage and "exclusion" in unit_types:
        bonus += best_overlap * 1.8
    if intent.wants_waiting_period and "waiting_period" in unit_types:
        bonus += best_overlap * 1.2
    if intent.asks_limit and contains_any(unit_types, "benefit", "benefit_item", "benefit_table"):
        bonus += best_overlap * 1.0
    return bonus


def markdown_table_rerank_bonus(intent: QueryIntent, node: NodeWithScore) -> float:
    metadata = node.node.metadata or {}
    if not is_markdown_table_query(intent):
        return 0.0

    lower_text = normalize_text(node.node.text)
    section_path = normalize_text(metadata.get("section_path", ""))
    clauses = normalize_text(metadata.get("clauses", ""))
    has_table = metadata_flag(metadata, "contains_markdown_table")
    question = intent.normalized_question

    bonus = 0.0
    if has_table:
        bonus += 3.0
        headers = [part.strip() for part in str(metadata.get("table_headers", "")).split(",") if part.strip()]
        row_labels = [part.strip() for part in str(metadata.get("table_row_labels", "")).split(",") if part.strip()]
        if headers:
            bonus += max(table_header_match_score(question, header) for header in headers)
        requested_rows = requested_markdown_table_row_numbers(intent)
        if requested_rows:
            matches = sum(1 for label in row_labels if (first_numeric_token(label) or "") in requested_rows)
            bonus += min(matches, 3) * 1.5
        if query_wants_markdown_table_overview(intent):
            bonus += 2.0

    if contains_any(question, "discount rate", "discount rates", "折扣率") and contains_any(
        lower_text,
        "discount rate",
        "折扣率",
    ):
        bonus += 1.8
    if contains_any(question, "no claim", "without claims", "無索償", "无索偿") and contains_any(
        lower_text,
        "no claim",
        "without claims",
        "無索償",
        "无索偿",
    ):
        bonus += 1.6
    if contains_any(question, "discount", "折扣") and contains_any(section_path, "no claim discount", "無索償折扣", "无索偿折扣"):
        bonus += 1.4
    if contains_any(question, "discount", "折扣") and contains_any(clauses, "no claim discount"):
        bonus += 1.0

    if not has_table and contains_any(question, "discount", "折扣", "no claim", "無索償", "无索偿"):
        if "exclusion" in normalize_text(metadata.get("unit_types", "")):
            bonus -= 2.0
    return bonus


def should_keep_supporting_node(
    intent: QueryIntent,
    top_score: float,
    candidate_score: float,
    node: NodeWithScore,
) -> bool:
    lower_text = normalize_text(node.node.text)
    metadata = node.node.metadata or {}
    unit_types = normalize_text(metadata.get("unit_types", ""))
    topic_tags = metadata_tags(metadata)

    if candidate_score < max(1.6, top_score * 0.42):
        return False

    if intent.wants_waiting_period:
        if "waiting_period" not in unit_types and "eligibility" not in unit_types and "waiting_period" not in topic_tags:
            return False
        if "mri_ct" in topic_tags or contains_mri_ct_reference(lower_text):
            return False
        if {"renewal", "upgrade"} & topic_tags or contains_any(lower_text, "續保", "续保", "renewal", "升級", "升级", "upgrade"):
            return False
        if {"addon_waiting_period", "critical_illness", "fip"} & topic_tags or contains_any(lower_text, "附加保障", "高級危疾現金保障", "貓傳染性腹膜炎", "critical illness", "fip"):
            return False
        if intent.wants_cancer and "cancer" not in topic_tags and not contains_any(lower_text, "癌症", "惡性腫瘤", "恶性肿瘤"):
            return False

    if intent.wants_pre_existing and "pre_existing" not in topic_tags and not contains_any(lower_text, "已存在", "pre-existing", "pre existing"):
        return False

    if intent.wants_pre_existing and "pre_existing" not in topic_tags:
        return False

    if intent.wants_exclusion and "exclusion" not in unit_types and "pre_existing" not in topic_tags:
        return False

    return True


def build_answer_prompt(intent: QueryIntent, nodes: list[NodeWithScore]) -> str:
    context_blocks = []
    for idx, node in enumerate(nodes, start=1):
        meta = node.node.metadata or {}
        context_blocks.append(
            "\n".join(
                [
                    f"[Source {idx}] provider={meta.get('provider', '')} language={meta.get('language', '')}",
                    f"source_name={meta.get('source_name', '')}",
                    f"section_path={meta.get('section_path', '')}",
                    f"clauses={meta.get('clauses', '')}",
                    f"unit_types={meta.get('unit_types', '')}",
                    node.node.text.strip(),
                ]
            )
        )

    context = "\n\n".join(context_blocks)
    comparison_rule = ""
    if intent.wants_comparison:
        provider_hint = ", ".join(intent.target_providers) if intent.target_providers else "the mentioned providers"
        comparison_rule = (
            "- Present the answer as a concise provider-by-provider comparison.\n"
            f"- Cover providers in this order when possible: {provider_hint}.\n"
            "- If a requested provider is missing evidence, say that explicitly.\n"
        )
    age_limit_rule = ""
    if is_general_age_limit_query(intent):
        age_limit_rule = "- If the question asks about age limits, prefer entry-age or eligibility clauses over benefit tables or monetary limits.\n"
    return (
        "You are answering insurance-policy questions from retrieved policy chunks.\n"
        "Rules:\n"
        "- Answer only from the provided evidence.\n"
        "- Prefer the most direct clause over related but narrower add-on clauses.\n"
        f"{comparison_rule}"
        f"{age_limit_rule}"
        "- If the question asks about exclusions or pre-existing conditions, focus on non-covered cases rather than covered benefits.\n"
        "- If evidence is ambiguous or partial, say so explicitly.\n"
        "- Be concise and factual.\n"
        "- Do not invent provider facts not present in the evidence.\n\n"
        f"Question:\n{intent.raw_question}\n\n"
        f"Retrieved evidence:\n{context}\n\n"
        "Answer in the same language as the question."
    )


def build_deterministic_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    if intent.asks_chronic_condition and nodes and not intent.wants_comparison:
        chronic_condition_answer = build_deterministic_chronic_condition_answer(intent, nodes)
    else:
        chronic_condition_answer = None

    if chronic_condition_answer is not None:
        return chronic_condition_answer

    if intent.asks_addon_benefit and intent.asks_eligibility and nodes and not intent.wants_comparison:
        addon_conditions_answer = build_deterministic_addon_conditions_answer(intent, nodes)
    else:
        addon_conditions_answer = None

    if addon_conditions_answer is not None:
        return addon_conditions_answer

    if intent.asks_cash_benefit and intent.asks_age_limit and nodes and not intent.wants_comparison:
        addon_eligibility_answer = build_deterministic_addon_eligibility_answer(intent, nodes)
    else:
        addon_eligibility_answer = None

    if addon_eligibility_answer is not None:
        return addon_eligibility_answer

    if intent.asks_addon_benefit and intent.wants_waiting_period and nodes and not intent.wants_comparison:
        addon_waiting_answer = build_deterministic_addon_waiting_period_answer(intent, nodes)
    else:
        addon_waiting_answer = None

    if addon_waiting_answer is not None:
        return addon_waiting_answer

    if (intent.asks_upgrade or intent.asks_renewal) and nodes and not intent.wants_comparison:
        renewal_or_upgrade_answer = build_deterministic_renewal_upgrade_answer(intent, nodes)
    else:
        renewal_or_upgrade_answer = None

    if renewal_or_upgrade_answer is not None:
        return renewal_or_upgrade_answer

    if not intent.wants_waiting_period or not nodes:
        waiting_period_answer = None
    else:
        waiting_period_answer = build_deterministic_waiting_period_answer(intent, nodes)

    if waiting_period_answer is not None:
        return waiting_period_answer

    if intent.wants_pre_existing and nodes:
        return build_deterministic_pre_existing_answer(intent, nodes)

    if should_try_generic_exclusion(intent) and nodes and not intent.wants_comparison:
        generic_exclusion_answer = build_deterministic_generic_exclusion_answer(intent, nodes)
    else:
        generic_exclusion_answer = None

    if generic_exclusion_answer is not None:
        return generic_exclusion_answer

    if intent.asks_cost_share and nodes and not intent.wants_comparison:
        return build_deterministic_cost_share_answer(intent, nodes)

    if nodes and not intent.wants_comparison and not intent.asks_cost_share:
        markdown_table_answer = build_deterministic_markdown_table_answer(intent, nodes)
    else:
        markdown_table_answer = None

    if markdown_table_answer is not None:
        return markdown_table_answer

    if intent.wants_consult and intent.asks_limit and nodes:
        return build_deterministic_consult_limit_answer(intent, nodes)

    if intent.asks_limit and nodes and not intent.wants_consult:
        return build_deterministic_generic_limit_answer(intent, nodes)

    if intent.wants_consult and intent.wants_coverage and nodes and not intent.wants_comparison:
        return build_deterministic_consult_answer(intent, nodes)

    return None


def build_deterministic_waiting_period_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    facts = collect_waiting_period_facts(intent, nodes)
    if not facts:
        return None

    if intent.wants_comparison and intent.target_providers:
        return build_waiting_period_comparison_answer(intent, facts)

    first_fact = facts[0]
    return build_single_provider_waiting_period_answer(intent, first_fact)


def build_deterministic_renewal_upgrade_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    if intent.asks_upgrade and intent.asks_age_limit:
        fact = extract_upgrade_age_limit_fact(intent, nodes)
    else:
        fact = extract_renewal_upgrade_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)

    if intent.asks_upgrade:
        answer = format_upgrade_answer(provider_name, fact, prefer_zh)
        if answer is None:
            return None
        return (
            answer,
            {
                "type": "upgrade_rule_single",
                "provider": fact.provider,
                "display_name": provider_name,
                "clauses": fact.clauses,
                "source_name": fact.source_name,
                "can_upgrade": fact.can_upgrade,
                "upgrade_waiting_period": fact.upgrade_waiting_period,
                "downgrade_no_waiting": fact.downgrade_no_waiting,
                "age_upgrade_block": fact.age_upgrade_block,
            },
        )

    answer = format_renewal_answer(provider_name, fact, prefer_zh)
    if answer is None:
        return None
    return (
        answer,
        {
            "type": "renewal_rule_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
            "auto_renew": fact.auto_renew,
            "guaranteed_age": fact.guaranteed_age,
            "approval_age_threshold": fact.approval_age_threshold,
        },
    )


def build_deterministic_addon_waiting_period_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    fact = extract_addon_waiting_period_fact(intent, nodes)
    if fact is None or fact.days is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if prefer_zh:
        answer = f"{provider_name} 的{fact.addon_label}等候期為 {fact.days} 日{format_clause_suffix(fact.clauses, prefer_zh=True)}"
    else:
        answer = f"{provider_name} {fact.addon_label} waiting period is {fact.days} days{format_clause_suffix(fact.clauses, prefer_zh=False)}"

    return (
        answer,
        {
            "type": "addon_waiting_period_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "addon_label": fact.addon_label,
            "days": fact.days,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_deterministic_addon_eligibility_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    fact = extract_addon_eligibility_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if fact.min_age is None and fact.max_age is None and fact.renewal_cutoff_age is None and not fact.renewal_no_age_limit:
        return None

    if prefer_zh:
        parts: list[str] = []
        if fact.min_age is not None and fact.max_age is not None:
            parts.append(f"只供 {fact.min_age} 歲至 {fact.max_age} 歲的寵物投保")
        elif fact.min_age is not None:
            parts.append(f"最低投保年齡為 {fact.min_age} 歲")
        elif fact.max_age is not None:
            parts.append(f"最高投保年齡為 {fact.max_age} 歲")
        if fact.renewal_cutoff_age is not None:
            parts.append(f"寵物到達 {fact.renewal_cutoff_age} 歲起無法續保")
        elif fact.renewal_no_age_limit:
            parts.append("續保時不設年齡限制")
        answer = f"{provider_name} 的{fact.addon_label}" + "，".join(parts) + format_clause_suffix(fact.clauses, prefer_zh=True)
    else:
        parts = []
        if fact.min_age is not None and fact.max_age is not None:
            parts.append(f"is only available for pets from {fact.min_age} to {fact.max_age} years old")
        elif fact.min_age is not None:
            parts.append(f"has a minimum entry age of {fact.min_age}")
        elif fact.max_age is not None:
            parts.append(f"has a maximum entry age of {fact.max_age}")
        if fact.renewal_cutoff_age is not None:
            parts.append(f"is not renewable once the pet becomes {fact.renewal_cutoff_age} years old")
        elif fact.renewal_no_age_limit:
            parts.append("has no age limit for renewals")
        answer = f"{provider_name} {fact.addon_label} " + ", ".join(parts) + format_clause_suffix(fact.clauses, prefer_zh=False)

    return (
        answer,
        {
            "type": "addon_eligibility_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "addon_label": fact.addon_label,
            "min_age": fact.min_age,
            "max_age": fact.max_age,
            "renewal_cutoff_age": fact.renewal_cutoff_age,
            "renewal_no_age_limit": fact.renewal_no_age_limit,
            "new_purchase_only": fact.new_purchase_only,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_deterministic_addon_conditions_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    fact = extract_addon_conditions_fact(intent, nodes)
    if fact is None or not fact.conditions:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if prefer_zh:
        clause_suffix = format_clause_suffix(fact.clauses, prefer_zh=True).rstrip("。")
        answer = (
            f"{provider_name} 的{fact.addon_label}受保條件如下{clause_suffix}：\n"
            + "\n".join(f"- {condition}" for condition in fact.conditions)
        )
    else:
        clause_suffix = format_clause_suffix(fact.clauses, prefer_zh=False).rstrip(".")
        answer = (
            f"{provider_name} {fact.addon_label} eligibility conditions are{clause_suffix}:\n"
            + "\n".join(f"- {condition}" for condition in fact.conditions)
        )

    return (
        answer,
        {
            "type": "addon_conditions_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "addon_label": fact.addon_label,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
            "conditions": list(fact.conditions),
        },
    )


def build_deterministic_chronic_condition_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    facts = collect_chronic_condition_facts(intent, nodes)
    if not facts:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    if chronic_condition_query_is_general(intent):
        summary = build_general_chronic_condition_answer(intent, facts, prefer_zh=prefer_zh)
        if summary is not None:
            answer, structured = summary
            return answer, structured

    primary = select_primary_chronic_condition_fact(intent, facts)
    if primary is None:
        return None
    upgrade_fact = select_upgrade_chronic_condition_fact(facts) if intent.asks_upgrade else None

    provider_name = provider_display_name(primary.provider)
    clauses = primary.clauses
    if upgrade_fact is not None and upgrade_fact.clauses and upgrade_fact.clauses not in clauses:
        clauses = f"{primary.clauses}, {upgrade_fact.clauses}" if primary.clauses else upgrade_fact.clauses

    if prefer_zh:
        answer = format_chronic_condition_answer_zh(provider_name, primary, upgrade_fact)
    else:
        answer = format_chronic_condition_answer_en(provider_name, primary, upgrade_fact)
    if answer is None:
        return None

    return (
        answer + format_clause_suffix(clauses, prefer_zh=prefer_zh),
        {
            "type": "chronic_condition_rule_single",
            "provider": primary.provider,
            "display_name": provider_name,
            "primary_rule_kind": primary.rule_kind,
            "age_threshold": primary.age_threshold,
            "full_coverage": primary.full_coverage,
            "first_policy_year_only": primary.first_policy_year_only,
            "excluded_after_renewal": primary.excluded_after_renewal,
            "waiting_period_required": primary.waiting_period_required,
            "upgrade_rule_kind": upgrade_fact.rule_kind if upgrade_fact is not None else None,
            "additional_coverage_subject_to_age_rule": upgrade_fact.additional_coverage_subject_to_age_rule if upgrade_fact else False,
            "original_coverage_unaffected": upgrade_fact.original_coverage_unaffected if upgrade_fact else False,
            "clauses": clauses,
            "source_name": ", ".join(sorted({fact.source_name for fact in facts if fact.source_name})),
        },
    )


def build_general_chronic_condition_answer(
    intent: QueryIntent,
    facts: list[ChronicConditionRuleFact],
    prefer_zh: bool,
) -> tuple[str, dict[str, Any]] | None:
    age_4 = next((fact for fact in facts if fact.rule_kind == "age_4_or_below"), None)
    age_5 = next((fact for fact in facts if fact.rule_kind == "age_5_or_above"), None)
    if age_4 is None or age_5 is None:
        return None

    provider_name = provider_display_name(age_4.provider or age_5.provider)
    upgrade_fact = select_upgrade_chronic_condition_fact(facts)
    clause_parts: list[str] = []
    for clause in (age_4.clauses, age_5.clauses, upgrade_fact.clauses if upgrade_fact is not None else ""):
        clause = clause.strip()
        if clause and clause not in clause_parts:
            clause_parts.append(clause)
    clauses = ", ".join(clause_parts)

    if prefer_zh:
        answer = (
            f"{provider_name} 的慢性病況規則可分為兩個年齡分支："
            f"若寵物在首次投保或升級至更高年度保障總額計劃的續保日（以較後者為準）為 4 歲或以下，"
            "並且在適用等候期完結前未曾因相關慢性病況出現症狀、確診、用藥、接受醫療建議或治療，則可獲全面保障；"
            f"若寵物在首次投保或升級至更高年度保障總額計劃的續保日（以較早者為準）已屆 5 歲或以上，"
            "相關慢性病況只會在首次出現症狀、確診、用藥、接受醫療建議或治療的保單年度受保，續保後將不再受保"
        )
        if upgrade_fact is not None and upgrade_fact.additional_coverage_subject_to_age_rule:
            answer += "。若升級發生在寵物滿 5 歲或之後，這個年齡相關條件會套用到新增保障，但原保障不受影響"
    else:
        answer = (
            f"{provider_name} has two chronic-condition branches: "
            "if the pet is 4 years old or below on the later of the first Policy Start Date or the renewal date on which the policy is upgraded "
            "to a higher Annual Limit plan, and no related symptoms, diagnosis, medication, advice, or treatment occurred before the applicable waiting period ended, "
            "those chronic conditions are fully covered; if the pet is 5 years old or above on the earlier of the first Policy Start Date or the renewal date of such upgrade, "
            "the related chronic conditions are only covered in the policy year when they first arise and are excluded from subsequent renewals"
        )
        if upgrade_fact is not None and upgrade_fact.additional_coverage_subject_to_age_rule:
            answer += ". If the upgrade happens on or after the pet turning 5 years old, this age-related rule applies to the Additional Coverage while the Original Coverage is unaffected"

    return (
        answer + format_clause_suffix(clauses, prefer_zh=prefer_zh),
        {
            "type": "chronic_condition_rule_summary",
            "provider": age_4.provider or age_5.provider,
            "display_name": provider_name,
            "clauses": clauses,
            "has_age_4_or_below_rule": True,
            "has_age_5_or_above_rule": True,
            "upgrade_rule_kind": upgrade_fact.rule_kind if upgrade_fact is not None else None,
            "additional_coverage_subject_to_age_rule": upgrade_fact.additional_coverage_subject_to_age_rule if upgrade_fact else False,
            "original_coverage_unaffected": upgrade_fact.original_coverage_unaffected if upgrade_fact else False,
            "source_name": ", ".join(sorted({fact.source_name for fact in facts if fact.source_name})),
        },
    )


def collect_waiting_period_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[WaitingPeriodFact]:
    facts: list[WaitingPeriodFact] = []
    seen: set[tuple[str, str]] = set()
    for node in nodes:
        fact = extract_waiting_period_fact(intent, node)
        if fact is None:
            continue
        key = (fact.provider, fact.clauses)
        if key in seen:
            continue
        seen.add(key)
        facts.append(fact)
    return facts


def collect_chronic_condition_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[ChronicConditionRuleFact]:
    facts: list[ChronicConditionRuleFact] = []
    seen: set[tuple[str, str, str]] = set()
    for node in nodes:
        fact = extract_chronic_condition_fact(intent, node)
        if fact is None:
            continue
        key = (fact.provider, fact.clauses, fact.rule_kind)
        if key in seen:
            continue
        seen.add(key)
        facts.append(fact)
    return facts


def extract_chronic_condition_fact(
    intent: QueryIntent,
    node: NodeWithScore,
) -> ChronicConditionRuleFact | None:
    metadata = node.node.metadata or {}
    provider = metadata.get("provider", "").strip().lower()
    if not provider:
        return None
    text = node.node.text.strip()
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    if "eligibility" not in unit_types and "renewal_rule" not in unit_types:
        return None
    if not contains_any(lower_text, "慢性病況", "慢性病况", "慢性疾病", "chronic medical conditions", "chronic condition"):
        return None

    if contains_any(lower_text, "4 歲或以下", "4 years old or below", "age 4 or below", "4 or below"):
        return ChronicConditionRuleFact(
            provider=provider,
            clauses=metadata.get("clauses", ""),
            source_name=metadata.get("source_name", ""),
            rule_kind="age_4_or_below",
            age_threshold=4,
            full_coverage=contains_any(lower_text, "全面保障", "will cover the above chronic medical conditions", "fully covered"),
            waiting_period_required=contains_any(lower_text, "等候期", "waiting period"),
        )

    if contains_any(lower_text, "5 歲或以上", "5 years old or above", "age 5 or above", "5 or above"):
        return ChronicConditionRuleFact(
            provider=provider,
            clauses=metadata.get("clauses", ""),
            source_name=metadata.get("source_name", ""),
            rule_kind="age_5_or_above",
            age_threshold=5,
            first_policy_year_only=contains_any(lower_text, "僅於", "only provide coverage", "only covered in the policy year"),
            excluded_after_renewal=contains_any(lower_text, "續保後", "不再受保", "excluded from the subsequent renewal policies", "excluded from subsequent renewals", "subsequent renewal"),
            waiting_period_required=contains_any(lower_text, "等候期", "waiting period"),
        )

    if contains_any(lower_text, "原保障不受影響", "original coverage is unaffected") and contains_any(lower_text, "新增保障", "additional coverage"):
        return ChronicConditionRuleFact(
            provider=provider,
            clauses=metadata.get("clauses", ""),
            source_name=metadata.get("source_name", ""),
            rule_kind="upgrade_additional_coverage",
            age_threshold=5 if contains_any(lower_text, "滿 5 歲", "turning 5 years old") else None,
            waiting_period_required=contains_any(lower_text, "等候期", "waiting period"),
            additional_coverage_subject_to_age_rule=True,
            original_coverage_unaffected=True,
        )

    return None


def select_primary_chronic_condition_fact(
    intent: QueryIntent,
    facts: list[ChronicConditionRuleFact],
) -> ChronicConditionRuleFact | None:
    if not facts:
        return None
    wants_age_5 = contains_any(intent.normalized_question, "5 歲", "5岁", "5 years old", "5 year old", "5 or above", "5歲或以上", "5岁或以上")
    wants_age_4 = contains_any(intent.normalized_question, "4 歲", "4岁", "4 years old", "4 year old", "4 or below", "4歲或以下", "4岁或以下")
    if wants_age_5:
        for fact in facts:
            if fact.rule_kind == "age_5_or_above":
                return fact
    if wants_age_4:
        for fact in facts:
            if fact.rule_kind == "age_4_or_below":
                return fact
    for preferred in ("age_5_or_above", "age_4_or_below"):
        for fact in facts:
            if fact.rule_kind == preferred:
                return fact
    return facts[0]


def select_upgrade_chronic_condition_fact(
    facts: list[ChronicConditionRuleFact],
) -> ChronicConditionRuleFact | None:
    for fact in facts:
        if fact.rule_kind == "upgrade_additional_coverage":
            return fact
    return None


def best_chronic_condition_node(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
    preferred_rule_kind: str | None = None,
) -> NodeWithScore | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        fact = extract_chronic_condition_fact(intent, node)
        if fact is None:
            continue
        if preferred_rule_kind is not None and fact.rule_kind != preferred_rule_kind:
            continue
        ranked.append((chronic_condition_answer_score(intent, node), rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return ranked[0][2]


def preferred_chronic_rule_kind(intent: QueryIntent) -> str | None:
    normalized = intent.normalized_question
    if contains_any(normalized, "5 歲", "5岁", "5 years old", "5 year old", "5 or above", "5歲或以上", "5岁或以上"):
        return "age_5_or_above"
    if contains_any(normalized, "4 歲", "4岁", "4 years old", "4 year old", "4 or below", "4歲或以下", "4岁或以下"):
        return "age_4_or_below"
    return None


def chronic_condition_query_is_general(intent: QueryIntent) -> bool:
    if not intent.asks_chronic_condition:
        return False
    if preferred_chronic_rule_kind(intent) is not None:
        return False
    return contains_any(
        intent.normalized_question,
        "慢性病況",
        "慢性病况",
        "慢性疾病",
        "chronic medical conditions",
        "chronic condition",
    )


def extract_addon_waiting_period_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> AddonWaitingPeriodFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_waiting_period_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    addon_label = addon_cash_benefit_label(intent, text)
    return AddonWaitingPeriodFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        addon_label=addon_label,
        days=extract_waiting_period_days(text, "general"),
    )


def extract_addon_eligibility_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> AddonEligibilityFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_eligibility_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    min_age, max_age = extract_age_range(text)
    renewal_cutoff_age = extract_nonrenewable_age(text)
    renewal_no_age_limit = detect_no_age_limit_for_renewal(text)
    return AddonEligibilityFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        addon_label=addon_cash_benefit_label(intent, text),
        min_age=min_age,
        max_age=max_age,
        renewal_cutoff_age=renewal_cutoff_age,
        renewal_no_age_limit=renewal_no_age_limit,
        new_purchase_only=contains_any(normalize_text(text), "for new purchase", "新投保", "投保"),
    )


def extract_addon_conditions_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> AddonEligibilityConditionsFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = addon_conditions_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    conditions = extract_structured_conditions(text)
    return AddonEligibilityConditionsFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        addon_label=addon_cash_benefit_label(intent, text),
        conditions=tuple(conditions),
    )


def extract_renewal_upgrade_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> RenewalRuleFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = renewal_upgrade_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    guaranteed_age, approval_age_threshold = extract_renewal_ages(text)
    return RenewalRuleFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        auto_renew=detect_auto_renew(lower_text),
        guaranteed_age=guaranteed_age,
        approval_age_threshold=approval_age_threshold,
        can_upgrade=detect_can_upgrade(lower_text),
        upgrade_waiting_period=detect_upgrade_waiting_period(lower_text),
        downgrade_no_waiting=detect_downgrade_no_waiting(lower_text),
        age_upgrade_block=extract_upgrade_age_block(text),
    )


def extract_upgrade_age_limit_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> RenewalRuleFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        if extract_upgrade_age_block(node.node.text) is None:
            continue
        score = renewal_upgrade_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    guaranteed_age, approval_age_threshold = extract_renewal_ages(text)
    return RenewalRuleFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        auto_renew=detect_auto_renew(lower_text),
        guaranteed_age=guaranteed_age,
        approval_age_threshold=approval_age_threshold,
        can_upgrade=detect_can_upgrade(lower_text),
        upgrade_waiting_period=detect_upgrade_waiting_period(lower_text),
        downgrade_no_waiting=detect_downgrade_no_waiting(lower_text),
        age_upgrade_block=extract_upgrade_age_block(text),
    )


def extract_waiting_period_fact(
    intent: QueryIntent,
    node: NodeWithScore,
) -> WaitingPeriodFact | None:
    metadata = node.node.metadata or {}
    provider = metadata.get("provider", "").strip().lower()

    topic_tags = metadata_tags(metadata)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    if "waiting_period" not in topic_tags and "waiting_period" not in unit_types and "definition" not in unit_types:
        return None

    text = node.node.text.strip()
    lower_text = normalize_text(text)
    no_waiting = contains_any(lower_text, "不設等候期", "不设等候期", "no waiting period")
    cancer_days = extract_waiting_period_days(text, "cancer")
    injury_days = extract_waiting_period_days(text, "injury")
    illness_days = extract_waiting_period_days(text, "illness")
    general_days = extract_waiting_period_days(text, "general")

    if not no_waiting and all(value is None for value in (cancer_days, injury_days, illness_days, general_days)):
        return None

    return WaitingPeriodFact(
        provider=provider,
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        cancer_days=cancer_days,
        injury_days=injury_days,
        illness_days=illness_days,
        general_days=general_days,
        no_waiting_period=no_waiting,
    )


def build_waiting_period_comparison_answer(
    intent: QueryIntent,
    facts: list[WaitingPeriodFact],
) -> tuple[str, dict[str, Any]] | None:
    prefer_zh = question_prefers_zh(intent.raw_question)
    by_provider = {fact.provider: fact for fact in facts}
    lines: list[str] = []
    structured_facts: list[dict[str, Any]] = []

    for provider_name in intent.target_providers:
        fact = by_provider.get(provider_name)
        if fact is None:
            lines.append(format_missing_evidence_line(provider_name, prefer_zh))
            structured_facts.append(
                {
                    "provider": provider_name,
                    "display_name": provider_display_name(provider_name),
                    "status": "missing_evidence",
                }
            )
            continue

        label, days = best_waiting_period_value(intent, fact)
        if days is None and not fact.no_waiting_period:
            lines.append(format_partial_evidence_line(provider_name, prefer_zh))
            structured_facts.append(
                {
                    "provider": provider_name,
                    "display_name": provider_display_name(provider_name),
                    "status": "partial_evidence",
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                }
            )
            continue

        lines.append(format_waiting_period_fact_line(provider_name, label, days, fact, prefer_zh))
        structured_facts.append(
            {
                "provider": provider_name,
                "display_name": provider_display_name(provider_name),
                "status": "ok",
                "label": label,
                "days": days,
                "no_waiting_period": fact.no_waiting_period,
                "clauses": fact.clauses,
                "source_name": fact.source_name,
            }
        )

    if not lines:
        return None

    lead = "根據目前檢索到的保單條款：" if prefer_zh else "Based on the policy clauses retrieved so far:"
    return (
        lead + "\n" + "\n".join(lines),
        {
            "type": "waiting_period_comparison",
            "facts": structured_facts,
        },
    )


def build_single_provider_waiting_period_answer(
    intent: QueryIntent,
    fact: WaitingPeriodFact,
) -> tuple[str, dict[str, Any]] | None:
    prefer_zh = question_prefers_zh(intent.raw_question)
    label, days = best_waiting_period_value(intent, fact)
    if days is None and not fact.no_waiting_period:
        return None

    provider_name = provider_display_name(fact.provider)
    text = format_single_waiting_period_line(provider_name, label, days, fact, prefer_zh)
    return (
        text,
        {
            "type": "waiting_period_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "label": label,
            "days": days,
            "no_waiting_period": fact.no_waiting_period,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_deterministic_pre_existing_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    fact = extract_pre_existing_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if prefer_zh:
        answer = (
            f"{provider_name} 的保單對投保前已存在病況"
            f"{'不受保' if fact.excluded else '未能從目前證據確認是否受保'}"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
        if fact.excluded:
            answer = f"{provider_name} 的保單明確將投保前已存在病況列為不保事項{format_clause_suffix(fact.clauses, prefer_zh=True)}"
    else:
        if fact.excluded:
            answer = f"{provider_name} explicitly excludes pre-existing conditions{format_clause_suffix(fact.clauses, prefer_zh=False)}"
        else:
            answer = f"{provider_name} pre-existing condition coverage could not be confirmed from the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"

    return (
        answer,
        {
            "type": "pre_existing_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "excluded": fact.excluded,
            "definition_like": fact.definition_like,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_deterministic_generic_exclusion_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    fact = extract_generic_exclusion_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    label = format_exclusion_label(fact.matched_label)
    if prefer_zh:
        if fact.excluded:
            answer = f"{provider_name} 的保單明確將「{label}」列為不保事項{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        else:
            answer = f"{provider_name} 對「{label}」的保障範圍未能從目前證據確認{format_clause_suffix(fact.clauses, prefer_zh=True)}"
    else:
        if fact.excluded:
            answer = f"{provider_name} explicitly excludes {label}{format_clause_suffix(fact.clauses, prefer_zh=False)}"
        else:
            answer = f"{provider_name} coverage for {label} could not be confirmed from the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"

    return (
        answer,
        {
            "type": "generic_exclusion_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "excluded": fact.excluded,
            "matched_label": label,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_deterministic_consult_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    facts = extract_consult_facts(intent, nodes)
    if not facts:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(facts[0].provider)
    consultation_label = (
        merge_consultation_labels(facts, prefer_zh)
        if question_asks_specific_consult_subtype(intent)
        else generic_consultation_label(prefer_zh)
    )
    if prefer_zh:
        answer = f"{provider_name} 的保單涵蓋{consultation_label}{format_clause_suffix(join_fact_clauses(facts), prefer_zh=True)}"
    else:
        answer = f"{provider_name} covers {consultation_label}{format_clause_suffix(join_fact_clauses(facts), prefer_zh=False)}"

    return (
        answer,
        {
            "type": "consult_coverage_single",
            "provider": facts[0].provider,
            "display_name": provider_name,
            "covered": all(fact.covered for fact in facts),
            "consultation_label": consultation_label,
            "clauses": join_fact_clauses(facts),
            "source_name": ", ".join(sorted({fact.source_name for fact in facts if fact.source_name})),
        },
    )


def build_deterministic_consult_limit_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    if intent.wants_comparison and intent.target_providers:
        facts = collect_consult_limit_facts(intent, nodes)
        if not facts:
            return None
        return build_consult_limit_comparison_answer(intent, facts)

    fact = extract_consult_limit_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if not fact.has_explicit_limit:
        if prefer_zh:
            answer = f"{provider_name} 的{fact.consultation_label}條款已檢索到，但目前證據內沒有明確的最高賠償額數字{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        else:
            answer = f"{provider_name} {fact.consultation_label} clause was retrieved, but no explicit limit amount was found in the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    else:
        if prefer_zh:
            answer = format_consult_limit_answer_zh(provider_name, fact)
        else:
            answer = format_consult_limit_answer_en(provider_name, fact)

    return (
        answer,
        {
            "type": "consult_limit_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "consultation_label": fact.consultation_label,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
            "plan_limits": fact.plan_limits,
            "has_explicit_limit": fact.has_explicit_limit,
        },
    )


def collect_consult_limit_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[ConsultLimitFact]:
    ranked: list[tuple[float, float, ConsultLimitFact]] = []
    prefer_zh = question_prefers_zh(intent.raw_question)
    for node in nodes:
        score = consult_answer_score(intent, node)
        if score <= 0.0:
            continue
        metadata = node.node.metadata or {}
        provider = metadata.get("provider", "").strip().lower()
        if not provider:
            continue
        text = node.node.text
        plan_limits = extract_plan_limits(text)
        ranked.append(
            (
                score,
                rerank_score(intent, node),
                ConsultLimitFact(
                    provider=provider,
                    clauses=metadata.get("clauses", ""),
                    source_name=metadata.get("source_name", ""),
                    consultation_label=consultation_label_for_text(normalize_text(text), prefer_zh),
                    plan_limits=plan_limits,
                    has_explicit_limit=bool(plan_limits),
                ),
            )
        )

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    by_provider: dict[str, ConsultLimitFact] = {}
    for _, _, fact in ranked:
        by_provider.setdefault(fact.provider, fact)

    if intent.target_providers:
        return [by_provider[provider] for provider in intent.target_providers if provider in by_provider]
    return list(by_provider.values())


def build_consult_limit_comparison_answer(
    intent: QueryIntent,
    facts: list[ConsultLimitFact],
) -> tuple[str, dict[str, Any]] | None:
    prefer_zh = question_prefers_zh(intent.raw_question)
    by_provider = {fact.provider: fact for fact in facts}
    lines: list[str] = []
    structured_facts: list[dict[str, Any]] = []

    for provider_name in intent.target_providers:
        fact = by_provider.get(provider_name)
        if fact is None:
            lines.append(format_missing_evidence_line(provider_name, prefer_zh))
            structured_facts.append(
                {
                    "provider": provider_name,
                    "display_name": provider_display_name(provider_name),
                    "status": "missing_evidence",
                }
            )
            continue

        if not fact.has_explicit_limit:
            lines.append(format_consult_limit_partial_line(provider_name, fact, prefer_zh))
            structured_facts.append(
                {
                    "provider": fact.provider,
                    "display_name": provider_display_name(fact.provider),
                    "status": "partial_evidence",
                    "consultation_label": fact.consultation_label,
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                    "has_explicit_limit": False,
                }
            )
            continue

        lines.append(format_consult_limit_fact_line(provider_name, fact, prefer_zh))
        structured_facts.append(
            {
                "provider": fact.provider,
                "display_name": provider_display_name(fact.provider),
                "status": "ok",
                "consultation_label": fact.consultation_label,
                "clauses": fact.clauses,
                "source_name": fact.source_name,
                "plan_limits": fact.plan_limits,
                "has_explicit_limit": True,
            }
        )

    if not lines:
        return None

    lead = "根據目前檢索到的保單條款：" if prefer_zh else "Based on the policy clauses retrieved so far:"
    return (
        lead + "\n" + "\n".join(lines),
        {
            "type": "consult_limit_comparison",
            "facts": structured_facts,
        },
    )


def build_deterministic_generic_limit_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    if intent.wants_comparison and intent.target_providers:
        facts = collect_generic_limit_facts(intent, nodes)
        if not facts:
            return None
        return build_generic_limit_comparison_answer(intent, facts)

    fact = extract_generic_limit_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if not fact.has_explicit_limit:
        if prefer_zh:
            answer = (
                f"{provider_name} 的{fact.benefit_label}條款已檢索到，但目前證據內沒有明確的最高賠償額數字"
                f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
            )
        else:
            answer = (
                f"{provider_name} {fact.benefit_label} clause was retrieved, but no explicit limit amount was found "
                f"in the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"
            )
    else:
        if prefer_zh:
            answer = format_generic_limit_answer_zh(provider_name, fact)
        else:
            answer = format_generic_limit_answer_en(provider_name, fact)

    return (
        answer,
        {
            "type": "benefit_limit_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "benefit_label": fact.benefit_label,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
            "plan_limits": fact.plan_limits,
            "has_explicit_limit": fact.has_explicit_limit,
        },
    )


def collect_generic_limit_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[BenefitLimitFact]:
    ranked: list[tuple[float, float, BenefitLimitFact]] = []
    prefer_zh = question_prefers_zh(intent.raw_question)
    for node in nodes:
        score = generic_limit_answer_score(intent, node)
        if score <= 0.0:
            continue
        metadata = node.node.metadata or {}
        provider = metadata.get("provider", "").strip().lower()
        if not provider:
            continue
        text = node.node.text
        ranked.append(
            (
                score,
                rerank_score(intent, node),
                BenefitLimitFact(
                    provider=provider,
                    clauses=metadata.get("clauses", ""),
                    source_name=metadata.get("source_name", ""),
                    benefit_label=benefit_label_for_text(text, prefer_zh),
                    plan_limits=extract_plan_limits(text),
                    has_explicit_limit=bool(extract_plan_limits(text)),
                ),
            )
        )

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    by_provider: dict[str, BenefitLimitFact] = {}
    for _, _, fact in ranked:
        by_provider.setdefault(fact.provider, fact)

    if intent.target_providers:
        return [by_provider[provider] for provider in intent.target_providers if provider in by_provider]
    return list(by_provider.values())


def build_generic_limit_comparison_answer(
    intent: QueryIntent,
    facts: list[BenefitLimitFact],
) -> tuple[str, dict[str, Any]] | None:
    prefer_zh = question_prefers_zh(intent.raw_question)
    by_provider = {fact.provider: fact for fact in facts}
    lines: list[str] = []
    structured_facts: list[dict[str, Any]] = []

    for provider_name in intent.target_providers:
        fact = by_provider.get(provider_name)
        if fact is None:
            lines.append(format_missing_evidence_line(provider_name, prefer_zh))
            structured_facts.append(
                {
                    "provider": provider_name,
                    "display_name": provider_display_name(provider_name),
                    "status": "missing_evidence",
                }
            )
            continue

        if not fact.has_explicit_limit:
            lines.append(format_generic_limit_partial_line(provider_name, fact, prefer_zh))
            structured_facts.append(
                {
                    "provider": fact.provider,
                    "display_name": provider_display_name(fact.provider),
                    "status": "partial_evidence",
                    "benefit_label": fact.benefit_label,
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                    "has_explicit_limit": False,
                }
            )
            continue

        lines.append(format_generic_limit_fact_line(provider_name, fact, prefer_zh))
        structured_facts.append(
            {
                "provider": fact.provider,
                "display_name": provider_display_name(fact.provider),
                "status": "ok",
                "benefit_label": fact.benefit_label,
                "clauses": fact.clauses,
                "source_name": fact.source_name,
                "plan_limits": fact.plan_limits,
                "has_explicit_limit": True,
            }
        )

    if not lines:
        return None

    lead = "根據目前檢索到的保單條款：" if prefer_zh else "Based on the policy clauses retrieved so far:"
    return (
        lead + "\n" + "\n".join(lines),
        {
            "type": "benefit_limit_comparison",
            "facts": structured_facts,
        },
    )


def build_deterministic_markdown_table_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    facts = collect_markdown_table_facts(intent, nodes)
    if not facts:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(facts[0].provider)
    if should_build_markdown_table_overview_answer(intent, facts):
        return build_markdown_table_overview_answer(intent, facts, provider_name, prefer_zh)
    if should_build_multi_markdown_table_answer(intent, facts):
        return build_multi_markdown_table_answer(intent, facts, provider_name, prefer_zh)
    fact = facts[0]
    if prefer_zh:
        answer = (
            f"{provider_name} 在「{fact.row_label}」對應的「{fact.column_label}」為 {fact.value}"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
    else:
        answer = (
            f"{provider_name} {fact.column_label} for {fact.row_label} is {fact.value}"
            f"{format_clause_suffix(fact.clauses, prefer_zh=False)}"
        )

    return (
        answer,
        {
            "type": "markdown_table_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "row_label": fact.row_label,
            "column_label": fact.column_label,
            "value": fact.value,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
        },
    )


def build_multi_markdown_table_answer(
    intent: QueryIntent,
    facts: list[MarkdownTableFact],
    provider_name: str,
    prefer_zh: bool,
) -> tuple[str, dict[str, Any]]:
    clauses = ", ".join(sorted({fact.clauses for fact in facts if fact.clauses}))
    if prefer_zh:
        parts = [f"「{fact.row_label}」為 {fact.value}" for fact in facts]
        answer = f"{provider_name} 的「{facts[0].column_label}」分別為：" + "；".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=True)
    else:
        parts = [f"{fact.row_label} is {fact.value}" for fact in facts]
        answer = f"{provider_name} {facts[0].column_label} values are: " + "; ".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=False)
    return (
        answer,
        {
            "type": "markdown_table_multi",
            "provider": facts[0].provider,
            "display_name": provider_name,
            "column_label": facts[0].column_label,
            "facts": [
                {
                    "row_label": fact.row_label,
                    "value": fact.value,
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                }
                for fact in facts
            ],
        },
    )


def build_markdown_table_overview_answer(
    intent: QueryIntent,
    facts: list[MarkdownTableFact],
    provider_name: str,
    prefer_zh: bool,
) -> tuple[str, dict[str, Any]]:
    clauses = ", ".join(sorted({fact.clauses for fact in facts if fact.clauses}))
    if prefer_zh:
        parts = [f"「{fact.row_label}」為 {fact.value}" for fact in facts]
        answer = f"{provider_name} 的「{facts[0].column_label}」如下：" + "；".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=True)
    else:
        parts = [f"{fact.row_label} is {fact.value}" for fact in facts]
        answer = f"{provider_name} {facts[0].column_label} values are as follows: " + "; ".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=False)
    return (
        answer,
        {
            "type": "markdown_table_overview",
            "provider": facts[0].provider,
            "display_name": provider_name,
            "column_label": facts[0].column_label,
            "facts": [
                {
                    "row_label": fact.row_label,
                    "value": fact.value,
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                }
                for fact in facts
            ],
        },
    )


def build_deterministic_cost_share_answer(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> tuple[str, dict[str, Any]] | None:
    facts = collect_cost_share_facts(intent, nodes)
    if should_build_multi_cost_share_answer(intent, facts):
        return build_multi_cost_share_answer(intent, facts)

    fact = extract_cost_share_fact(intent, nodes)
    if fact is None:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(fact.provider)
    if prefer_zh:
        label = fact.surface_label or cost_share_label_zh(fact.kind)
        if fact.value:
            answer = f"{provider_name} 的{label}為 {fact.value}{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        else:
            answer = f"{provider_name} 的{label}條款已檢索到，但目前證據未載明具體數值{format_clause_suffix(fact.clauses, prefer_zh=True)}"
            if fact.note:
                answer += f" {fact.note}"
    else:
        label = fact.surface_label or cost_share_label_en(fact.kind)
        if fact.value:
            answer = f"{provider_name} {label} is {fact.value}{format_clause_suffix(fact.clauses, prefer_zh=False)}"
        else:
            answer = f"{provider_name} {label} clause was retrieved, but the current evidence does not include a specific numeric value{format_clause_suffix(fact.clauses, prefer_zh=False)}"
            if fact.note:
                answer += f" {fact.note}"

    return (
        answer,
        {
            "type": "cost_share_single",
            "provider": fact.provider,
            "display_name": provider_name,
            "kind": fact.kind,
            "value": fact.value,
            "scope": fact.scope,
            "clauses": fact.clauses,
            "source_name": fact.source_name,
            "note": fact.note,
            "surface_label": fact.surface_label,
        },
    )


def build_multi_cost_share_answer(
    intent: QueryIntent,
    facts: list[CostShareFact],
) -> tuple[str, dict[str, Any]] | None:
    if not facts:
        return None

    prefer_zh = question_prefers_zh(intent.raw_question)
    provider_name = provider_display_name(facts[0].provider)
    label = facts[0].surface_label or (cost_share_label_zh(facts[0].kind) if prefer_zh else cost_share_label_en(facts[0].kind))
    clauses = ", ".join(fact.clauses for fact in facts if fact.clauses)
    if prefer_zh:
        parts = [
            f"{fact.scope or '相關條款'}為 {render_cost_share_value_phrase(fact, prefer_zh=True)}"
            for fact in facts
            if fact.value
        ]
        answer = f"{provider_name} 的{label}按不同保障項目分別為："
        answer += "；".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=True)
    else:
        parts = [
            f"{fact.scope or 'relevant clause'} is {render_cost_share_value_phrase(fact, prefer_zh=False)}"
            for fact in facts
            if fact.value
        ]
        answer = f"{provider_name} {label} differs by coverage section: "
        answer += "; ".join(parts)
        answer += format_clause_suffix(clauses, prefer_zh=False)

    return (
        answer,
        {
            "type": "cost_share_multi",
            "provider": facts[0].provider,
            "display_name": provider_name,
            "kind": facts[0].kind,
            "facts": [
                {
                    "scope": fact.scope,
                    "value": fact.value,
                    "clauses": fact.clauses,
                    "source_name": fact.source_name,
                    "note": fact.note,
                    "surface_label": fact.surface_label,
                }
                for fact in facts
            ],
        },
    )


def should_build_multi_cost_share_answer(intent: QueryIntent, facts: list[CostShareFact]) -> bool:
    if len(facts) < 2:
        return False
    requested_kind = cost_share_kind_requested(intent)
    if requested_kind != "deductible":
        return False
    providers = {fact.provider for fact in facts if fact.provider}
    kinds = {fact.kind for fact in facts}
    values = {fact.value for fact in facts if fact.value}
    return len(providers) == 1 and kinds == {"deductible"} and len(values) >= 2


def extract_cost_share_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> CostShareFact | None:
    facts = collect_cost_share_facts(intent, nodes)
    if not facts:
        return None
    return facts[0]


def cost_share_fact_from_node(
    intent: QueryIntent,
    node: NodeWithScore,
) -> CostShareFact | None:
    metadata = node.node.metadata or {}
    text = node.node.text
    kind = infer_cost_share_kind_from_node(metadata, text)
    if kind in {"", "mixed"}:
        return None
    requested_kind = cost_share_kind_requested(intent)
    if not node_matches_cost_share_kind(requested_kind, kind):
        return None

    value = extract_cost_share_value(kind, text)
    direct = has_direct_cost_share_statement(kind, text)
    note = cost_share_note_for(kind, text, question_prefers_zh(intent.raw_question))
    if not value and not direct and not note:
        return None

    return CostShareFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        kind=kind,
        value=value,
        scope=extract_cost_share_scope(metadata, text),
        note=note,
        surface_label=cost_share_surface_label(
            kind,
            text,
            prefer_zh=question_prefers_zh(intent.raw_question),
            question=intent.raw_question,
        ),
    )


def select_cost_share_dependency_support_node(
    intent: QueryIntent,
    primary: NodeWithScore,
    candidates: list[NodeWithScore],
) -> NodeWithScore | None:
    primary_fact = cost_share_fact_from_node(intent, primary)
    if primary_fact is None or primary_fact.value:
        return None

    primary_metadata = primary.node.metadata or {}
    dependencies = metadata_cost_share_value_dependencies(primary_metadata)
    if not dependencies:
        return None

    primary_key = node_key(primary)
    primary_provider = str(primary_metadata.get("provider", "")).strip().lower()
    best: tuple[float, NodeWithScore] | None = None
    for candidate in candidates:
        if node_key(candidate) == primary_key:
            continue
        metadata = candidate.node.metadata or {}
        provider = str(metadata.get("provider", "")).strip().lower()
        if primary_provider and provider and provider != primary_provider:
            continue
        score = cost_share_dependency_support_score(candidate, dependencies)
        if score <= 0.0:
            continue
        if best is None or score > best[0]:
            best = (score, candidate)
    return best[1] if best else None


def cost_share_dependency_support_score(node: NodeWithScore, dependencies: set[str]) -> float:
    metadata = node.node.metadata or {}
    node_dependencies = metadata_cost_share_value_dependencies(metadata)
    text = normalize_text(node.node.text)
    topic_tags = metadata_tags(metadata)
    definition_labels = {
        part.strip().lower()
        for part in str(metadata.get("definition_labels", "")).split(",")
        if part.strip()
    }
    unit_types = {
        part.strip().lower()
        for part in str(metadata.get("unit_types", "")).split(",")
        if part.strip()
    }

    score = 0.0
    score += 2.4 * len(dependencies & node_dependencies)

    if "table_of_benefits" in dependencies and (
        "table of benefits" in definition_labels
        or "table_of_benefits" in topic_tags
        or "benefit_table" in unit_types
        or contains_any(text, "table of benefits", "保障項目表")
    ):
        score += 1.8
    if "policy_schedule" in dependencies and (
        "policy schedule" in definition_labels
        or "policy_schedule" in topic_tags
        or contains_any(text, "policy schedule", "承保表", "保單承保表", "保单承保表")
    ):
        score += 1.8

    if metadata_cost_share_evidence(metadata) == "table_reference":
        score += 0.8
    if metadata_flag(metadata, "cost_share_mentions_table"):
        score += 0.4

    return score


def collect_cost_share_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[CostShareFact]:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = cost_share_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return []

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    requested_kind = cost_share_kind_requested(intent)
    facts: list[CostShareFact] = []
    seen: set[tuple[str, str, str, str]] = set()
    for _, _, node in ranked:
        fact = cost_share_fact_from_node(intent, node)
        if fact is None:
            continue
        if requested_kind and fact.kind != requested_kind:
            continue
        key = (fact.provider, fact.kind, fact.scope or "", fact.value or "")
        if key in seen:
            continue
        seen.add(key)
        facts.append(fact)
    return facts


def extract_cost_share_scope(metadata: dict[str, Any], text: str) -> str | None:
    headings: list[str] = []
    for raw_line in text.splitlines():
        match = re.match(r"^#{3,6}\s+(.*\S)\s*$", raw_line.strip())
        if not match:
            continue
        heading = match.group(1).strip()
        normalized = normalize_text(heading)
        if contains_any(normalized, "保障項目", "benefits", "條件", "conditions", "不保項目", "exclusions", "定義", "definitions"):
            continue
        if contains_any(normalized, "等候期", "waiting period"):
            continue
        headings.append(heading)
    if headings:
        return headings[0]

    section_path = str(metadata.get("section_path", "")).strip()
    if section_path:
        parts = [part.strip() for part in section_path.split(">") if part.strip()]
        for part in reversed(parts):
            normalized = normalize_text(part)
            if contains_any(normalized, "保障項目", "benefits", "條件", "conditions", "不保項目", "exclusions", "定義", "definitions"):
                continue
            return part
    return None


def render_cost_share_value_phrase(fact: CostShareFact, prefer_zh: bool) -> str:
    if not fact.value:
        return "未載明具體數值" if prefer_zh else "no specific numeric value"
    lower_text = normalize_text(fact.scope or "")
    if fact.kind == "deductible" and fact.value.startswith("港幣") and contains_any(lower_text, "項目二", "third party", "第三者"):
        return f"每宗索償{fact.value}" if prefer_zh else f"{fact.value} per claim"
    return fact.value


def extract_consult_limit_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> ConsultLimitFact | None:
    primary = best_consult_node(intent, nodes)
    if primary is None:
        return None
    metadata = primary.node.metadata or {}
    text = primary.node.text
    plan_limits = extract_plan_limits(text)
    return ConsultLimitFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        consultation_label=consultation_label_for_text(normalize_text(text), question_prefers_zh(intent.raw_question)),
        plan_limits=plan_limits,
        has_explicit_limit=bool(plan_limits),
    )


def extract_generic_limit_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> BenefitLimitFact | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = generic_limit_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    node = ranked[0][2]
    metadata = node.node.metadata or {}
    text = node.node.text
    plan_limits = extract_plan_limits(text)
    return BenefitLimitFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        benefit_label=benefit_label_for_text(text, question_prefers_zh(intent.raw_question)),
        plan_limits=plan_limits,
        has_explicit_limit=bool(plan_limits),
    )


def extract_markdown_table_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> MarkdownTableFact | None:
    facts = collect_markdown_table_facts(intent, nodes)
    if not facts:
        return None
    return facts[0]


def collect_markdown_table_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[MarkdownTableFact]:
    ranked: list[tuple[float, float, MarkdownTableFact]] = []
    seen: set[tuple[str, str, str, str]] = set()
    for node in nodes:
        scored_facts = markdown_table_facts_from_node(intent, node)
        for score, fact in scored_facts:
            key = (fact.provider, fact.clauses, fact.row_label, fact.column_label)
            if key in seen:
                continue
            seen.add(key)
            ranked.append((score, rerank_score(intent, node), fact))
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [fact for _, _, fact in ranked]


def markdown_table_fact_from_node(
    intent: QueryIntent,
    node: NodeWithScore,
) -> tuple[float, MarkdownTableFact] | None:
    facts = markdown_table_facts_from_node(intent, node)
    if not facts:
        return None
    return facts[0]


def markdown_table_facts_from_node(
    intent: QueryIntent,
    node: NodeWithScore,
) -> list[tuple[float, MarkdownTableFact]]:
    metadata = node.node.metadata or {}
    if not metadata_flag(metadata, "contains_markdown_table"):
        return []

    ranked: list[tuple[float, MarkdownTableFact]] = []
    question = intent.normalized_question
    overview_mode = query_wants_markdown_table_overview(intent)
    for table in parse_markdown_tables(node.node.text):
        headers = [str(cell).strip() for cell in table.get("headers", [])]
        rows = [[str(cell).strip() for cell in row] for row in table.get("rows", [])]
        if len(headers) < 2 or not rows:
            continue

        column_idx, column_score = best_table_column_match(question, headers)
        for row in rows:
            if len(row) < 2:
                continue
            row_label = row[0].strip()
            row_score = table_row_match_score(question, row_label)
            if not overview_mode and row_score <= 0.0:
                continue

            effective_column_idx = column_idx
            effective_column_score = column_score
            if effective_column_idx is None:
                if len(headers) != 2:
                    continue
                effective_column_idx = 1
                effective_column_score = 1.0

            if effective_column_idx >= len(row):
                continue
            value = row[effective_column_idx].strip()
            if not value:
                continue

            score = row_score + effective_column_score + lexical_overlap_score(question, normalize_text(value))
            if overview_mode:
                score += 0.8
            fact = MarkdownTableFact(
                provider=metadata.get("provider", "").strip().lower(),
                clauses=metadata.get("clauses", ""),
                source_name=metadata.get("source_name", ""),
                row_label=row_label,
                column_label=headers[effective_column_idx].strip(),
                value=value,
                score=score,
            )
            ranked.append((score, fact))

    ranked.sort(key=lambda item: item[0], reverse=True)
    return ranked


def best_table_column_match(question: str, headers: list[str]) -> tuple[int | None, float]:
    best_idx: int | None = None
    best_score = 0.0
    for idx, header in enumerate(headers[1:], start=1):
        score = table_header_match_score(question, header)
        if score > best_score:
            best_idx = idx
            best_score = score
    return best_idx, best_score


def table_header_match_score(question: str, header: str) -> float:
    normalized_header = normalize_text(header)
    score = lexical_overlap_score(question, normalized_header) * 8.0
    if contains_any(question, "discount rate", "折扣率") and contains_any(normalized_header, "discount rate", "折扣率"):
        score += 4.0
    if contains_any(question, "rate", "比率", "率") and contains_any(normalized_header, "rate", "比率", "率"):
        score += 1.2
    if contains_any(question, "premium", "保費", "保费") and contains_any(normalized_header, "premium", "保費", "保费"):
        score += 1.2
    return score


def table_row_match_score(question: str, row_label: str) -> float:
    normalized_row = normalize_text(row_label)
    score = lexical_overlap_score(question, normalized_row) * 10.0

    question_numbers = set(re.findall(r"\d+", question))
    row_numbers = set(re.findall(r"\d+", normalized_row))
    if question_numbers and row_numbers:
        if question_numbers == row_numbers:
            score += 4.0
        elif question_numbers & row_numbers:
            score += 2.0

    if contains_any(question, "without claims", "no claim", "無索償", "无索偿") and contains_any(
        normalized_row,
        "without claims",
        "no claim",
        "無索償",
        "无索偿",
    ):
        score += 2.0
    return score


def should_build_multi_markdown_table_answer(intent: QueryIntent, facts: list[MarkdownTableFact]) -> bool:
    if len(facts) < 2:
        return False
    requested_rows = requested_markdown_table_row_numbers(intent)
    if len(requested_rows) < 2:
        return False
    matched_rows = {first_numeric_token(fact.row_label) for fact in facts if first_numeric_token(fact.row_label) is not None}
    return len(requested_rows & matched_rows) >= 2


def should_build_markdown_table_overview_answer(intent: QueryIntent, facts: list[MarkdownTableFact]) -> bool:
    if len(facts) < 2:
        return False
    if requested_markdown_table_row_numbers(intent):
        return False
    return query_wants_markdown_table_overview(intent)


def requested_markdown_table_row_numbers(intent: QueryIntent) -> set[str]:
    return set(re.findall(r"\d+", intent.normalized_question))


def first_numeric_token(text: str) -> str | None:
    match = re.search(r"\d+", text)
    if not match:
        return None
    return match.group(0)


def is_markdown_table_query(intent: QueryIntent) -> bool:
    question = intent.normalized_question
    return contains_any(
        question,
        "discount rate",
        "discount rates",
        "no claim discount",
        "no claim discount rates",
        "without claims",
        "無索償",
        "无索偿",
        "折扣率",
        "表格",
        "table",
    )


def query_wants_markdown_table_overview(intent: QueryIntent) -> bool:
    question = intent.normalized_question
    return contains_any(
        question,
        "discount rates",
        "no claim discount rates",
        "all discount rates",
        "what are the",
        "折扣率有邊幾級",
        "折扣率有边几级",
        "所有折扣率",
        "無索償折扣率",
        "无索偿折扣率",
    )


def extract_consult_facts(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> list[ConsultCoverageFact]:
    facts: list[ConsultCoverageFact] = []
    for node in nodes:
        metadata = node.node.metadata or {}
        if consult_answer_score(intent, node) <= 0.0:
            continue
        text = normalize_text(node.node.text)
        facts.append(
            ConsultCoverageFact(
                provider=metadata.get("provider", "").strip().lower(),
                clauses=metadata.get("clauses", ""),
                source_name=metadata.get("source_name", ""),
                covered="exclusion" not in normalize_text(metadata.get("unit_types", "")),
                consultation_label=consultation_label_for_text(text, question_prefers_zh(intent.raw_question)),
            )
        )
    return facts


def cost_share_kind_requested(intent: QueryIntent) -> str:
    normalized_question = intent.normalized_question
    if contains_any(normalized_question, "自負額", "自负额", "自付額", "自付额", "deductible", "excess"):
        return "deductible"
    if contains_any(normalized_question, "co-insurance", "coinsurance", "共同保險", "共同保险", "co-payment", "copayment", "co payment"):
        return "co_insurance"
    if contains_any(normalized_question, "賠償比率", "reimbursement ratio", "reimbursement rate", "indemnity ratio", "claim ratio"):
        return "reimbursement_ratio"
    return ""


def detect_cost_share_kind_from_text(text: str) -> str:
    lower_text = normalize_text(text)
    if contains_any(lower_text, "co-insurance", "coinsurance", "共同保險", "共同保险", "co-payment", "copayment", "co payment"):
        return "co_insurance"
    if contains_any(
        lower_text,
        "percentage of eligible expense",
        "percentage of any claims",
        "policyholder must contribute after paying the deductible",
        "must contribute after paying the deductible",
        "amount shall be borne by the insured",
        "索償金額的一個百分比",
    ):
        return "co_insurance"
    if contains_any(lower_text, "賠償比率", "reimbursement ratio", "reimbursement rate", "indemnity ratio", "claim ratio"):
        return "reimbursement_ratio"
    if contains_any(
        lower_text,
        "自負額：",
        "自负额：",
        "自付額",
        "自付额",
        "附設自負額",
        "附设自负额",
        "自負額為",
        "自负额为",
        "deductible of",
        "subject to a deductible",
        "subject to deductible",
        "excess amount",
        "(excess)",
    ):
        return "deductible"
    if re.search(r"\bexcess of\s+(hk\$|港幣|\d)", lower_text):
        return "deductible"
    if extract_hkd_amount(text) is not None and contains_any(lower_text, "deductible", "自負額", "自负额", "自付額", "自付额"):
        return "deductible"
    return ""


def extract_cost_share_value(kind: str, text: str) -> str | None:
    if kind in {"deductible", "co_insurance", "reimbursement_ratio"}:
        percent = extract_percentage(text)
        if percent is not None:
            return percent
    if kind == "deductible":
        amount = extract_hkd_amount(text)
        if amount is not None:
            return amount
    return None


def extract_percentage(text: str) -> str | None:
    match = re.search(r"(\d+(?:\.\d+)?)\s*%", text)
    if match:
        return f"{match.group(1)}%"
    return None


def extract_hkd_amount(text: str) -> str | None:
    match = re.search(r"(港幣|HK\$)\s*([0-9,]+)", text, flags=re.IGNORECASE)
    if not match:
        return None
    prefix = "港幣" if "港幣" in match.group(1) else "HK$"
    return f"{prefix}{match.group(2)}"


def cost_share_note_for(kind: str, text: str, prefer_zh: bool) -> str | None:
    lower_text = normalize_text(text)
    if kind == "co_insurance" and contains_any(lower_text, "table of benefits", "承保表"):
        return "具體百分比需參考承保表。" if prefer_zh else "The exact percentage must be checked in the Table of Benefits."
    if kind == "reimbursement_ratio" and contains_any(lower_text, "承保表", "schedule"):
        return "具體賠償比率需參考承保表。" if prefer_zh else "The exact reimbursement ratio must be checked in the policy schedule."
    return None


def cost_share_surface_label(kind: str, text: str, prefer_zh: bool, question: str = "") -> str:
    lower_text = normalize_text(text)
    lower_question = normalize_text(question)
    if prefer_zh:
        if kind == "deductible":
            if contains_any(lower_text, "自付額") or contains_any(lower_question, "自付額", "自付额"):
                return "自付額"
            if contains_any(lower_text, "自負額", "自负额") or contains_any(lower_question, "自負額", "自负额"):
                return "自負額"
        if kind == "co_insurance":
            if contains_any(lower_text, "共同保險", "共同保险") or contains_any(lower_question, "共同保險", "共同保险"):
                return "共同保險"
        if kind == "reimbursement_ratio":
            if contains_any(lower_text, "賠償比率") or contains_any(lower_question, "賠償比率"):
                return "賠償比率"
        return cost_share_label_zh(kind)

    if kind == "co_insurance":
        if contains_any(lower_text, "co-payment", "copayment", "co payment") or contains_any(lower_question, "co-payment", "copayment", "co payment"):
            return "co-payment"
        if contains_any(lower_text, "co-insurance", "coinsurance") or contains_any(lower_question, "co-insurance", "coinsurance"):
            return "co-insurance"
    if kind == "deductible":
        if contains_any(lower_text, "deductible") or contains_any(lower_question, "deductible"):
            return "deductible"
        if contains_any(lower_text, "excess") or contains_any(lower_question, "excess"):
            return "excess"
    if kind == "reimbursement_ratio":
        if contains_any(lower_text, "reimbursement rate") or contains_any(lower_question, "reimbursement rate"):
            return "reimbursement rate"
        if contains_any(lower_text, "reimbursement ratio", "indemnity ratio", "claim ratio") or contains_any(lower_question, "reimbursement ratio", "indemnity ratio", "claim ratio"):
            return "reimbursement ratio"
    return cost_share_label_en(kind)


def has_direct_cost_share_statement(kind: str, text: str) -> bool:
    lower_text = normalize_text(text)
    if kind == "deductible":
        return (
            contains_any(
                lower_text,
                "自負額：",
                "自付額",
                "附設自負額",
                "自負額為每宗索償",
                "首港幣",
                "deductible of",
                "subject to a deductible",
                "subject to deductible",
                "excess amount",
                "subject to an excess of",
                "definition: excess",
                "(excess)",
            )
            or re.search(r"\bexcess of\s+(hk\$|港幣|\d)", lower_text) is not None
            or extract_cost_share_value(kind, text) is not None
        )
    if kind == "co_insurance":
        return contains_any(
            lower_text,
            "definition: co-insurance",
            "definition: co-payment",
            "定義：共同保險",
            "co-payment:",
            "co payment:",
            "copayment:",
            "subject to a co-payment",
            "subject to co-payment",
            "索償金額的一個百分比",
            "percentage of any claims",
            "percentage of eligible expense",
            "must contribute after paying the deductible",
            "amount shall be borne by the insured",
        ) or extract_cost_share_value(kind, text) is not None
    if kind == "reimbursement_ratio":
        return contains_any(
            lower_text,
            "賠償比率",
            "reimbursement ratio",
            "reimbursement rate",
            "indemnity ratio",
            "claim ratio",
        ) or extract_cost_share_value(kind, text) is not None
    return False


def cost_share_label_zh(kind: str) -> str:
    mapping = {
        "deductible": "自負額",
        "co_insurance": "共同保險",
        "reimbursement_ratio": "賠償比率",
    }
    return mapping.get(kind, "費用分攤條款")


def cost_share_label_en(kind: str) -> str:
    mapping = {
        "deductible": "deductible",
        "co_insurance": "co-insurance",
        "reimbursement_ratio": "reimbursement ratio",
    }
    return mapping.get(kind, "cost-sharing term")


def extract_pre_existing_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> PreExistingFact | None:
    exclusion_node = best_pre_existing_node(intent, nodes, require_exclusion=True)
    if exclusion_node is None:
        exclusion_node = best_pre_existing_node(intent, nodes, require_exclusion=False)
    if exclusion_node is None:
        return None

    metadata = exclusion_node.node.metadata or {}
    topic_tags = metadata_tags(metadata)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    lower_text = normalize_text(exclusion_node.node.text)
    excluded = (
        "exclusion" in unit_types
        or "pre_existing" in topic_tags
        or contains_any(lower_text, "不會賠償", "不获赔偿", "不獲賠償", "not be liable", "exclude", "excluded", "not covered")
    )
    return PreExistingFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        excluded=excluded,
        definition_like="definition" in unit_types,
    )


def extract_waiting_period_days(text: str, category: str) -> int | None:
    direct_match = extract_direct_waiting_period_days(text, category)
    if direct_match is not None:
        return direct_match

    for segment in iter_waiting_period_segments(text):
        if category == "cancer" and not contains_any(segment, "cancer", "癌症", "恶性肿瘤", "惡性腫瘤"):
            continue
        if category == "injury" and not contains_any(segment, "injury", "injuries", "accident", "受伤", "受傷", "身体损伤", "身體損傷", "意外"):
            continue
        if category == "illness" and not contains_any(
            segment,
            "illness",
            "illnesses",
            "disease",
            "diseases",
            "other conditions",
            "other illnesses",
            "其他病況",
            "其他病况",
            "疾病",
            "非上述",
            "狀況",
            "状况",
        ):
            continue
        if category == "general" and contains_any(
            segment,
            "cancer",
            "癌症",
            "恶性肿瘤",
            "惡性腫瘤",
            "injury",
            "injuries",
            "accident",
            "受伤",
            "受傷",
            "illness",
            "disease",
            "疾病",
        ):
            continue

        days = extract_first_day_count(segment)
        if days is not None:
            return days
    return None


def extract_direct_waiting_period_days(text: str, category: str) -> int | None:
    for pattern in waiting_period_patterns_for(category):
        match = re.search(pattern, text, flags=re.IGNORECASE | re.DOTALL)
        if match:
            return int(match.group(1))
    return None


def iter_waiting_period_segments(text: str) -> list[str]:
    compact = text.replace("\r", "\n")
    raw_segments = re.split(r"[\n。；;]+", compact)
    return [normalize_text(segment) for segment in raw_segments if segment.strip()]


def extract_first_day_count(text: str) -> int | None:
    match = re.search(r"(\d+)\s*(?:days?|天|日)", text, flags=re.IGNORECASE)
    if not match:
        return None
    return int(match.group(1))


def waiting_period_patterns_for(category: str) -> list[str]:
    if category == "cancer":
        return [
            r"(?:癌症|恶性肿瘤|惡性腫瘤)[^。；;\n]{0,14}?的等候期[^0-9\n]{0,12}?(\d+)\s*(?:天|日)",
            r"(?:癌症|恶性肿瘤|惡性腫瘤)[^。；;\n]{0,48}?[：:]\s*(\d+)\s*(?:天|日)",
            r"(?:cancer|malignant)[^.\n;]{0,40}?waiting period[^0-9\n]{0,16}?(\d+)\s*days?",
            r"(?:cancer|malignant)[^.\n;]{0,56}?[,:]\s*(\d+)\s*days?",
        ]
    if category == "injury":
        return [
            r"(?:受伤|受傷|身体损伤|身體損傷|意外)[^。；;\n]{0,14}?的等候期[^0-9\n]{0,12}?(\d+)\s*(?:天|日)",
            r"(?:受伤|受傷|身体损伤|身體損傷|意外)[^。；;\n]{0,48}?[：:]\s*(\d+)\s*(?:天|日)",
            r"(?:injury|injuries|bodily injury|accident)[^.\n;]{0,40}?waiting period[^0-9\n]{0,16}?(\d+)\s*days?",
            r"(?:injury|injuries|bodily injury|accident)[^.\n;]{0,56}?[,:]\s*(\d+)\s*days?",
        ]
    if category == "illness":
        return [
            r"(?:其他病況|其他病况|疾病（癌症除外）|疾病\(癌症除外\)|非上述涵蓋的狀況|非上述涵盖的状况|其他病況的等候期|其他病况的等候期)[^0-9\n]{0,20}?(\d+)\s*(?:天|日)",
            r"(?:other illnesses|other conditions|illness \(other than cancer\)|conditions not included in the above)[^.\n;]{0,56}?[,:]?\s*(\d+)\s*days?",
        ]
    if category == "general":
        return [
            r"(?:等候期|waiting period)[^0-9\n]{0,40}?(\d+)\s*(?:days?|天|日)",
        ]
    return []


def contains_cost_share_reference(text: str) -> bool:
    return contains_any(
        text,
        "自負額",
        "自付額",
        "deductible",
        "excess",
        "co-insurance",
        "co-payment",
        "copayment",
        "co payment",
        "coinsurance",
        "共同保險",
        "共同保险",
        "賠償比率",
        "reimbursement ratio",
        "reimbursement rate",
        "table of benefits",
        "承保表",
        "policy schedule",
    )


def intent_summary(intent: QueryIntent) -> str:
    flags: list[str] = []
    if intent.wants_waiting_period:
        flags.append("waiting_period")
    if intent.wants_coverage:
        flags.append("coverage")
    if intent.wants_exclusion:
        flags.append("exclusion")
    if intent.wants_pre_existing:
        flags.append("pre_existing")
    if intent.wants_comparison:
        flags.append("comparison")
    if intent.wants_cancer:
        flags.append("cancer")
    if intent.wants_injury:
        flags.append("injury")
    if intent.wants_consult:
        flags.append("consult")
    if intent.asks_limit:
        flags.append("limit")
    if intent.asks_cost_share:
        flags.append("cost_share")
    if intent.asks_renewal:
        flags.append("renewal")
    if intent.asks_upgrade:
        flags.append("upgrade")
    if intent.asks_cash_benefit:
        flags.append("cash_benefit")
    if intent.asks_addon_benefit:
        flags.append("addon_benefit")
    if intent.asks_age_limit:
        flags.append("age_limit")
    if intent.asks_eligibility:
        flags.append("eligibility")
    if intent.target_providers:
        flags.append("providers=" + ",".join(intent.target_providers))
    return ", ".join(flags)


def provider_display_name(provider_name: str) -> str:
    return PROVIDER_DISPLAY_NAMES.get(provider_name, provider_name)


def question_prefers_zh(question: str) -> bool:
    return bool(re.search(r"[\u3400-\u9fff]", question))


def best_waiting_period_value(intent: QueryIntent, fact: WaitingPeriodFact) -> tuple[str, int | None]:
    if intent.wants_cancer:
        return ("癌症等候期", fact.cancer_days)
    if intent.wants_injury:
        return ("受傷等候期", fact.injury_days)
    if fact.illness_days is not None:
        return ("疾病等候期", fact.illness_days)
    if fact.general_days is not None:
        return ("等候期", fact.general_days)
    if fact.cancer_days is not None:
        return ("癌症等候期", fact.cancer_days)
    if fact.injury_days is not None:
        return ("受傷等候期", fact.injury_days)
    return ("等候期", None)


def best_pre_existing_node(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
    require_exclusion: bool,
) -> NodeWithScore | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = pre_existing_answer_score(intent, node, require_exclusion=require_exclusion)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return ranked[0][2]


def best_consult_node(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> NodeWithScore | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        score = consult_answer_score(intent, node)
        if score <= 0.0:
            continue
        ranked.append((score, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return ranked[0][2]


def best_pre_existing_definition_node(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
    provider_name: str,
) -> NodeWithScore | None:
    ranked: list[tuple[float, float, NodeWithScore]] = []
    for node in nodes:
        metadata = node.node.metadata or {}
        if provider_name and metadata.get("provider", "") != provider_name:
            continue
        unit_types = normalize_text(metadata.get("unit_types", ""))
        if "definition" not in unit_types:
            continue
        if not contains_pre_existing_reference(node.node.text):
            continue
        ranked.append((1.0, rerank_score(intent, node), node))
    if not ranked:
        return None
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return ranked[0][2]


def extract_generic_exclusion_fact(
    intent: QueryIntent,
    nodes: list[NodeWithScore],
) -> ExclusionItemFact | None:
    ranked: list[tuple[float, float, str, NodeWithScore]] = []
    for node in nodes:
        score = generic_exclusion_answer_score(intent, node)
        if score <= 0.0:
            continue
        label = matched_exclusion_label(intent, node)
        if not label:
            continue
        ranked.append((score, rerank_score(intent, node), label, node))
    if not ranked:
        return None

    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    _, _, label, node = ranked[0]
    metadata = node.node.metadata or {}
    unit_types = normalize_text(metadata.get("unit_types", ""))
    lower_text = normalize_text(node.node.text)
    return ExclusionItemFact(
        provider=metadata.get("provider", "").strip().lower(),
        clauses=metadata.get("clauses", ""),
        source_name=metadata.get("source_name", ""),
        excluded="exclusion" in unit_types or contains_negative_coverage_reference(lower_text),
        matched_label=label,
    )


def generic_exclusion_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    unit_types = normalize_text(metadata.get("unit_types", ""))
    lower_text = normalize_text(node.node.text)
    if "exclusion" not in unit_types and not contains_negative_coverage_reference(lower_text):
        return 0.0

    matched_label = matched_exclusion_label(intent, node)
    if not matched_label:
        return 0.0

    overlap = semantic_label_match_score(intent.normalized_question, matched_label)
    if overlap <= 0.0:
        return 0.0

    score = 0.0
    if "exclusion" in unit_types:
        score += 3.0
    if contains_negative_coverage_reference(lower_text):
        score += 1.2
    score += overlap * 8.0
    if intent.wants_coverage:
        score += overlap * 2.4
    if intent.wants_exclusion:
        score += 1.0
    return score


def matched_exclusion_label(intent: QueryIntent, node: NodeWithScore) -> str:
    metadata = node.node.metadata or {}
    labels = metadata_list_item_labels(metadata)
    if not labels:
        return ""

    question = intent.normalized_question
    scored = [
        (semantic_label_match_score(question, label), label)
        for label in labels
    ]
    scored = [item for item in scored if item[0] > 0.0]
    if not scored:
        return ""
    scored.sort(key=lambda item: item[0], reverse=True)
    return scored[0][1]


def pre_existing_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
    require_exclusion: bool,
) -> float:
    metadata = node.node.metadata or {}
    lower_text = normalize_text(node.node.text)
    topic_tags = metadata_tags(metadata)
    unit_types = normalize_text(metadata.get("unit_types", ""))

    has_reference = "pre_existing" in topic_tags or contains_pre_existing_reference(lower_text)
    if not has_reference:
        return 0.0

    is_exclusion = "exclusion" in unit_types or contains_negative_coverage_reference(lower_text)
    if require_exclusion and not is_exclusion:
        return 0.0

    score = 0.0
    if is_exclusion:
        score += 4.0
    if "pre_existing" in topic_tags:
        score += 2.0
    if contains_negative_coverage_reference(lower_text):
        score += 1.5
    if "definition" in unit_types:
        score += 0.4

    position = first_pre_existing_reference_position(lower_text)
    if position >= 0:
        score += max(0.0, 1.0 - min(position, 400) / 400.0)
    return score


def consult_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    lower_text = normalize_text(node.node.text)
    topic_tags = metadata_tags(metadata)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    section_path = normalize_text(metadata.get("section_path", ""))

    if "exclusion" in unit_types:
        return 0.0

    has_direct_consult = "consult" in topic_tags or contains_direct_consult_reference(lower_text) or contains_any(section_path, "consultation", "診症", "诊症", "診金")
    if not has_direct_consult:
        return 0.0

    score = 0.0
    if "consult" in topic_tags:
        score += 3.0
    if contains_direct_consult_reference(lower_text):
        score += 2.5
    if contains_any(section_path, "consultation", "診症", "诊症", "診金"):
        score += 1.5
    if "benefit" in unit_types:
        score += 1.2
    if "coverage_definition" in unit_types:
        score -= 0.8
    if intent.asks_limit and has_explicit_limit_pattern(node.node.text):
        score += 2.0
    if intent.asks_limit and metadata_flag(metadata, "has_plan_limit_lines"):
        score += 1.6
    subtype = consult_subtype_requested(intent)
    if subtype and consult_text_matches_subtype(lower_text, subtype):
        score += 1.8
    elif subtype:
        score -= 0.6
    return score


def generic_limit_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    heading = heading_text_for_node(text)
    heading_lower = normalize_text(heading)
    benefit_labels = metadata_benefit_labels(metadata)

    if "benefit" not in unit_types:
        return 0.0

    score = 0.0
    if has_explicit_limit_pattern(text):
        score += 3.0
    plan_limits = extract_plan_limits(text)
    if plan_limits:
        score += 2.6
    if metadata_flag(metadata, "has_plan_limit_lines"):
        score += 1.4
    if contains_any(lower_text, "table of benefits", "maximum limits", "total annual coverage", "最高賠償額", "最高赔偿额", "總年度保障", "总年度保障"):
        score += 1.4
    overlap = lexical_overlap_score(intent.normalized_question, lower_text)
    score += overlap * 4.0
    if benefit_labels:
        best_label_overlap = max(semantic_label_match_score(intent.normalized_question, label) for label in benefit_labels)
        score += best_label_overlap * 3.6
    if question_limit_mentions_hospitalisation(intent) and contains_any(
        lower_text,
        "hospitalisation",
        "hospitalization",
        "hospital",
        "room and board",
        "住院",
        "住院費用",
        "住院费用",
    ):
        score += 1.8
    if question_limit_mentions_hospitalisation(intent) and any(
        contains_any(label, "hospitalisation", "hospitalization", "room and board", "overnight hospitalisation", "住院", "住院費用")
        for label in benefit_labels
    ):
        score += 2.2
    if heading_lower:
        score += lexical_overlap_score(intent.normalized_question, heading_lower) * 8.0
        focus_words = english_benefit_focus_words(intent.raw_question)
        if focus_words:
            heading_matches = sum(1 for word in focus_words if contains_ascii_term(heading_lower, word))
            score += heading_matches * 2.8
            if heading_matches == 0:
                score -= 2.4
        if question_limit_mentions_hospitalisation(intent) and contains_any(
            heading_lower,
            "hospitalisation",
            "hospitalization",
            "room and board",
            "住院",
        ):
            score += 2.0

    if intent.wants_consult and contains_direct_consult_reference(lower_text):
        score -= 1.2
    if intent.wants_coverage and not intent.wants_consult:
        score += 0.3
    if intent.wants_injury and not contains_any(lower_text, "injury", "injuries", "受伤", "受傷", "身體損傷", "身体损伤"):
        score -= 0.4
    return score


def renewal_upgrade_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    section_path = normalize_text(metadata.get("section_path", ""))
    topic_tags = metadata_tags(metadata)

    if "renewal_rule" not in unit_types and "renewal" not in topic_tags and "upgrade" not in topic_tags:
        if not contains_any(lower_text, "renewal", "續保", "续保", "upgrade", "升級", "升级"):
            return 0.0

    score = 0.0
    if "renewal_rule" in unit_types:
        score += 2.6
    if contains_any(section_path, "renewal", "續保", "续保", "更改保障", "upgrade", "升級", "升级"):
        score += 1.0
    if intent.asks_renewal:
        if detect_auto_renew(lower_text):
            score += 2.4
        guaranteed_age, approval_age = extract_renewal_ages(text)
        if guaranteed_age is not None:
            score += 2.2
        if approval_age is not None:
            score += 0.8
    if intent.asks_upgrade:
        if detect_upgrade_waiting_period(lower_text):
            score += 2.6
        if detect_downgrade_no_waiting(lower_text):
            score += 1.5
        if detect_can_upgrade(lower_text):
            score += 1.2
        if extract_upgrade_age_block(text) is not None:
            score += 1.2
        if contains_any(lower_text, "新增的保障部份", "additional coverage", "較高年度保障總額", "higher annual limit"):
            score += 1.1
        if intent.asks_age_limit and extract_upgrade_age_block(text) is not None:
            score += 2.8
        if intent.asks_age_limit and detect_upgrade_waiting_period(lower_text):
            score -= 1.4
    score += lexical_overlap_score(intent.normalized_question, lower_text) * 3.0
    return score


def addon_waiting_period_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    topic_tags = metadata_tags(metadata)

    if "waiting_period" not in unit_types and "waiting_period" not in topic_tags:
        return 0.0
    if not intent.asks_addon_benefit:
        return 0.0

    score = 0.0
    if "addon_waiting_period" in topic_tags:
        score += 3.0
    if contains_any(
        lower_text,
        "危疾現金保障",
        "高级危疾现金保障",
        "高級危疾現金保障",
        "critical illness cash benefit",
        "advanced critical illness cash benefit",
        "貓傳染性腹膜炎",
        "猫传染性腹膜炎",
        "feline infectious peritonitis",
        "cash benefit",
    ):
        score += 3.2
    clause_kind = addon_additional_benefit_kind_for_text(lower_text)
    subtype = addon_additional_benefit_kind(intent)
    if clause_kind is not None:
        score += 1.0
        if subtype == clause_kind:
            score += 3.2
        elif subtype is not None:
            score -= 2.8
    elif subtype and contains_any(lower_text, "標準等候期", "standard waiting period"):
        score -= 2.4
    days = extract_waiting_period_days(text, "general")
    if days is not None:
        score += 1.2
    return score


def addon_eligibility_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    topic_tags = metadata_tags(metadata)

    if not intent.asks_cash_benefit or not intent.asks_age_limit:
        return 0.0
    if "eligibility" not in unit_types and "eligibility" not in topic_tags:
        return 0.0

    score = 0.0
    if contains_any(lower_text, "age limit", "年齡限制", "年龄限制", "years old", "歲", "岁"):
        score += 2.0
    if contains_any(lower_text, "only available for pet from", "只供", "只適用於", "only available for pet"):
        score += 2.2
    if contains_any(lower_text, "not renewable once", "無法續保", "不設年齡限制", "no age limit for renewals"):
        score += 2.0
    clause_kind = addon_cash_benefit_kind_for_text(lower_text)
    subtype = addon_cash_benefit_kind(intent)
    if clause_kind is not None:
        score += 1.0
        if subtype == clause_kind:
            score += 3.0
        elif subtype is not None:
            score -= 2.6
    if extract_age_range(text) != (None, None):
        score += 1.5
    if extract_nonrenewable_age(text) is not None or detect_no_age_limit_for_renewal(text):
        score += 1.2
    return score


def addon_conditions_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    topic_tags = metadata_tags(metadata)

    if not intent.asks_addon_benefit or not intent.asks_eligibility:
        return 0.0
    if "eligibility" not in unit_types and "eligibility" not in topic_tags:
        return 0.0

    score = 0.0
    if contains_any(lower_text, "受保條件", "eligibility"):
        score += 1.2
    if contains_any(lower_text, "首次因", "must not develop", "received a diagnosis", "註冊獸醫", "registered veterinary surgeon", "prescribes"):
        score += 2.0
    if extract_structured_conditions(text):
        score += 2.4
    clause_kind = addon_additional_benefit_kind_for_text(lower_text)
    subtype = addon_additional_benefit_kind(intent)
    if clause_kind is not None:
        score += 1.0
        if subtype == clause_kind:
            score += 3.2
        elif subtype is not None:
            score -= 2.8
    return score


def chronic_condition_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))

    if not intent.asks_chronic_condition:
        return 0.0
    if not contains_any(lower_text, "慢性病況", "慢性病况", "慢性疾病", "chronic medical conditions", "chronic condition"):
        return 0.0

    score = 0.0
    if "eligibility" in unit_types:
        score += 2.6
    if "renewal_rule" in unit_types and intent.asks_upgrade:
        score += 2.0
    if contains_any(lower_text, "4 歲或以下", "5 歲或以上", "4 years old or below", "5 years old or above"):
        score += 2.1
    if contains_any(lower_text, "全面保障", "will cover the above chronic medical conditions"):
        score += 1.4
    if contains_any(lower_text, "僅於", "only provide coverage", "policy year", "不再受保", "excluded from the subsequent renewal"):
        score += 2.2
    if contains_any(lower_text, "原保障不受影響", "original coverage is unaffected", "新增保障", "additional coverage"):
        score += 2.0
    if intent.asks_age_limit and contains_any(lower_text, "4 歲", "5 歲", "4 years old", "5 years old"):
        score += 1.5
    return score


def cost_share_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(node.node.text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    section_path = normalize_text(metadata.get("section_path", ""))
    clauses = normalize_text(metadata.get("clauses", ""))
    topic_tags = metadata_tags(metadata)
    requested_kind = cost_share_kind_requested(intent)
    candidate_kind = infer_cost_share_kind_from_node(metadata, text)
    evidence_kind = metadata_cost_share_evidence(metadata)
    has_numeric_metadata = metadata_flag(metadata, "cost_share_has_numeric")
    value_type = infer_cost_share_value_type_from_node(metadata, text)
    score = 0.0

    if not candidate_kind:
        return 0.0
    if candidate_kind == "mixed":
        if not contains_cost_share_reference(lower_text):
            return 0.0
        score += 0.8
    direct = candidate_kind != "mixed" and has_direct_cost_share_statement(candidate_kind, text)
    if not contains_cost_share_reference(lower_text) and not direct and "cost_share" not in topic_tags:
        return 0.0

    if candidate_kind != "mixed":
        score += 2.5
    if requested_kind and candidate_kind == requested_kind:
        score += 1.2
    elif requested_kind and candidate_kind == "mixed":
        score -= 0.6
    elif requested_kind and candidate_kind != requested_kind:
        score -= 3.6

    if direct:
        score += 3.0

    value = extract_cost_share_value(candidate_kind, text) if candidate_kind != "mixed" else None
    if value is not None or has_numeric_metadata:
        score += 2.2
    if "cost_share" in topic_tags:
        score += 0.8

    explicit_metadata_kind = metadata_cost_share_kind(metadata)
    if explicit_metadata_kind and explicit_metadata_kind == candidate_kind and candidate_kind != "mixed":
        score += 0.7

    if candidate_kind == "deductible":
        if contains_any(lower_text, "自負額：", "自负额："):
            score += 1.6
        if contains_any(lower_text, "附設30%自負額", "附设30%自负额", "30% deductible"):
            score += 1.8
        if contains_any(lower_text, "每宗索償港幣", "per claim hk$", "per claim hkd"):
            score += 1.2
        if evidence_kind == "exclusion":
            score += 1.6
        elif evidence_kind == "benefit":
            score += 1.0
        elif contains_any(unit_types, "benefit", "waiting_period"):
            score += 0.8
        if "claim_rule" in unit_types:
            score -= 2.8
        if "exclusion" in unit_types and not contains_any(lower_text, "首港幣3,000", "first hk$3,000", "first hkd3,000") and not has_numeric_metadata:
            score -= 1.8
        if metadata_flag(metadata, "cost_share_mentions_table") and value is None and value_type != "hkd_amount":
            score -= 0.8

    if candidate_kind == "co_insurance":
        if evidence_kind == "definition":
            score += 1.8
        elif "definition" in unit_types and contains_any(lower_text, "co-insurance", "共同保險", "共同保险"):
            score += 1.4
        if metadata_flag(metadata, "cost_share_mentions_table"):
            score += 0.6
        if evidence_kind == "table_reference" and value is None:
            score -= 1.4

    if candidate_kind == "reimbursement_ratio":
        if evidence_kind == "definition":
            score += 2.2
        elif contains_any(lower_text, "賠償比率", "reimbursement ratio", "reimbursement rate"):
            score += 1.8
        if metadata_flag(metadata, "cost_share_mentions_table"):
            score += 0.8
        if evidence_kind == "table_reference" and value is None:
            score -= 1.4

    if evidence_kind == "table_reference" and candidate_kind == "mixed":
        score -= 1.2
    if "section" in unit_types and not direct and evidence_kind not in {"definition", "benefit", "exclusion"}:
        score -= 1.6

    if contains_any(section_path, "waiting period", "等候期", "benefits", "保障項目"):
        score += 0.3
    if clauses == "definition" or evidence_kind == "definition":
        score += 0.2

    return score


def general_age_limit_answer_score(
    intent: QueryIntent,
    node: NodeWithScore,
) -> float:
    if not is_general_age_limit_query(intent):
        return 0.0

    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    unit_types = normalize_text(metadata.get("unit_types", ""))
    section_path = normalize_text(metadata.get("section_path", ""))
    topic_tags = metadata_tags(metadata)

    min_age, max_age, max_exclusive = extract_general_entry_age_bounds(text)
    has_age_bounds = min_age is not None or max_age is not None
    has_age_heading = contains_any(
        lower_text,
        "age limit",
        "年齡限制",
        "年龄限制",
        "pet eligibility",
        "寵物資格",
        "宠物资格",
    )
    has_eligibility_markers = contains_any(
        lower_text,
        "eligibility",
        "eligible",
        "micro-chipped",
        "vaccinations",
        "working pet",
        "年齡為",
        "年龄为",
        "星期以上",
        "歲以下",
        "岁以下",
        "weeks old",
        "years old",
    )
    has_underwriting = detect_general_age_limit_renewal_underwriting(text)

    if not has_age_bounds and not has_age_heading and not has_eligibility_markers and not has_underwriting:
        return 0.0

    score = 0.0
    if has_age_bounds:
        score += 4.2
    if "eligibility" in unit_types or "eligibility" in topic_tags:
        score += 2.4
    if "general_condition" in unit_types:
        score += 2.0
    if contains_any(section_path, "age limit", "pet eligibility", "eligibility", "年齡限制", "年龄限制"):
        score += 1.6
    if has_age_heading:
        score += 1.8
    if has_eligibility_markers:
        score += 1.0
    if min_age is not None and max_age is not None and max_exclusive:
        score += 0.4
    if has_underwriting:
        score += 0.5

    if has_explicit_limit_pattern(text):
        score -= 3.0
    if contains_any(
        lower_text,
        "table of benefits",
        "annual coverage",
        "per year",
        "per visit",
        "總年度保障",
        "总年度保障",
        "最高賠償額",
        "最高赔偿额",
    ):
        score -= 2.8
    if "benefit_table" in unit_types or ("benefit" in unit_types and not has_age_bounds):
        score -= 1.4
    return score


def node_has_general_age_limit_evidence(node: NodeWithScore) -> bool:
    metadata = node.node.metadata or {}
    text = node.node.text
    lower_text = normalize_text(text)
    section_path = normalize_text(metadata.get("section_path", ""))
    min_age, max_age, _ = extract_general_entry_age_bounds(text)
    if min_age is not None or max_age is not None:
        return True
    if contains_any(
        lower_text,
        "age limit",
        "年齡限制",
        "年龄限制",
        "pet eligibility",
        "年齡為",
        "年龄为",
        "weeks old",
        "years old",
        "星期以上",
        "歲以下",
        "岁以下",
    ):
        return True
    return contains_any(section_path, "age limit", "pet eligibility", "年齡限制", "年龄限制")


def extract_renewal_ages(text: str) -> tuple[int | None, int | None]:
    guaranteed_age: int | None = None
    approval_age: int | None = None

    zh_guaranteed = re.search(r"可續保至\s*([0-9]+)\s*歲", text)
    en_guaranteed = re.search(r"renewed up to\s*([0-9]+)\s*years? old", text, flags=re.IGNORECASE)
    if zh_guaranteed:
        guaranteed_age = int(zh_guaranteed.group(1))
    elif en_guaranteed:
        guaranteed_age = int(en_guaranteed.group(1))

    zh_approval = re.search(r"任何\s*([0-9]+)\s*歲以上之續保須經核保審批", text)
    en_approval = re.search(r"any\s*([0-9]+)\s*years? old.*subject to underwriting approval", text, flags=re.IGNORECASE)
    if zh_approval:
        approval_age = int(zh_approval.group(1))
    elif en_approval:
        approval_age = int(en_approval.group(1))

    return guaranteed_age, approval_age


def extract_upgrade_age_block(text: str) -> int | None:
    zh = re.search(r"已屆\s*([0-9]+)\s*歲.*不接受升級", text)
    en = re.search(r"turning\s*([0-9]+)\s*years? old.*do not accept.*upgrade", text, flags=re.IGNORECASE)
    if zh:
        return int(zh.group(1))
    if en:
        return int(en.group(1))
    return None


def extract_age_range(text: str) -> tuple[int | None, int | None]:
    zh = re.search(r"([0-9]+)\s*歲至\s*([0-9]+)\s*歲", text)
    en = re.search(r"from\s*([0-9]+)\s*year[s]?\s*old\s*to\s*([0-9]+)\s*year[s]?\s*old", text, flags=re.IGNORECASE)
    if zh:
        return int(zh.group(1)), int(zh.group(2))
    if en:
        return int(en.group(1)), int(en.group(2))
    return None, None


def extract_general_entry_age_bounds(text: str) -> tuple[str | None, str | None, bool]:
    zh_range = re.search(
        r"年齡為\s*[^0-9（）()]*[（(]?(\d+)[)）]?\s*星期以上及[^0-9（）()]*[（(]?(\d+)[)）]?\s*歲以下",
        text,
    )
    if zh_range:
        return f"{zh_range.group(1)} 星期", f"{zh_range.group(2)} 歲", True

    en_between = re.search(
        r"aged between\s*[a-z\s()]*?(\d+)\s*weeks?\s*and\s*[a-z\s()]*?(\d+)\s*years?\s*old",
        text,
        flags=re.IGNORECASE,
    )
    if en_between:
        return f"{en_between.group(1)} weeks old", f"{en_between.group(2)} years old", False

    en_at_least_below = re.search(
        r"at least\s*[a-z\s()]*?(\d+)\s*weeks?\s*old\s*and\s*below\s*[a-z\s()]*?(\d+)\s*years?\s*old",
        text,
        flags=re.IGNORECASE,
    )
    if en_at_least_below:
        return f"{en_at_least_below.group(1)} weeks old", f"{en_at_least_below.group(2)} years old", True

    return None, None, False


def extract_nonrenewable_age(text: str) -> int | None:
    zh = re.search(r"到達\s*([0-9]+)\s*歲起無法續保", text)
    en = re.search(r"not renewable once .* becomes\s*([0-9]+)\s*years? old", text, flags=re.IGNORECASE)
    if zh:
        return int(zh.group(1))
    if en:
        return int(en.group(1))
    return None


def detect_no_age_limit_for_renewal(text: str) -> bool:
    return contains_any(
        normalize_text(text),
        "續保時則不設年齡限制",
        "續保時不設年齡限制",
        "續保時不设年龄限制",
        "no age limit for renewals",
    )


def detect_general_age_limit_renewal_underwriting(text: str) -> bool:
    return contains_any(
        normalize_text(text),
        "renewal of this policy will be subject to underwriting",
        "renewal of this policy shall be subject to underwriting",
        "renewal is subject to underwriting",
        "續保須經核保",
        "续保须经核保",
    )


def extract_structured_conditions(text: str) -> list[str]:
    conditions: list[str] = []
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or line.startswith(">"):
            continue
        if line.startswith("- "):
            candidate = line[2:].strip()
            if candidate:
                conditions.append(candidate.rstrip("。."))
    return conditions


def detect_auto_renew(text: str) -> bool | None:
    if contains_any(text, "自動續保", "automatically renewed", "renewed automatically", "automatic renewal"):
        return True
    return None


def detect_can_upgrade(text: str) -> bool | None:
    if contains_any(text, "申請更改保障計劃", "upgrade the policy", "更改保障計劃", "轉換至較高的計劃級別", "upgrade to a plan"):
        return True
    return None


def detect_upgrade_waiting_period(text: str) -> bool | None:
    if contains_any(
        text,
        "升級計劃後新增的保障部份將設等候期",
        "升級至年度保障總額較高的計劃",
        "waiting period applies to the additional coverage due to upgrade of plan",
        "increased portion of annual limit shall be subject to a waiting period",
    ):
        return True
    if contains_any(text, "不設等候期", "no waiting period"):
        return False
    return None


def detect_downgrade_no_waiting(text: str) -> bool | None:
    if contains_any(text, "降級計劃不設等候期", "in the case of downgrading, there will not be waiting period"):
        return True
    return None


def addon_cash_benefit_kind(intent: QueryIntent) -> str | None:
    normalized = intent.normalized_question
    if contains_any(normalized, "高級危疾現金保障", "高级危疾现金保障", "advanced critical illness cash benefit"):
        return "advanced"
    if contains_any(normalized, "危疾現金保障", "危疾现金保障", "critical illness cash benefit"):
        return "critical"
    if contains_any(normalized, "癌症現金保障", "癌症现金保障", "cancer cash benefit"):
        return "cancer"
    return None


def addon_additional_benefit_kind(intent: QueryIntent) -> str | None:
    normalized = intent.normalized_question
    if contains_any(normalized, "貓傳染性腹膜炎額外保額", "猫传染性腹膜炎额外保额", "feline infectious peritonitis additional coverage"):
        return "fip"
    return addon_cash_benefit_kind(intent)


def addon_cash_benefit_kind_for_text(text: str) -> str | None:
    lower_text = normalize_text(text)
    if contains_any(lower_text, "高級危疾現金保障", "高级危疾现金保障", "advanced critical illness cash benefit"):
        return "advanced"
    if contains_any(lower_text, "危疾現金保障", "危疾现金保障", "critical illness cash benefit"):
        return "critical"
    if contains_any(lower_text, "癌症現金保障", "癌症现金保障", "cancer cash benefit"):
        return "cancer"
    return None


def addon_additional_benefit_kind_for_text(text: str) -> str | None:
    lower_text = normalize_text(text)
    if contains_any(lower_text, "貓傳染性腹膜炎額外保額", "猫传染性腹膜炎额外保额", "feline infectious peritonitis additional coverage", "feline infectious peritonitis"):
        return "fip"
    return addon_cash_benefit_kind_for_text(lower_text)


def addon_cash_benefit_label(intent: QueryIntent, text: str) -> str:
    subtype = addon_additional_benefit_kind(intent)
    lower_text = normalize_text(text)
    if subtype == "fip" or contains_any(lower_text, "貓傳染性腹膜炎額外保額", "猫传染性腹膜炎额外保额", "feline infectious peritonitis additional coverage"):
        return "貓傳染性腹膜炎額外保額" if question_prefers_zh(intent.raw_question) else "feline infectious peritonitis additional coverage"
    if subtype == "advanced" or contains_any(lower_text, "高級危疾現金保障", "advanced critical illness cash benefit"):
        return "高級危疾現金保障" if question_prefers_zh(intent.raw_question) else "advanced critical illness cash benefit"
    if subtype == "critical" or contains_any(lower_text, "危疾現金保障", "critical illness cash benefit"):
        return "危疾現金保障" if question_prefers_zh(intent.raw_question) else "critical illness cash benefit"
    if subtype == "cancer" or contains_any(lower_text, "癌症現金保障", "cancer cash benefit"):
        return "癌症現金保障" if question_prefers_zh(intent.raw_question) else "cancer cash benefit"
    return "附加現金保障" if question_prefers_zh(intent.raw_question) else "cash benefit add-on"


def format_waiting_period_fact_line(
    provider_name: str,
    label: str,
    days: int | None,
    fact: WaitingPeriodFact,
    prefer_zh: bool,
) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        answer_value = "不設等候期" if fact.no_waiting_period and days is None else f"{days} 日"
        return f"- {display_name}：{label}為 {answer_value}{format_clause_suffix(fact.clauses, prefer_zh=True)}"

    english_label = english_waiting_period_label(label)
    answer_value = "no waiting period" if fact.no_waiting_period and days is None else f"{days} days"
    return f"- {display_name}: {english_label} is {answer_value}{format_clause_suffix(fact.clauses, prefer_zh=False)}"


def format_single_waiting_period_line(
    provider_name: str,
    label: str,
    days: int | None,
    fact: WaitingPeriodFact,
    prefer_zh: bool,
) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        answer_value = "不設等候期" if fact.no_waiting_period and days is None else f"{days} 日"
        return f"{display_name} 的{label}為 {answer_value}{format_clause_suffix(fact.clauses, prefer_zh=True)}"

    english_label = english_waiting_period_label(label)
    answer_value = "no waiting period" if fact.no_waiting_period and days is None else f"{days} days"
    return f"{display_name} {english_label} is {answer_value}{format_clause_suffix(fact.clauses, prefer_zh=False)}"


def format_renewal_answer(provider_name: str, fact: RenewalRuleFact, prefer_zh: bool) -> str | None:
    if fact.guaranteed_age is None and fact.auto_renew is None:
        return None
    if prefer_zh:
        if fact.guaranteed_age is not None:
            answer = f"{provider_name} 的保單保證可續保至 {fact.guaranteed_age} 歲"
            if fact.approval_age_threshold is not None:
                answer += f"，{fact.approval_age_threshold} 歲以上續保須經核保審批"
            return answer + format_clause_suffix(fact.clauses, prefer_zh=True)
        if fact.auto_renew:
            return f"{provider_name} 的保單會在符合條款下自動續保{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        return None

    if fact.guaranteed_age is not None:
        answer = f"{provider_name} can be renewed up to age {fact.guaranteed_age}"
        if fact.approval_age_threshold is not None:
            answer += f", and renewal above age {fact.approval_age_threshold} is subject to underwriting approval"
        return answer + format_clause_suffix(fact.clauses, prefer_zh=False)
    if fact.auto_renew:
        return f"{provider_name} can be renewed automatically under the stated renewal terms{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    return None


def format_upgrade_answer(provider_name: str, fact: RenewalRuleFact, prefer_zh: bool) -> str | None:
    if fact.upgrade_waiting_period is None and fact.can_upgrade is None and fact.age_upgrade_block is None:
        return None
    if prefer_zh:
        parts: list[str] = []
        if fact.upgrade_waiting_period:
            parts.append("升級至較高年度保障總額的計劃後，新增保障部份會設有等候期")
        elif fact.upgrade_waiting_period is False:
            parts.append("升級後不設等候期")
        if fact.age_upgrade_block is not None:
            parts.append(f"如寵物於續保時已屆 {fact.age_upgrade_block} 歲，則不接受升級至更高年度保障總額的計劃")
        if fact.downgrade_no_waiting:
            parts.append("降級計劃則不設等候期")
        if not parts:
            return None
        return f"{provider_name} 的升級／更改保障規則為：" + "；".join(parts) + format_clause_suffix(fact.clauses, prefer_zh=True)

    parts = []
    if fact.upgrade_waiting_period:
        parts.append("the additional coverage from an upgrade is subject to a waiting period")
    elif fact.upgrade_waiting_period is False:
        parts.append("there is no waiting period after upgrade")
    if fact.age_upgrade_block is not None:
        parts.append(f"upgrade to a higher annual-limit plan is not accepted once the pet is turning {fact.age_upgrade_block}")
    if fact.downgrade_no_waiting:
        parts.append("downgrade does not trigger a waiting period")
    if not parts:
        return None
    return f"{provider_name} upgrade/change-of-plan rule is: " + "; ".join(parts) + format_clause_suffix(fact.clauses, prefer_zh=False)


def format_chronic_condition_answer_zh(
    provider_name: str,
    primary: ChronicConditionRuleFact,
    upgrade_fact: ChronicConditionRuleFact | None,
) -> str | None:
    if primary.rule_kind == "age_5_or_above":
        answer = (
            f"{provider_name} 的慢性病況規則是：若寵物在首次投保或升級至更高年度保障總額計劃的續保日（以較早者為準）已屆 5 歲或以上，"
            "相關慢性病況只會在首次出現症狀、確診、用藥、接受醫療建議或治療的保單年度受保；續保後，相關慢性病況將不再受保"
        )
        if primary.waiting_period_required:
            answer += "。若於適用等候期完結前已出現症狀、確診、用藥、接受醫療建議或治療，則不獲賠償"
        if upgrade_fact is not None and upgrade_fact.additional_coverage_subject_to_age_rule:
            answer += "。若升級發生在寵物滿 5 歲或之後，這個年齡相關條件會套用到新增保障，但原保障不受影響"
        return answer
    if primary.rule_kind == "age_4_or_below":
        answer = (
            f"{provider_name} 的慢性病況規則是：若寵物在首次投保或升級至更高年度保障總額計劃的續保日（以較後者為準）為 4 歲或以下，"
            "並且在適用等候期完結前未曾因相關慢性病況出現症狀、確診、用藥、接受醫療建議或治療，則可獲全面保障"
        )
        return answer
    return None


def format_chronic_condition_answer_en(
    provider_name: str,
    primary: ChronicConditionRuleFact,
    upgrade_fact: ChronicConditionRuleFact | None,
) -> str | None:
    if primary.rule_kind == "age_5_or_above":
        answer = (
            f"{provider_name} chronic-condition rule is: if the pet is 5 years old or above on the earlier of the first Policy Start Date "
            "or the renewal date on which the policy is upgraded to a higher Annual Limit plan, the related chronic conditions are only covered "
            "in the policy year when symptoms, diagnosis, medication, advice, or treatment first arise, and they are excluded from subsequent renewals"
        )
        if primary.waiting_period_required:
            answer += ". If symptoms, diagnosis, medication, advice, or treatment happened before the applicable waiting period ended, they are not covered"
        if upgrade_fact is not None and upgrade_fact.additional_coverage_subject_to_age_rule:
            answer += ". If the upgrade happens on or after the pet turning 5 years old, this age-related rule applies to the Additional Coverage while the Original Coverage is unaffected"
        return answer
    if primary.rule_kind == "age_4_or_below":
        return (
            f"{provider_name} chronic-condition rule is: if the pet is 4 years old or below on the later of the first Policy Start Date "
            "or the renewal date on which the policy is upgraded to a higher Annual Limit plan, and no related symptoms, diagnosis, medication, advice, or treatment "
            "occurred before the applicable waiting period ended, those chronic conditions are fully covered"
        )
    return None


def format_missing_evidence_line(provider_name: str, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return f"- {display_name}：未在目前檢索到的條款證據中找到明確答案。"
    return f"- {display_name}: no clear answer was found in the currently retrieved policy evidence."


def format_partial_evidence_line(provider_name: str, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return f"- {display_name}：已檢索到相關條款，但未能從目前證據穩定抽取出明確天數。"
    return f"- {display_name}: related clauses were retrieved, but the current evidence did not yield a stable day-count extraction."


def format_clause_suffix(clauses: str, prefer_zh: bool) -> str:
    clauses = clauses.strip()
    if not clauses:
        return "。"
    if prefer_zh:
        return f"（條款 {clauses}）。"
    return f" (Clause {clauses})."


def format_exclusion_label(label: str) -> str:
    cleaned = label.strip().strip(" .;,:-")
    if "（" in cleaned and "）" not in cleaned:
        cleaned += "）"
    if "(" in cleaned and ")" not in cleaned:
        cleaned += ")"
    return cleaned


def english_waiting_period_label(label: str) -> str:
    mapping = {
        "癌症等候期": "cancer waiting period",
        "受傷等候期": "injury waiting period",
        "疾病等候期": "illness waiting period",
        "等候期": "waiting period",
    }
    return mapping.get(label, "waiting period")


def contains_pre_existing_reference(text: str) -> bool:
    return contains_any(
        text,
        "pre-existing",
        "pre existing",
        "投保前已存在",
        "已存在病況",
        "已存在病况",
        "已存在之狀況",
        "已存在之状况",
        "pre-existing conditions",
    )


def contains_negative_coverage_reference(text: str) -> bool:
    return contains_any(
        text,
        "不會賠償",
        "不获赔偿",
        "不獲賠償",
        "不會負責",
        "不受保",
        "not be liable",
        "not covered",
        "excluded",
        "exclude",
    )


def first_pre_existing_reference_position(text: str) -> int:
    positions = [
        pos
        for pos in [
            text.find("pre-existing"),
            text.find("pre existing"),
            text.find("投保前已存在"),
            text.find("已存在病況"),
            text.find("已存在病况"),
            text.find("已存在之狀況"),
            text.find("已存在之状况"),
        ]
        if pos >= 0
    ]
    return min(positions) if positions else -1


def contains_direct_consult_reference(text: str) -> bool:
    has_direct_consult = contains_any(
        text,
        "consultation",
        "consult carried out by a vet",
        "vet expenses made for the consultation",
        "獸醫診症",
        "兽医诊症",
        "診症",
        "诊症",
        "診金",
    )
    if not has_direct_consult:
        return False
    if contains_any(text, "follow-up consultation", "follow-up consultations") and not contains_any(
        text,
        "consult carried out by a vet",
        "vet expenses made for the consultation",
        "veterinary consultation",
        "獸醫診症",
        "兽医诊症",
        "診症",
        "诊症",
        "診金",
    ):
        return False
    return True


def consultation_label_for_text(text: str, prefer_zh: bool) -> str:
    if prefer_zh:
        if contains_any(text, "專科", "specialist"):
            return "專科獸醫診金"
        if contains_any(text, "普通科", "general practice"):
            return "普通科獸醫診金"
        return "獸醫診症"

    if contains_any(text, "specialist", "emergency consultation"):
        return "specialist or emergency veterinary consultation"
    if contains_any(text, "general practice", "general vet"):
        return "general veterinary consultation"
    return "veterinary consultation"


def benefit_label_for_text(text: str, prefer_zh: bool) -> str:
    heading = heading_text_for_node(text)
    if heading:
        return heading
    return "相關保障項目" if prefer_zh else "the relevant benefit"


def heading_text_for_node(text: str) -> str:
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    candidate_heading = ""
    for line in lines:
        heading_match = re.match(r"^#{3,6}\s+(.*\S)\s*$", line)
        if not heading_match:
            continue
        heading = heading_match.group(1).strip()
        normalized = normalize_text(heading)
        if contains_any(normalized, "benefits", "benefit provisions", "coverage", "保障項目", "一般條款", "條件"):
            continue
        if re.fullmatch(r"section\s+\d+.*", normalized):
            continue
        if re.fullmatch(r"項目[一二三四五六七八九十].*", normalized):
            continue
        candidate_heading = heading
    return candidate_heading


def english_benefit_focus_words(question: str) -> list[str]:
    if question_prefers_zh(question):
        return []
    stopwords = {
        "what",
        "is",
        "the",
        "for",
        "and",
        "annual",
        "limit",
        "maximum",
        "max",
        "prudential",
        "blue",
        "cross",
        "onedegree",
        "one",
        "degree",
        "msig",
        "plan",
        "plans",
        "benefit",
        "benefits",
        "coverage",
        "policy",
        "year",
    }
    words = re.findall(r"[a-z]+", question.lower())
    return [word for word in words if len(word) >= 4 and word not in stopwords]


def generic_consultation_label(prefer_zh: bool) -> str:
    return "獸醫診症" if prefer_zh else "veterinary consultation"


def question_asks_specific_consult_subtype(intent: QueryIntent) -> bool:
    return contains_any(
        intent.normalized_question,
        "specialist",
        "emergency consultation",
        "general practice",
        "general vet",
        "專科",
        "普通科",
        "緊急診症",
        "紧急诊症",
    )


def consult_subtype_requested(intent: QueryIntent) -> str | None:
    if contains_any(intent.normalized_question, "specialist", "專科"):
        return "specialist"
    if contains_any(intent.normalized_question, "general practice", "general vet", "普通科"):
        return "general"
    if contains_any(intent.normalized_question, "emergency consultation", "緊急診症", "紧急诊症"):
        return "emergency"
    return None


def consult_text_matches_subtype(text: str, subtype: str) -> bool:
    if subtype == "specialist":
        return contains_any(text, "specialist", "專科")
    if subtype == "general":
        return contains_any(text, "general practice", "general vet", "普通科")
    if subtype == "emergency":
        return contains_any(text, "emergency consultation", "緊急診症", "紧急诊症")
    return False


def has_explicit_limit_pattern(text: str) -> bool:
    lower_text = normalize_text(text)
    return bool(
        re.search(r"plan\s*a\s*:\s*hk\$\s*\d", text, flags=re.IGNORECASE)
        or re.search(r"計劃a[:：]\s*港幣?\s*\d", text, flags=re.IGNORECASE)
        or contains_any(lower_text, "per year", "per visit", "/年", "每次最多", "max 20 visits", "只限最多20次")
    )


def extract_plan_limits(text: str) -> dict[str, dict[str, str]]:
    limits: dict[str, dict[str, str]] = {}
    normalized = normalize_plan_limit_text(text)
    for raw_line in normalized.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        plan_match = re.match(r"^(?:plan|計劃)\s*([ab])\s*[:：]\s*(.*)$", line, flags=re.IGNORECASE)
        if not plan_match:
            continue
        plan = plan_match.group(1).upper()
        remainder = plan_match.group(2).strip()
        parsed = parse_plan_limit_line(remainder)
        if parsed:
            limits[plan] = parsed
    return limits


def normalize_plan_limit_text(text: str) -> str:
    normalized = text.replace("\r", "\n")
    normalized = re.sub(r"(?<!\n)(?=(?:Plan\s+[A-Z]\s*:|計劃[A-Z0-9]\s*[:：]))", "\n", normalized)
    return normalized


def parse_plan_limit_line(text: str) -> dict[str, str]:
    result: dict[str, str] = {}
    zh_annual = re.search(r"港幣\s*([0-9,]+(?:/年)?)", text)
    en_annual = re.search(r"HK\$\s*([0-9,]+(?:\s*per\s+year)?)", text, flags=re.IGNORECASE)
    if zh_annual:
        result["annual_limit"] = f"港幣{zh_annual.group(1).strip()}"
    elif en_annual:
        value = en_annual.group(1).strip()
        result["annual_limit"] = f"HK${value}"

    zh_per_visit = re.search(r"每次最多港幣\s*([0-9,]+)", text)
    en_per_visit = re.search(r"HK\$\s*([0-9,]+)\s*per\s+visit", text, flags=re.IGNORECASE)
    zh_per_day = re.search(r"每天最多港幣\s*([0-9,]+)", text)
    en_per_day = re.search(r"HK\$\s*([0-9,]+)\s*per\s+day", text, flags=re.IGNORECASE)
    zh_max_visits = re.search(r"最多\s*([0-9]+)\s*次", text)
    en_max_visits = re.search(r"max\s*([0-9]+)\s*visits?", text, flags=re.IGNORECASE)

    if zh_per_visit:
        result["per_visit_limit"] = f"港幣{zh_per_visit.group(1)}"
    elif en_per_visit:
        result["per_visit_limit"] = f"HK${en_per_visit.group(1)}"

    if zh_per_day:
        result["per_day_limit"] = f"港幣{zh_per_day.group(1)}"
    elif en_per_day:
        result["per_day_limit"] = f"HK${en_per_day.group(1)}"

    if zh_max_visits:
        result["max_visits"] = zh_max_visits.group(1)
    elif en_max_visits:
        result["max_visits"] = en_max_visits.group(1)

    return result


def format_consult_limit_answer_zh(provider_name: str, fact: ConsultLimitFact) -> str:
    return (
        f"{provider_name} 的{fact.consultation_label}最高賠償額為 {format_consult_limit_values_zh(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
    )


def format_consult_limit_values_zh(fact: ConsultLimitFact) -> str:
    parts: list[str] = []
    for plan in ("A", "B"):
        values = fact.plan_limits.get(plan)
        if not values:
            continue
        piece = f"計劃{plan}：{values.get('annual_limit', '')}"
        if values.get("per_visit_limit"):
            piece += f"，每次最多{values['per_visit_limit']}"
        if values.get("per_day_limit"):
            piece += f"，每天最多{values['per_day_limit']}"
        if values.get("max_visits"):
            piece += f"，最多{values['max_visits']}次"
        parts.append(piece)
    return "；".join(parts) if parts else "目前證據未載明"


def format_consult_limit_answer_en(provider_name: str, fact: ConsultLimitFact) -> str:
    return (
        f"{provider_name} {fact.consultation_label} limit is {format_consult_limit_values_en(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def format_consult_limit_values_en(fact: ConsultLimitFact) -> str:
    parts: list[str] = []
    for plan in ("A", "B"):
        values = fact.plan_limits.get(plan)
        if not values:
            continue
        piece = f"Plan {plan}: {values.get('annual_limit', '')}"
        if values.get("per_visit_limit"):
            piece += f", {values['per_visit_limit']} per visit"
        if values.get("per_day_limit"):
            piece += f", {values['per_day_limit']} per day"
        if values.get("max_visits"):
            piece += f", max {values['max_visits']} visits"
        parts.append(piece)
    return "; ".join(parts) if parts else "no explicit limit in current evidence"


def format_consult_limit_fact_line(provider_name: str, fact: ConsultLimitFact, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return (
            f"- {display_name}：{fact.consultation_label}最高賠償額為 {format_consult_limit_values_zh(fact)}"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
    return (
        f"- {display_name}: {fact.consultation_label} limit is {format_consult_limit_values_en(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def format_consult_limit_partial_line(provider_name: str, fact: ConsultLimitFact, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return (
            f"- {display_name}：已檢索到{fact.consultation_label}條款，但目前證據內沒有明確的最高賠償額數字"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
    return (
        f"- {display_name}: the {fact.consultation_label} clause was retrieved, but no explicit limit amount was found "
        f"in the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def format_generic_limit_values_zh(fact: BenefitLimitFact) -> str:
    parts: list[str] = []
    for plan in ("A", "B"):
        values = fact.plan_limits.get(plan)
        if not values:
            continue
        piece = f"計劃{plan}：{values.get('annual_limit', '')}"
        if values.get("per_visit_limit"):
            piece += f"，每次最多{values['per_visit_limit']}"
        if values.get("per_day_limit"):
            piece += f"，每天最多{values['per_day_limit']}"
        if values.get("max_visits"):
            piece += f"，最多{values['max_visits']}次"
        parts.append(piece)
    return "；".join(parts) if parts else "目前證據未載明"


def format_generic_limit_values_en(fact: BenefitLimitFact) -> str:
    parts: list[str] = []
    for plan in ("A", "B"):
        values = fact.plan_limits.get(plan)
        if not values:
            continue
        piece = f"Plan {plan}: {values.get('annual_limit', '')}"
        if values.get("per_visit_limit"):
            piece += f", {values['per_visit_limit']} per visit"
        if values.get("per_day_limit"):
            piece += f", {values['per_day_limit']} per day"
        if values.get("max_visits"):
            piece += f", max {values['max_visits']} visits"
        parts.append(piece)
    return "; ".join(parts) if parts else "no explicit limit in current evidence"


def format_generic_limit_fact_line(provider_name: str, fact: BenefitLimitFact, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return (
            f"- {display_name}：{fact.benefit_label}最高賠償額為 {format_generic_limit_values_zh(fact)}"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
    return (
        f"- {display_name}: {fact.benefit_label} limit is {format_generic_limit_values_en(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def format_generic_limit_partial_line(provider_name: str, fact: BenefitLimitFact, prefer_zh: bool) -> str:
    display_name = provider_display_name(provider_name)
    if prefer_zh:
        return (
            f"- {display_name}：已檢索到{fact.benefit_label}條款，但目前證據內沒有明確的最高賠償額數字"
            f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
        )
    return (
        f"- {display_name}: the {fact.benefit_label} clause was retrieved, but no explicit limit amount was found "
        f"in the current evidence{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def format_generic_limit_answer_zh(provider_name: str, fact: BenefitLimitFact) -> str:
    return (
        f"{provider_name} 的{fact.benefit_label}最高賠償額為 {format_generic_limit_values_zh(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=True)}"
    )


def format_generic_limit_answer_en(provider_name: str, fact: BenefitLimitFact) -> str:
    return (
        f"{provider_name} {fact.benefit_label} limit is {format_generic_limit_values_en(fact)}"
        f"{format_clause_suffix(fact.clauses, prefer_zh=False)}"
    )


def merge_consultation_labels(facts: list[ConsultCoverageFact], prefer_zh: bool) -> str:
    labels = []
    seen: set[str] = set()
    for fact in facts:
        if fact.consultation_label in seen:
            continue
        seen.add(fact.consultation_label)
        labels.append(fact.consultation_label)

    if not labels:
        return "獸醫診症" if prefer_zh else "veterinary consultation"
    if len(labels) == 1:
        return labels[0]
    if prefer_zh:
        return "及".join(labels)
    return " and ".join(labels)


def join_fact_clauses(facts: list[ConsultCoverageFact]) -> str:
    clauses = []
    seen: set[str] = set()
    for fact in facts:
        clause = fact.clauses.strip()
        if not clause or clause in seen:
            continue
        seen.add(clause)
        clauses.append(clause)
    return ", ".join(clauses)


def normalize_text(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip().lower()


def build_rerank_document(node: NodeWithScore) -> str:
    meta = node.node.metadata or {}
    fields = [
        f"provider={meta.get('provider', '')}",
        f"language={meta.get('language', '')}",
        f"product={meta.get('product', '')}",
        f"section_path={meta.get('section_path', '')}",
        f"clauses={meta.get('clauses', '')}",
        f"unit_types={meta.get('unit_types', '')}",
        f"topic_tags={meta.get('topic_tags', '')}",
        f"definition_labels={meta.get('definition_labels', '')}",
        f"benefit_labels={meta.get('benefit_labels', '')}",
        f"list_item_labels={meta.get('list_item_labels', '')}",
        f"list_item_count={meta.get('list_item_count', '')}",
        f"contains_markdown_table={meta.get('contains_markdown_table', '')}",
        f"table_headers={meta.get('table_headers', '')}",
        f"table_row_labels={meta.get('table_row_labels', '')}",
        f"table_column_count={meta.get('table_column_count', '')}",
        f"table_row_count={meta.get('table_row_count', '')}",
        f"cost_share_kind={meta.get('cost_share_kind', '')}",
        f"cost_share_evidence={meta.get('cost_share_evidence', '')}",
        f"cost_share_has_numeric={meta.get('cost_share_has_numeric', '')}",
        f"cost_share_value_type={meta.get('cost_share_value_type', '')}",
        f"cost_share_value_dependencies={meta.get('cost_share_value_dependencies', '')}",
        node.node.text.strip(),
    ]
    return "\n".join(part for part in fields if part)


def node_key(node: NodeWithScore) -> tuple[str, str]:
    metadata = node.node.metadata or {}
    return (metadata.get("source_path", ""), metadata.get("chunk_index", ""))


def metadata_tags(metadata: dict[str, Any]) -> set[str]:
    raw = metadata.get("topic_tags", "")
    if not isinstance(raw, str):
        return set()
    return {part.strip().lower() for part in raw.split(",") if part.strip()}


def metadata_list_item_labels(metadata: dict[str, Any]) -> list[str]:
    raw = metadata.get("list_item_labels", "")
    if not isinstance(raw, str):
        return []
    return [part.strip().lower() for part in raw.split(",") if part.strip()]


def metadata_benefit_labels(metadata: dict[str, Any]) -> list[str]:
    raw = metadata.get("benefit_labels", "")
    if not isinstance(raw, str):
        return []
    return [part.strip().lower() for part in raw.split(",") if part.strip()]


def metadata_flag(metadata: dict[str, Any], key: str) -> bool:
    return str(metadata.get(key, "")).strip().lower() == "true"


def metadata_cost_share_kind(metadata: dict[str, Any]) -> str:
    kind = str(metadata.get("cost_share_kind", "")).strip().lower()
    if kind in {"deductible", "co_insurance", "reimbursement_ratio", "mixed"}:
        return kind
    return ""


def metadata_cost_share_evidence(metadata: dict[str, Any]) -> str:
    return str(metadata.get("cost_share_evidence", "")).strip().lower()


def metadata_cost_share_value_dependencies(metadata: dict[str, Any]) -> set[str]:
    raw = str(metadata.get("cost_share_value_dependencies", "")).strip().lower()
    if not raw:
        return set()
    return {part.strip() for part in raw.split(",") if part.strip()}


def infer_cost_share_kind_from_node(metadata: dict[str, Any], text: str) -> str:
    kind = metadata_cost_share_kind(metadata)
    if kind:
        return kind
    return detect_cost_share_kind_from_text(text)


def infer_cost_share_value_type_from_node(metadata: dict[str, Any], text: str) -> str:
    value_type = str(metadata.get("cost_share_value_type", "")).strip().lower()
    if value_type in {"percentage", "hkd_amount"}:
        return value_type
    if extract_percentage(text) is not None:
        return "percentage"
    if extract_hkd_amount(text) is not None:
        return "hkd_amount"
    return ""


def node_matches_cost_share_kind(requested_kind: str, candidate_kind: str) -> bool:
    if not requested_kind:
        return True
    if not candidate_kind:
        return False
    return candidate_kind == requested_kind or candidate_kind == "mixed"


def detect_query_intent(question: str) -> QueryIntent:
    normalized = normalize_text(question)
    wants_pre_existing = contains_any(normalized, "pre-existing", "pre existing", "投保前已存在", "既有病況")
    return QueryIntent(
        raw_question=question,
        normalized_question=normalized,
        wants_waiting_period=contains_any(normalized, "waiting period", "等待期", "等候期", "几多日", "幾多日", "多久", "多少日"),
        wants_coverage=contains_any(normalized, "cover", "coverage", "保障", "赔", "賠", "保唔保", "包唔包", "包不包"),
        wants_exclusion=wants_pre_existing or contains_any(normalized, "exclude", "exclusion", "不保", "不受保", "不赔", "不賠"),
        wants_pre_existing=wants_pre_existing,
        wants_comparison=contains_any(normalized, "compare", "comparison", "分别", "分別", "比较", "比較", "邊間", "边间"),
        wants_cancer=contains_any(normalized, "cancer", "癌症", "恶性肿瘤", "惡性腫瘤"),
        wants_injury=contains_any(normalized, "injury", "injuries", "accident", "bodily injury", "受伤", "受傷", "身体损伤", "身體損傷"),
        wants_consult=contains_any(normalized, "consult", "consultation", "vet fee", "診症", "诊症", "獸醫", "兽医", "診金"),
        asks_limit=contains_any(normalized, "annual limit", "limit", "maximum", "max", "最高賠償額", "最高赔偿额", "上限", "每次", "每年"),
        asks_cost_share=contains_any(
            normalized,
            "自負額",
            "自负额",
            "自付額",
            "自付额",
            "deductible",
            "excess",
            "co-insurance",
            "coinsurance",
            "共同保險",
            "共同保险",
            "co-payment",
            "copayment",
            "co payment",
            "賠償比率",
            "reimbursement ratio",
            "reimbursement rate",
        ),
        asks_renewal=contains_any(normalized, "renewal", "renewed", "renew", "自動續保", "自动续保", "可續保", "可续保", "續保", "续保"),
        asks_upgrade=contains_any(normalized, "upgrade", "升級", "升级"),
        asks_cash_benefit=contains_any(normalized, "現金保障", "现金保障", "cash benefit"),
        asks_addon_benefit=contains_any(
            normalized,
            "現金保障",
            "现金保障",
            "cash benefit",
            "額外保額",
            "额外保额",
            "附加保障",
            "貓傳染性腹膜炎",
            "猫传染性腹膜炎",
            "fip",
            "feline infectious peritonitis",
        ),
        asks_chronic_condition=contains_any(normalized, "慢性病況", "慢性病况", "慢性疾病", "chronic medical conditions", "chronic condition"),
        asks_age_limit=contains_any(normalized, "年齡限制", "年龄限制", "幾多歲", "几多岁", "幾歲", "几岁", "age limit", "how old", "years old", "歲起", "岁起", "歲後", "岁后"),
        asks_eligibility=contains_any(normalized, "受保條件", "eligibility", "eligible", "requirements", "條件"),
        target_providers=extract_target_providers(normalized),
    )


def is_general_age_limit_query(intent: QueryIntent) -> bool:
    return (
        intent.asks_age_limit
        and not intent.asks_cash_benefit
        and not intent.asks_upgrade
        and not intent.asks_chronic_condition
        and not intent.asks_renewal
    )


def should_try_generic_exclusion(intent: QueryIntent) -> bool:
    return (
        (intent.wants_exclusion or intent.wants_coverage)
        and not intent.wants_pre_existing
        and not intent.wants_consult
        and not intent.asks_limit
        and not intent.asks_cost_share
        and not intent.asks_chronic_condition
        and not intent.wants_waiting_period
        and not intent.asks_addon_benefit
        and not intent.asks_cash_benefit
        and not intent.asks_age_limit
    )


def question_limit_mentions_hospitalisation(intent: QueryIntent) -> bool:
    return contains_any(
        intent.normalized_question,
        "hospitalisation",
        "hospitalization",
        "hospitalisation limit",
        "hospitalization limit",
        "hospital",
        "room and board",
        "住院",
        "住院費用",
        "住院费用",
    )


def extract_target_providers(normalized_question: str) -> tuple[str, ...]:
    matched: list[str] = []
    for provider_name, aliases in PROVIDER_ALIASES.items():
        if any(alias.lower() in normalized_question for alias in aliases):
            matched.append(provider_name)
    return tuple(matched)


def merge_candidate_nodes(primary: list[NodeWithScore], secondary: list[NodeWithScore]) -> list[NodeWithScore]:
    merged: list[NodeWithScore] = []
    seen: set[tuple[str, str]] = set()
    for node in [*primary, *secondary]:
        key = node_key(node)
        if key in seen:
            continue
        seen.add(key)
        merged.append(node)
    return merged


def lexical_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    question: str,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = rerank_score(intent, candidate)
        if score <= 0.5:
            continue
        scored.append((score, NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: item[0], reverse=True)
    return [node for _, node in scored[: cfg.lexical_backfill_k]]


def comparison_provider_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    language: str | None,
) -> list[NodeWithScore]:
    selected: list[NodeWithScore] = []
    for provider_name in intent.target_providers:
        scored: list[tuple[float, float, NodeWithScore]] = []
        for node in index.storage_context.docstore.docs.values():
            metadata = node.metadata or {}
            if metadata.get("provider") != provider_name:
                continue
            if language and metadata.get("language") != language:
                continue
            candidate = NodeWithScore(node=node, score=0.0)
            answer_score = comparison_provider_answer_score(intent, candidate)
            base_score = rerank_score(intent, candidate)
            if answer_score <= 0.0 and base_score <= 0.5:
                continue
            scored.append((answer_score, base_score, NodeWithScore(node=node, score=max(answer_score, base_score))))
        if not scored:
            continue
        scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
        selected.append(scored[0][2])
    return selected


def generic_exclusion_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = generic_exclusion_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        scored.append((score, rerank_score(intent, candidate), NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in scored[: cfg.lexical_backfill_k]]


def general_age_limit_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = general_age_limit_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        rerank = rerank_score(intent, candidate)
        scored.append((score, rerank, NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in scored[: max(2, cfg.answer_max_sources)]]


def generic_limit_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = generic_limit_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        rerank = rerank_score(intent, candidate)
        scored.append((score, rerank, NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in scored[: max(2, cfg.answer_max_sources)]]


def consult_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = consult_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        rerank = rerank_score(intent, candidate)
        scored.append((score, rerank, NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in scored[: max(2, cfg.answer_max_sources)]]


def cost_share_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = cost_share_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        scored.append((score, NodeWithScore(node=node, score=score)))
    scored.sort(key=lambda item: item[0], reverse=True)
    return [node for _, node in scored[: max(2, cfg.answer_max_sources)]]


def markdown_table_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        scored_fact = markdown_table_fact_from_node(intent, candidate)
        if scored_fact is None:
            continue
        score, _ = scored_fact
        if score <= 0.0:
            continue
        rerank = rerank_score(intent, candidate)
        scored.append((score, rerank, NodeWithScore(node=node, score=score)))

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [node for _, _, node in scored[: max(1, min(2, cfg.answer_max_sources))]]


def chronic_condition_backfill_nodes(
    cfg: PrototypeConfig,
    index: VectorStoreIndex,
    intent: QueryIntent,
    provider: str | None,
    language: str | None,
) -> list[NodeWithScore]:
    scored: list[tuple[float, float, NodeWithScore]] = []
    for node in index.storage_context.docstore.docs.values():
        metadata = node.metadata or {}
        if provider and metadata.get("provider") != provider.lower():
            continue
        if language and metadata.get("language") != language:
            continue
        candidate = NodeWithScore(node=node, score=0.0)
        score = chronic_condition_answer_score(intent, candidate)
        if score <= 0.0:
            continue
        rerank = rerank_score(intent, candidate)
        scored.append((score, rerank, NodeWithScore(node=node, score=score)))

    if not scored:
        return []

    scored.sort(key=lambda item: (item[0], item[1]), reverse=True)
    ranked = [node for _, _, node in scored]
    selected: list[NodeWithScore] = []

    preferred_rule_kind = preferred_chronic_rule_kind(intent)
    if preferred_rule_kind is not None:
        primary = best_chronic_condition_node(intent, ranked, preferred_rule_kind=preferred_rule_kind)
        if primary is not None:
            selected.append(primary)
    else:
        for rule_kind in ("age_4_or_below", "age_5_or_above"):
            candidate = best_chronic_condition_node(intent, ranked, preferred_rule_kind=rule_kind)
            if candidate is not None and all(node_key(candidate) != node_key(existing) for existing in selected):
                selected.append(candidate)

    if intent.asks_upgrade:
        upgrade = best_chronic_condition_node(intent, ranked, preferred_rule_kind="upgrade_additional_coverage")
        if upgrade is not None and (not selected or node_key(upgrade) != node_key(selected[0])):
            selected.append(upgrade)

    seen = {node_key(node) for node in selected}
    for node in ranked:
        if len(selected) >= max(2, cfg.answer_max_sources):
            break
        if node_key(node) in seen:
            continue
        selected.append(node)
        seen.add(node_key(node))
    return selected


def retry_request(func: Any, max_attempts: int) -> Any:
    last_error: Exception | None = None
    for attempt in range(1, max_attempts + 1):
        try:
            return func()
        except Exception as err:
            last_error = err
            if attempt >= max_attempts:
                raise
            time.sleep(min(2.0 * attempt, 6.0))
    if last_error is not None:
        raise last_error
    raise RuntimeError("request failed without an exception")


def persist_index_atomically(cfg: PrototypeConfig, index: VectorStoreIndex, document_count: int) -> None:
    staging_dir = cfg.persist_dir.with_name(cfg.persist_dir.name + ".staging")
    if staging_dir.exists():
        shutil.rmtree(staging_dir)
    staging_dir.mkdir(parents=True, exist_ok=True)
    index.storage_context.persist(persist_dir=str(staging_dir))
    write_index_metadata_to_dir(staging_dir, cfg, document_count)

    if cfg.persist_dir.exists():
        shutil.rmtree(cfg.persist_dir)
    staging_dir.rename(cfg.persist_dir)


def contains_any(text: str, *needles: str) -> bool:
    return any(needle.lower() in text for needle in needles)


def lexical_overlap_score(question: str, text: str) -> float:
    question_terms = [term for term in re.split(r"[^0-9a-zA-Z\u3400-\u9fff]+", question) if term]
    if not question_terms:
        return 0.0
    score = 0.0
    for term in question_terms:
        if term in text:
            score += 0.08 if len(term) <= 2 else 0.16
    return min(score, 1.2)


def semantic_label_match_score(question: str, label: str) -> float:
    question = question.strip().lower()
    label = label.strip().lower()
    if not question or not label:
        return 0.0

    score = lexical_overlap_score(question, label)
    if label in question:
        score += 0.8
    elif question in label:
        score += 0.4

    question_terms = [term for term in re.split(r"[^0-9a-zA-Z\u3400-\u9fff]+", question) if term]
    label_terms = [term for term in re.split(r"[^0-9a-zA-Z\u3400-\u9fff]+", label) if term]
    for q_term in question_terms:
        if len(q_term) <= 3:
            continue
        if any(l_term.startswith(q_term) or q_term.startswith(l_term) for l_term in label_terms if len(l_term) > 3):
            score += 0.1

    question_cjk = [term for term in re.findall(r"[\u3400-\u9fff]{2,}", question) if term]
    label_cjk = [term for term in re.findall(r"[\u3400-\u9fff]{2,}", label) if term]
    if question_cjk and label_cjk:
        if any(l_term in q_term or q_term in l_term for q_term in question_cjk for l_term in label_cjk):
            score += 0.5

    return min(score, 1.8)


def contains_mri_ct_reference(text: str) -> bool:
    return contains_any(
        text,
        "mri",
        "ct scan",
        "ct-scan",
        "computed tomography",
        "核磁共振",
        "電腦斷層掃描",
        "电脑断层扫描",
    ) or contains_ascii_term(text, "ct")


def contains_ascii_term(text: str, term: str) -> bool:
    pattern = r"(?<![a-z0-9])" + re.escape(term.lower()) + r"(?![a-z0-9])"
    return re.search(pattern, text) is not None
