from __future__ import annotations

import json
import math
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

try:
    from .constants import SUPPORTED_PROVIDERS
except ImportError:
    from constants import SUPPORTED_PROVIDERS


HEADING_RE = re.compile(r"^(#{1,6})\s+(.*\S)\s*$")
ANCHOR_RE = re.compile(r"^>\s*([A-Za-z][A-Za-z ]*):\s*(.*?)\s*$")
WORD_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_/\-.]*")
CJK_RE = re.compile(r"[\u3400-\u9fff]")
PUNCT_RE = re.compile(r"[^\w\s]", re.UNICODE)
LIST_ITEM_RE = re.compile(
    r"^(?P<indent>\s*)(?:(?:[-*+])\s+(?:\([a-z0-9ivxlcdm]+\)\s+)?|(?:\(?[a-z0-9ivxlcdm]+\)?[.)])\s+)",
    re.IGNORECASE,
)
PLAN_LINE_RE = re.compile(r"^\s*(?:plan\s+[a-z0-9]+(?:\s+[a-z]+){0,2}|計劃[a-z0-9]+)\s*[:：]", re.IGNORECASE)
MARKDOWN_TABLE_ROW_RE = re.compile(r"^\s*\|.*\|\s*$")
MARKDOWN_TABLE_SEPARATOR_RE = re.compile(r"^\s*\|(?:\s*:?-{3,}:?\s*\|)+\s*$")

CHUNKER_VERSION = "semantic-v9"

GROUPABLE_UNIT_POLICIES: dict[str, dict[str, int]] = {
    "definition": {
        "max_unit_cost": 120,
        "target_cost": 280,
        "max_units": 5,
    },
    "claim_rule": {
        "max_unit_cost": 90,
        "target_cost": 240,
        "max_units": 3,
    },
    "renewal_rule": {
        "max_unit_cost": 90,
        "target_cost": 240,
        "max_units": 3,
    },
    "eligibility": {
        "max_unit_cost": 90,
        "target_cost": 240,
        "max_units": 3,
    },
    "exclusion": {
        "max_unit_cost": 90,
        "target_cost": 240,
        "max_units": 3,
    },
    "general_condition": {
        "max_unit_cost": 90,
        "target_cost": 240,
        "max_units": 3,
    },
}

STRUCTURED_UNIT_TARGET_BUDGETS: dict[str, int] = {
    "claim_rule": 260,
    "exclusion": 260,
    "policy_intro": 220,
    "renewal_rule": 280,
    "waiting_period": 220,
}


@dataclass(frozen=True)
class ChunkRecord:
    text: str
    metadata: dict[str, str]
    token_estimate: int


def detect_language(path: Path, text: str) -> str:
    lower = path.name.lower()
    if any(token in lower for token in ("_zh", "-zh", ".zh", "_cn", "-cn", ".cn")):
        return "zh"
    if any(token in lower for token in ("_en", "-en", ".en")):
        return "en"

    cjk = 0
    total = 0
    for char in text:
        if char.isspace():
            continue
        total += 1
        if "\u4e00" <= char <= "\u9fff":
            cjk += 1
    if total == 0:
        return "en"
    return "zh" if (cjk / total) >= 0.05 else "en"


def parse_front_matter(text: str) -> tuple[dict[str, str], str]:
    match = re.match(r"^---\n(.*?)\n---\n?", text, re.S)
    if not match:
        return {}, text

    front_matter: dict[str, str] = {}
    for raw_line in match.group(1).splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or ":" not in line:
            continue
        key, value = line.split(":", 1)
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if key and value and not value.startswith(("-", "{", "[")):
            front_matter[key] = value

    return front_matter, text[match.end() :]


def normalize_provider(raw: str) -> str:
    return raw.strip().lower()


def should_skip_file(path: Path) -> bool:
    name = path.name
    if name.startswith(".") or name == "README.md":
        return True
    return False


@dataclass(frozen=True)
class MarkdownBlock:
    depth: int
    heading: str
    path: tuple[str, ...]
    heading_line: str
    anchor_lines: tuple[str, ...]
    anchor_map: dict[str, str]
    body: str


@dataclass(frozen=True)
class SemanticUnit:
    depth: int
    path: tuple[str, ...]
    heading_line: str
    anchor_lines: tuple[str, ...]
    anchor_map: dict[str, str]
    body: str
    part_index: int = 1
    part_count: int = 1

    @property
    def scope_path(self) -> tuple[str, ...]:
        if len(self.path) <= 1:
            return ()
        return self.path[:-1]

    @property
    def heading(self) -> str:
        return self.path[-1] if self.path else ""


def estimate_token_cost(text: str) -> int:
    if not text.strip():
        return 0

    cjk_cost = len(CJK_RE.findall(text))
    word_cost = sum(max(1, math.ceil(len(word) / 4)) for word in WORD_RE.findall(text))
    punctuation_cost = math.ceil(len(PUNCT_RE.findall(text)) * 0.15)
    newline_cost = math.ceil(text.count("\n") * 0.1)
    return max(1, cjk_cost + word_cost + punctuation_cost + newline_cost)


def build_chunk_records(data_path: Path, chunk_size: int, chunk_overlap: int) -> list[ChunkRecord]:
    records: list[ChunkRecord] = []
    for path in sorted(data_path.rglob("*.md")):
        if should_skip_file(path):
            continue

        raw_text = path.read_text(encoding="utf-8")
        front_matter, body = parse_front_matter(raw_text)
        provider = normalize_provider(front_matter.get("provider", path.parent.name))
        if provider not in SUPPORTED_PROVIDERS:
            continue

        source_path = str(path.relative_to(data_path))
        language = front_matter.get("language") or detect_language(path, body)
        base_metadata = {
            "provider": provider,
            "source_name": path.name,
            "source_path": source_path,
            "language": language,
            "product": front_matter.get("product", ""),
            "policy_type": front_matter.get("policy_type", ""),
            "source_file": front_matter.get("source_file", ""),
            "source_version_label": front_matter.get("source_version_label", ""),
            "normalization_status": front_matter.get("normalization_status", ""),
            "schema_version": front_matter.get("schema_version", ""),
            "chunker_version": CHUNKER_VERSION,
        }
        records.extend(
            chunk_markdown_document(
                body=body,
                metadata=base_metadata,
                chunk_size=chunk_size,
                chunk_overlap=chunk_overlap,
            )
        )
    return records


def chunk_markdown_document(
    body: str,
    metadata: dict[str, str],
    chunk_size: int,
    chunk_overlap: int,
) -> list[ChunkRecord]:
    blocks = parse_markdown_blocks(body)
    if not blocks:
        trimmed = body.strip()
        if not trimmed:
            return []
        return [
            ChunkRecord(
                text=trimmed,
                metadata={**metadata, "chunk_index": "0"},
                token_estimate=estimate_token_cost(trimmed),
            )
        ]

    units = build_semantic_units(blocks, chunk_size, chunk_overlap)
    bundles = bundle_units(units, chunk_size)

    records: list[ChunkRecord] = []
    for idx, bundle in enumerate(bundles):
        rendered = render_bundle(bundle)
        if not rendered:
            continue
        bundle_metadata = bundle_metadata_for(bundle)
        merged_metadata = {
            **metadata,
            **bundle_metadata,
            "chunk_index": str(idx),
        }
        token_estimate = estimate_token_cost(rendered)
        merged_metadata["token_estimate"] = str(token_estimate)
        records.append(
            ChunkRecord(
                text=rendered,
                metadata=merged_metadata,
                token_estimate=token_estimate,
            )
        )
    return records


def parse_markdown_blocks(body: str) -> list[MarkdownBlock]:
    lines = body.splitlines()
    blocks: list[MarkdownBlock] = []

    path_stack: list[str] = []
    current_heading: str | None = None
    current_depth = 0
    current_anchor_lines: list[str] = []
    current_anchor_map: dict[str, str] = {}
    current_body_lines: list[str] = []

    def flush_current() -> None:
        nonlocal current_heading, current_depth, current_anchor_lines, current_anchor_map, current_body_lines
        if current_heading is None:
            current_body_lines = []
            return

        body_text = "\n".join(current_body_lines).strip()
        if body_text:
            blocks.append(
                MarkdownBlock(
                    depth=current_depth,
                    heading=current_heading,
                    path=tuple(path_stack[:current_depth]),
                    heading_line="#" * current_depth + " " + current_heading,
                    anchor_lines=tuple(current_anchor_lines),
                    anchor_map=dict(current_anchor_map),
                    body=body_text,
                )
            )

        current_heading = None
        current_depth = 0
        current_anchor_lines = []
        current_anchor_map = {}
        current_body_lines = []

    for raw_line in lines:
        heading_match = HEADING_RE.match(raw_line)
        if heading_match:
            flush_current()
            depth = len(heading_match.group(1))
            title = heading_match.group(2).strip()
            path_stack = path_stack[: depth - 1]
            path_stack.append(title)
            current_heading = title
            current_depth = depth
            continue

        if current_heading is None:
            continue

        stripped = raw_line.strip()
        if not stripped and not current_anchor_lines and not current_body_lines:
            continue
        anchor_match = ANCHOR_RE.match(stripped)
        if anchor_match and not current_body_lines:
            key = anchor_match.group(1).strip().lower().replace(" ", "_")
            current_anchor_lines.append(stripped)
            current_anchor_map[key] = anchor_match.group(2).strip()
            continue

        current_body_lines.append(raw_line.rstrip())

    flush_current()
    return blocks


def build_semantic_units(
    blocks: list[MarkdownBlock],
    chunk_size: int,
    chunk_overlap: int,
) -> list[SemanticUnit]:
    units: list[SemanticUnit] = []
    for block in blocks:
        units.extend(split_block_to_units(block, chunk_size, chunk_overlap))
    return units


def split_block_to_units(
    block: MarkdownBlock,
    chunk_size: int,
    chunk_overlap: int,
) -> list[SemanticUnit]:
    prefix_text = render_path_prefix(block.path[:-1]) + "\n\n" if block.path[:-1] else ""
    header_text = "\n".join(
        [block.heading_line, *block.anchor_lines]
    ).strip()
    prefix_budget = estimate_token_cost(prefix_text + header_text) + 12
    body_budget = max(120, chunk_size - prefix_budget)
    body_budget = min(body_budget, preferred_structured_body_budget(block, body_budget))
    body_cost = estimate_token_cost(block.body)
    components = split_body_components(block.body, body_budget)

    if (
        body_cost <= body_budget
        and not should_force_structured_split(block.body, body_budget)
        and not should_isolate_plan_limit_components(block, components)
    ):
        return [
            SemanticUnit(
                depth=block.depth,
                path=block.path,
                heading_line=block.heading_line,
                anchor_lines=block.anchor_lines,
                anchor_map=block.anchor_map,
                body=block.body,
            )
        ]

    component_groups = pack_block_components(block, components, body_budget, chunk_overlap)

    units: list[SemanticUnit] = []
    for idx, group in enumerate(component_groups, start=1):
        units.append(
            SemanticUnit(
                depth=block.depth,
                path=block.path,
                heading_line=block.heading_line,
                anchor_lines=block.anchor_lines,
                anchor_map=block.anchor_map,
                body=render_component_group(group),
                part_index=idx,
                part_count=len(component_groups),
            )
        )
    return units


def pack_block_components(
    block: MarkdownBlock,
    components: list[str],
    body_budget: int,
    chunk_overlap: int,
) -> list[list[str]]:
    if should_isolate_plan_limit_components(block, components):
        return pack_components_with_plan_isolation(components, body_budget, chunk_overlap)
    return pack_components(components, body_budget, chunk_overlap)


def should_isolate_plan_limit_components(block: MarkdownBlock, components: list[str]) -> bool:
    unit_type = block.anchor_map.get("unit", "").strip().lower()
    if unit_type not in {"benefit", "benefit_item"}:
        return False
    if len(components) < 2:
        return False
    plan_components = [component for component in components if is_plan_limit_component(component)]
    non_plan_components = [component for component in components if not is_plan_limit_component(component)]
    return bool(plan_components and non_plan_components)


def pack_components_with_plan_isolation(
    components: list[str],
    body_budget: int,
    chunk_overlap: int,
) -> list[list[str]]:
    groups: list[list[str]] = []
    pending: list[str] = []

    def flush_pending() -> None:
        nonlocal pending
        if not pending:
            return
        groups.extend(pack_components(pending, body_budget, chunk_overlap))
        pending = []

    for component in components:
        if is_plan_limit_component(component):
            flush_pending()
            groups.append([component])
            continue
        pending.append(component)

    flush_pending()
    return groups


def should_force_structured_split(body: str, body_budget: int) -> bool:
    body_cost = estimate_token_cost(body)
    if body_cost < max(150, int(body_budget * 0.55)):
        return False

    lines = [line.rstrip() for line in body.splitlines() if line.strip()]
    if len(lines) < 4:
        return False

    _, groups = split_structured_lines(lines)
    return len(groups) >= 4


def preferred_structured_body_budget(block: MarkdownBlock, body_budget: int) -> int:
    unit_type = block.anchor_map.get("unit", "").strip().lower()
    target = STRUCTURED_UNIT_TARGET_BUDGETS.get(unit_type)
    if not target:
        return body_budget
    return max(120, min(body_budget, target))


def split_body_components(body: str, body_budget: int) -> list[str]:
    raw_components = [part.strip() for part in re.split(r"\n\s*\n", body) if part.strip()]
    components: list[str] = []
    for component in raw_components:
        table_aware = split_markdown_table_component(component, body_budget)
        if len(table_aware) > 1:
            for part in table_aware:
                if estimate_token_cost(part) <= body_budget:
                    components.append(part)
                else:
                    components.extend(split_oversized_component(part, body_budget))
            continue
        if estimate_token_cost(component) <= body_budget:
            components.append(component)
            continue
        components.extend(split_oversized_component(component, body_budget))
    return components


def split_markdown_table_component(component: str, body_budget: int) -> list[str]:
    lines = [line.rstrip() for line in component.splitlines()]
    if len(lines) < 3:
        return [component]

    parts: list[str] = []
    current_text: list[str] = []
    idx = 0
    while idx < len(lines):
        if is_markdown_table_start(lines, idx):
            preface = "\n".join(line for line in current_text if line.strip()).strip()
            current_text = []

            header = lines[idx].strip()
            separator = lines[idx + 1].strip()
            row_lines: list[str] = []
            idx += 2
            while idx < len(lines) and is_markdown_table_row(lines[idx]):
                row_lines.append(lines[idx].strip())
                idx += 1

            table_groups = split_markdown_table_rows(header, separator, row_lines, body_budget)
            if preface:
                if table_groups:
                    first = f"{preface}\n\n{table_groups[0]}".strip()
                    if estimate_token_cost(first) <= body_budget:
                        parts.append(first)
                        parts.extend(table_groups[1:])
                    else:
                        parts.append(preface)
                        parts.extend(table_groups)
                else:
                    parts.append(preface)
            else:
                parts.extend(table_groups)
            continue

        current_text.append(lines[idx])
        idx += 1

    trailing = "\n".join(line for line in current_text if line.strip()).strip()
    if trailing:
        parts.append(trailing)
    return parts or [component]


def split_markdown_table_rows(header: str, separator: str, rows: list[str], body_budget: int) -> list[str]:
    if not rows:
        return ["\n".join([header, separator]).strip()]

    full_table = "\n".join([header, separator, *rows]).strip()
    if estimate_token_cost(full_table) <= body_budget:
        return [full_table]

    row_budget = max(40, body_budget - estimate_token_cost("\n".join([header, separator])) - 4)
    grouped_rows = pack_components(rows, row_budget, 0)
    if not grouped_rows:
        grouped_rows = [[row] for row in rows]
    return ["\n".join([header, separator, *group]).strip() for group in grouped_rows if group]


def is_markdown_table_start(lines: list[str], idx: int) -> bool:
    return idx + 1 < len(lines) and is_markdown_table_row(lines[idx]) and is_markdown_table_separator(lines[idx + 1])


def is_markdown_table_row(line: str) -> bool:
    return MARKDOWN_TABLE_ROW_RE.match(line.strip()) is not None


def is_markdown_table_separator(line: str) -> bool:
    return MARKDOWN_TABLE_SEPARATOR_RE.match(line.strip()) is not None


def split_oversized_component(component: str, body_budget: int) -> list[str]:
    semantic_groups = split_structured_component(component, body_budget)
    if len(semantic_groups) > 1:
        normalized_groups: list[str] = []
        for group in semantic_groups:
            if estimate_token_cost(group) <= body_budget:
                normalized_groups.append(group)
                continue
            normalized_groups.extend(split_oversized_component_fallback(group, body_budget))
        if len(normalized_groups) > 1:
            return normalized_groups
        if normalized_groups:
            return normalized_groups

    return split_oversized_component_fallback(component, body_budget)


def split_oversized_component_fallback(component: str, body_budget: int) -> list[str]:
    lines = [line.rstrip() for line in component.splitlines() if line.strip()]
    if len(lines) > 1:
        line_groups = pack_components(lines, body_budget, 0)
        if len(line_groups) > 1:
            return ["\n".join(group).strip() for group in line_groups if group]

    sentences = split_sentences(component)
    if len(sentences) > 1:
        sentence_groups = pack_components(sentences, body_budget, 0)
        if len(sentence_groups) > 1:
            return [" ".join(group).strip() for group in sentence_groups if group]

    return split_by_hard_limit(component, body_budget)


def split_structured_component(component: str, body_budget: int) -> list[str]:
    lines = [line.rstrip() for line in component.splitlines() if line.strip()]
    if len(lines) < 3:
        return []

    preface, grouped_items = split_structured_lines(lines)
    if len(grouped_items) < 2:
        nested_groups = split_nested_structured_group(component, body_budget)
        return nested_groups if len(nested_groups) > 1 else []

    semantic_items = ["\n".join(group).strip() for group in grouped_items if any(line.strip() for line in group)]
    if len(semantic_items) < 2:
        nested_groups = split_nested_structured_groups(preface, grouped_items, body_budget)
        return nested_groups if len(nested_groups) > 1 else []

    packed = pack_components(semantic_items, body_budget, 0)
    if len(packed) < 2:
        rendered = [render_structured_group(preface, group) for group in semantic_items]
    else:
        rendered = [render_structured_group(preface, "\n\n".join(group).strip()) for group in packed if group]

    normalized_groups: list[str] = []
    for group in rendered:
        if estimate_token_cost(group) <= body_budget:
            normalized_groups.append(group)
            continue
        nested_groups = split_nested_structured_group(group, body_budget)
        if len(nested_groups) > 1:
            normalized_groups.extend(nested_groups)
        else:
            normalized_groups.append(group)
    return normalized_groups


def split_nested_structured_group(component: str, body_budget: int) -> list[str]:
    lines = [line.rstrip() for line in component.splitlines() if line.strip()]
    if len(lines) < 3:
        return []

    preface, grouped_items = split_subordinate_structured_lines(lines)
    if len(grouped_items) < 2:
        return []

    semantic_items = ["\n".join(group).strip() for group in grouped_items if any(line.strip() for line in group)]
    if len(semantic_items) < 2:
        return []

    normalized_groups: list[str] = []
    preface_cost = estimate_token_cost("\n".join(preface).strip()) + 4
    item_budget = max(60, body_budget - preface_cost)
    packed = pack_components(semantic_items, item_budget, 0)
    if not packed:
        packed = [[item] for item in semantic_items]

    for group in packed:
        body = "\n\n".join(group).strip()
        rendered = render_structured_group(preface, body)
        if estimate_token_cost(rendered) <= body_budget:
            normalized_groups.append(rendered)
            continue

        fallback_groups = split_oversized_component_fallback(body, body_budget)
        if len(fallback_groups) > 1:
            normalized_groups.extend(render_structured_group(preface, part) for part in fallback_groups)
        else:
            normalized_groups.append(rendered)
    return normalized_groups


def split_subordinate_structured_lines(lines: list[str]) -> tuple[list[str], list[list[str]]]:
    if not lines:
        return [], []

    lead_preface: list[str] = []
    idx = 0
    while idx < len(lines):
        marker = semantic_line_indent(lines[idx])
        if marker is not None:
            break
        lead_preface.append(lines[idx])
        idx += 1

    if idx >= len(lines):
        return lead_preface, []

    parent_line = lines[idx]
    parent_indent = semantic_line_indent(parent_line)
    if parent_indent is None:
        return lead_preface, []

    preface = [*lead_preface, parent_line]
    groups: list[list[str]] = []
    current: list[str] = []
    current_indent: int | None = None

    for line in lines[idx + 1 :]:
        marker = semantic_line_indent(line)
        if marker is None:
            if current:
                current.append(line)
            else:
                preface.append(line)
            continue

        if marker <= parent_indent:
            return preface, groups

        if current and current_indent is not None and marker <= current_indent:
            groups.append(current)
            current = [line]
            current_indent = marker
            continue

        if not current:
            current = [line]
            current_indent = marker
            continue

        current.append(line)

    if current:
        groups.append(current)

    return preface, groups


def split_structured_lines(lines: list[str]) -> tuple[list[str], list[list[str]]]:
    preface: list[str] = []
    groups: list[list[str]] = []
    current: list[str] = []
    current_indent: int | None = None

    for line in lines:
        marker = semantic_line_indent(line)
        if marker is None:
            if current:
                current.append(line)
            else:
                preface.append(line)
            continue

        if current and current_indent is not None and marker <= current_indent:
            groups.append(current)
            current = [line]
            current_indent = marker
            continue

        if not current:
            current = [line]
            current_indent = marker
            continue

        current.append(line)

    if current:
        groups.append(current)

    return preface, groups


def render_structured_group(preface: list[str], body: str) -> str:
    if not preface:
        return body.strip()
    return "\n".join([*preface, body]).strip()


def is_plan_limit_line(line: str) -> bool:
    stripped = line.strip()
    if not stripped:
        return False
    return PLAN_LINE_RE.match(stripped) is not None


def is_plan_limit_component(component: str) -> bool:
    lines = [line.rstrip() for line in component.splitlines() if line.strip()]
    if not lines:
        return False
    return any(is_plan_limit_line(line) for line in lines)


def semantic_line_indent(line: str) -> int | None:
    list_match = LIST_ITEM_RE.match(line)
    if list_match:
        return len(list_match.group("indent") or "")
    if is_plan_limit_line(line):
        return len(line) - len(line.lstrip())
    return None


def split_sentences(text: str) -> list[str]:
    normalized = re.sub(r"\s+", " ", text).strip()
    if not normalized:
        return []
    parts = re.split(r"(?<=[。！？；.!?;:])\s+", normalized)
    return [part.strip() for part in parts if part.strip()]


def split_by_hard_limit(text: str, body_budget: int) -> list[str]:
    words = text.split()
    if not words:
        return [text.strip()]

    segments: list[str] = []
    current: list[str] = []
    current_cost = 0
    for word in words:
        word_cost = estimate_token_cost(word)
        if current and current_cost + word_cost > body_budget:
            segments.append(" ".join(current).strip())
            current = [word]
            current_cost = word_cost
            continue
        current.append(word)
        current_cost += word_cost
    if current:
        segments.append(" ".join(current).strip())
    return segments


def pack_components(
    components: Iterable[str],
    body_budget: int,
    overlap_budget: int,
) -> list[list[str]]:
    groups: list[list[str]] = []
    current: list[str] = []
    current_cost = 0
    component_list = [component for component in components if component.strip()]

    for component in component_list:
        component_cost = estimate_token_cost(component)
        if current and current_cost + component_cost > body_budget:
            groups.append(current)
            overlap_components = trailing_overlap_components(current, overlap_budget)
            current = [*overlap_components, component]
            current_cost = sum(estimate_token_cost(item) for item in current)
            while len(current) > 1 and current_cost > body_budget:
                current.pop(0)
                current_cost = sum(estimate_token_cost(item) for item in current)
            continue

        current.append(component)
        current_cost += component_cost

    if current:
        groups.append(current)
    return groups


def render_component_group(group: list[str]) -> str:
    cleaned = [component.strip() for component in group if component.strip()]
    if len(cleaned) <= 1:
        return "\n\n".join(cleaned).strip()

    line_groups = [[line.rstrip() for line in component.splitlines() if line.strip()] for component in cleaned]
    if not line_groups or any(not lines for lines in line_groups):
        return "\n\n".join(cleaned).strip()

    common_prefix_len = 0
    max_prefix = min(len(lines) for lines in line_groups)
    while common_prefix_len < max_prefix:
        candidate = line_groups[0][common_prefix_len]
        if any(lines[common_prefix_len] != candidate for lines in line_groups[1:]):
            break
        common_prefix_len += 1

    if common_prefix_len == 0 or any(len(lines) == common_prefix_len for lines in line_groups):
        return "\n\n".join(cleaned).strip()

    prefix = line_groups[0][:common_prefix_len]
    remainders = ["\n".join(lines[common_prefix_len:]).strip() for lines in line_groups]
    if any(not remainder for remainder in remainders):
        return "\n\n".join(cleaned).strip()

    return "\n".join(prefix + ["", *remainders]).strip()


def trailing_overlap_components(components: list[str], overlap_budget: int) -> list[str]:
    if overlap_budget <= 0:
        return []

    result: list[str] = []
    running_cost = 0
    for component in reversed(components):
        component_cost = estimate_token_cost(component)
        if result and running_cost + component_cost > overlap_budget:
            break
        result.append(component)
        running_cost += component_cost
    return list(reversed(result))


def bundle_units(units: list[SemanticUnit], chunk_size: int) -> list[list[SemanticUnit]]:
    if not units:
        return []

    bundles: list[list[SemanticUnit]] = []
    current: list[SemanticUnit] = []

    for unit in units:
        if not current:
            current = [unit]
            continue

        if can_merge_units(current, unit, chunk_size):
            current.append(unit)
            continue

        bundles.append(current)
        current = [unit]

    if current:
        bundles.append(current)
    return bundles


def can_merge_units(bundle: list[SemanticUnit], candidate: SemanticUnit, chunk_size: int) -> bool:
    scope = bundle[0].scope_path
    if candidate.scope_path != scope:
        return False
    if candidate.path[:-1] != bundle[-1].path[:-1]:
        return False

    unit_type = normalized_unit_type(bundle[0])
    if not unit_type or unit_type != normalized_unit_type(candidate):
        return False

    policy = GROUPABLE_UNIT_POLICIES.get(unit_type)
    if not policy:
        return False

    if unit_type == "general_condition":
        if any(general_condition_unit_requires_isolation(unit) for unit in [*bundle, candidate]):
            return False
    if unit_type == "definition":
        if any(definition_unit_requires_isolation(unit) for unit in [*bundle, candidate]):
            return False

    if len(bundle) >= policy["max_units"]:
        return False

    all_units = [*bundle, candidate]
    if any(estimate_token_cost(unit.body) > policy["max_unit_cost"] for unit in all_units):
        return False

    if unit_type != "definition" and primary_source_page(bundle[0]) != primary_source_page(candidate):
        return False

    prospective = render_bundle(all_units)
    return estimate_token_cost(prospective) <= min(chunk_size, policy["target_cost"])


def render_bundle(units: list[SemanticUnit]) -> str:
    if not units:
        return ""

    common_prefix = units[0].scope_path
    lines: list[str] = []
    if common_prefix:
        lines.extend(render_heading_lines(common_prefix))
        lines.append("")

    for idx, unit in enumerate(units):
        heading_included_by_prefix = len(unit.path) == len(common_prefix)
        if not heading_included_by_prefix:
            lines.append(unit.heading_line)
        lines.extend(unit.anchor_lines)
        if unit.part_count > 1:
            lines.append(f"> Part: {unit.part_index}/{unit.part_count}")
        lines.append("")
        lines.append(unit.body.strip())
        if idx != len(units) - 1:
            lines.append("")

    return "\n".join(line.rstrip() for line in lines if line is not None).strip()


def render_heading_lines(path: tuple[str, ...]) -> list[str]:
    return [("#" * depth) + " " + title for depth, title in enumerate(path, start=1)]


def render_path_prefix(path: tuple[str, ...]) -> str:
    return "\n".join(render_heading_lines(path)).strip()


def normalized_unit_type(unit: SemanticUnit) -> str:
    return unit.anchor_map.get("unit", "").strip().lower()


def general_condition_unit_requires_isolation(unit: SemanticUnit) -> bool:
    if normalized_unit_type(unit) != "general_condition":
        return False

    topic_text = normalize_topic_text(
        " ".join(
            [
                *unit.path,
                *unit.anchor_lines,
                unit.body,
            ]
        )
    )
    if contains_topic(
        topic_text,
        "age limit",
        "pet eligibility",
        "eligibility",
        "年齡限制",
        "年龄限制",
        "投保年齡",
        "投保年龄",
        "年齡為",
        "年龄为",
        "weeks old",
        "years old",
        "星期以上",
        "歲以下",
        "岁以下",
        "micro-chipped",
        "vaccinations",
        "working pet",
    ):
        return True

    bullet_lines = [line for line in unit.body.splitlines() if LIST_ITEM_RE.match(line.strip())]
    return len(bullet_lines) >= 2


def definition_unit_requires_isolation(unit: SemanticUnit) -> bool:
    if normalized_unit_type(unit) != "definition":
        return False

    topic_text = normalize_topic_text(
        " ".join(
            [
                *unit.path,
                *unit.anchor_lines,
                unit.body,
            ]
        )
    )
    if estimate_token_cost(unit.body) > 85:
        return True

    if any(LIST_ITEM_RE.match(line.strip()) for line in unit.body.splitlines()):
        return True

    return contains_topic(
        topic_text,
        "co-insurance",
        "coinsurance",
        "共同保險",
        "共同保险",
        "co-payment",
        "copayment",
        "co payment",
        "excess",
        "deductible",
        "自負額",
        "自负额",
        "自付額",
        "自付额",
        "pre-existing",
        "pre existing",
        "已存在病況",
        "已存在病况",
        "waiting period",
        "等候期",
        "賠償比率",
        "reimbursement ratio",
        "reimbursement rate",
        "renewal",
        "續保",
        "续保",
    )


def primary_source_page(unit: SemanticUnit) -> str:
    return unit.anchor_map.get("source", "").strip()


def bundle_metadata_for(units: list[SemanticUnit]) -> dict[str, str]:
    scope_path = units[0].scope_path if units else ()
    section_path = " > ".join(scope_path)
    heading_path = json.dumps([list(unit.path) for unit in units], ensure_ascii=False)
    source_pages = sorted(
        {
            unit.anchor_map.get("source", "")
            for unit in units
            if unit.anchor_map.get("source", "").strip()
        }
    )
    clauses = sorted(
        {
            unit.anchor_map.get("clause", "")
            for unit in units
            if unit.anchor_map.get("clause", "").strip()
        }
    )
    unit_types = sorted(
        {
            unit.anchor_map.get("unit", "")
            for unit in units
            if unit.anchor_map.get("unit", "").strip()
        }
    )
    topic_tags = infer_topic_tags(units)
    definition_labels = infer_definition_labels(units)
    benefit_labels = infer_benefit_labels(units)
    list_item_labels = infer_list_item_labels(units)
    table_metadata = infer_markdown_table_metadata(units)
    plan_limit_metadata = infer_plan_limit_metadata(units)
    cost_share_metadata = infer_cost_share_metadata(units)

    return {
        "section_path": section_path,
        "heading_path": heading_path,
        "source_pages": ", ".join(source_pages),
        "clauses": ", ".join(clauses),
        "unit_types": ", ".join(unit_types),
        "topic_tags": ", ".join(topic_tags),
        "definition_labels": ", ".join(definition_labels),
        "benefit_labels": ", ".join(benefit_labels),
        "list_item_labels": ", ".join(list_item_labels),
        "list_item_count": str(len(list_item_labels)) if list_item_labels else "0",
        **table_metadata,
        **plan_limit_metadata,
        "bundle_unit_count": str(len(units)),
        **cost_share_metadata,
    }


def infer_topic_tags(units: list[SemanticUnit]) -> list[str]:
    if not units:
        return []

    tags: set[str] = set()
    topic_text = normalize_topic_text(
        "\n".join(
            [
                " ".join(unit.path)
                + "\n"
                + " ".join(unit.anchor_lines)
                + "\n"
                + unit.body
                for unit in units
            ]
        )
    )
    unit_types = {unit.anchor_map.get("unit", "").strip().lower() for unit in units}

    if "waiting_period" in unit_types:
        tags.add("waiting_period")
    if "definition" in unit_types and contains_topic(topic_text, "等待期", "等候期", "waiting period"):
        tags.add("waiting_period")
    if "definition" in unit_types:
        tags.add("definition")
    if "eligibility" in unit_types:
        tags.add("eligibility")
    if "general_condition" in unit_types and contains_topic(
        topic_text,
        "eligibility",
        "eligible",
        "age limit",
        "pet eligibility",
        "年齡限制",
        "年龄限制",
        "投保年齡",
        "投保年龄",
        "年齡為",
        "年龄为",
        "micro-chipped",
        "vaccinations",
        "working pet",
    ):
        tags.add("eligibility")
    if "exclusion" in unit_types:
        tags.add("exclusion")
    if "benefit" in unit_types:
        tags.add("benefit")
    if contains_topic(topic_text, "cancer", "癌症", "恶性肿瘤", "惡性腫瘤"):
        tags.add("cancer")
    if contains_mri_ct_topic(topic_text):
        tags.add("mri_ct")
    if contains_topic(topic_text, "renewal", "續保", "续保"):
        tags.add("renewal")
    if contains_topic(topic_text, "upgrade", "升級", "升级"):
        tags.add("upgrade")
    if contains_topic(topic_text, "table of benefits", "保障項目表"):
        tags.add("table_of_benefits")
    if contains_topic(topic_text, "policy schedule", "承保表", "保單承保表", "保单承保表"):
        tags.add("policy_schedule")
    if any(parse_markdown_tables(unit.body) for unit in units):
        tags.add("markdown_table")
    if contains_topic(
        topic_text,
        "age limit",
        "年齡限制",
        "年龄限制",
        "投保年齡",
        "投保年龄",
        "weeks old",
        "years old",
        "星期以上",
        "歲以下",
        "岁以下",
    ):
        tags.add("age_limit")
    if contains_topic(topic_text, "pre-existing", "pre existing", "投保前已存在", "已存在之狀況", "已存在状况", "已存在病況", "已存在病况"):
        tags.add("pre_existing")
    if contains_topic(topic_text, "critical illness", "危疾"):
        tags.add("critical_illness")
    if contains_topic(topic_text, "貓傳染性腹膜炎", "猫传染性腹膜炎", "fip"):
        tags.add("fip")
    if contains_topic(topic_text, "consult", "consultation", "vet consultation", "診症", "诊症", "診金"):
        tags.add("consult")
    if contains_topic(topic_text, "injury", "injuries", "受傷", "受伤", "意外"):
        tags.add("injury")
    if contains_topic(topic_text, "illness", "疾病", "病況", "病况"):
        tags.add("illness")

    cost_share_kind = infer_cost_share_kind_for_units(units)
    if cost_share_kind:
        tags.add("cost_share")
    if cost_share_kind == "deductible":
        tags.add("cost_share_deductible")
    if cost_share_kind == "co_insurance":
        tags.add("cost_share_co_insurance")
    if cost_share_kind == "reimbursement_ratio":
        tags.add("cost_share_reimbursement_ratio")

    if "waiting_period" in tags and "cancer" in tags and not {"renewal", "upgrade", "critical_illness", "fip", "mri_ct"} & tags:
        tags.add("standard_waiting_period")
    if "waiting_period" in tags and {"critical_illness", "fip"} & tags:
        tags.add("addon_waiting_period")

    return sorted(tags)


def normalize_topic_text(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip().lower()


def infer_definition_labels(units: list[SemanticUnit]) -> list[str]:
    labels: set[str] = set()
    for unit in units:
        if normalized_unit_type(unit) != "definition":
            continue
        label = definition_label_for_heading(unit.heading)
        if not label:
            continue
        for part in re.split(r"\s*/\s*", label):
            normalized = normalize_definition_label(part)
            if normalized:
                labels.add(normalized)
    return sorted(labels)


def infer_benefit_labels(units: list[SemanticUnit]) -> list[str]:
    labels: set[str] = set()
    for unit in units:
        unit_type = normalized_unit_type(unit)
        if unit_type not in {"benefit", "benefit_item", "benefit_table"}:
            continue
        for label in benefit_labels_for_unit(unit):
            normalized = normalize_benefit_label(label)
            if not normalized:
                continue
            labels.add(normalized)
            labels.update(expand_benefit_label_aliases(normalized))
    return sorted(label for label in labels if label)


def benefit_labels_for_unit(unit: SemanticUnit) -> list[str]:
    labels: list[str] = []
    heading_label = benefit_label_for_heading(unit.heading)
    if heading_label:
        labels.append(heading_label)

    if normalized_unit_type(unit) == "benefit_table":
        for raw_label in extract_list_item_labels(unit.body):
            labels.extend(split_benefit_table_item_label(raw_label))
    return labels


def split_benefit_table_item_label(label: str) -> list[str]:
    normalized = normalize_benefit_label(label)
    if not normalized:
        return []

    parts = [normalized]
    stripped = re.sub(r"^section\s+[ivx0-9a-z]+[\s\-–—:]*", "", normalized, flags=re.IGNORECASE).strip()
    if stripped and stripped != normalized:
        parts.append(stripped)

    if "including" in stripped:
        _, remainder = stripped.split("including", 1)
        for part in re.split(r",| and ", remainder):
            candidate = normalize_benefit_label(part)
            if candidate:
                parts.append(candidate)

    if "包括" in stripped:
        _, remainder = stripped.split("包括", 1)
        for part in re.split(r"[、，及和]", remainder):
            candidate = normalize_benefit_label(part)
            if candidate:
                parts.append(candidate)

    return [part for part in parts if part]


def infer_list_item_labels(units: list[SemanticUnit]) -> list[str]:
    labels: set[str] = set()
    for unit in units:
        for label in extract_list_item_labels(unit.body):
            normalized = normalize_list_item_label(label)
            if normalized:
                labels.add(normalized)
    return sorted(labels)


def extract_list_item_labels(text: str) -> list[str]:
    labels: list[str] = []
    lines = [line.rstrip() for line in text.splitlines() if line.strip()]
    for line in lines:
        stripped = strip_list_marker(line)
        if not stripped:
            continue
        candidate = stripped.strip()
        if not candidate:
            continue
        if candidate.endswith(":"):
            candidate = candidate[:-1].strip()
        if candidate:
            labels.append(candidate)
    return labels


def strip_list_marker(line: str) -> str:
    match = LIST_ITEM_RE.match(line)
    if not match:
        return ""
    return line[match.end() :].strip()


def definition_label_for_heading(heading: str) -> str:
    cleaned = heading.strip()
    cleaned = re.sub(r"^(?:definition|definitions)\s*[:：-]\s*", "", cleaned, flags=re.IGNORECASE)
    cleaned = re.sub(r"^(?:定義|定义)\s*[:：-]\s*", "", cleaned)
    return normalize_definition_label(cleaned)


def benefit_label_for_heading(heading: str) -> str:
    cleaned = heading.strip()
    cleaned = re.sub(r"^[A-Z]\s*[.)]\s*", "", cleaned)
    cleaned = re.sub(r"^\d+\s*[.)]\s*", "", cleaned)
    cleaned = re.sub(r"^section\s+[ivx0-9a-z]+[\s\-–—:]*", "", cleaned, flags=re.IGNORECASE)
    cleaned = re.sub(r"^第\s*\d+\s*節[\s\-–—:]*", "", cleaned)
    cleaned = re.sub(r"^第[一二三四五六七八九十]+\s*部分[\s\-–—:]*", "", cleaned)
    cleaned = re.sub(r"^項目[一二三四五六七八九十][\s\-–—:]*", "", cleaned)
    return normalize_benefit_label(cleaned)


def normalize_definition_label(label: str) -> str:
    cleaned = label.strip()
    cleaned = cleaned.strip("\"'“”‘’`「」『』()（）[]")
    cleaned = re.sub(r"\s+", " ", cleaned).strip()
    return cleaned.lower()


def normalize_benefit_label(label: str) -> str:
    cleaned = normalize_definition_label(label)
    cleaned = cleaned.strip(" .;,:-")
    cleaned = re.sub(r"\s+", " ", cleaned).strip()
    return cleaned


def expand_benefit_label_aliases(label: str) -> set[str]:
    aliases: set[str] = set()

    if contains_topic(label, "room and board", "overnight hospitalisation", "hospitalisation", "hospitalization", "住院費用", "過夜住院費用", "「過夜」住院費用"):
        aliases.update(
            {
                "room and board",
                "room and board expenses",
                "hospitalisation",
                "hospitalization",
                "overnight hospitalisation",
                "住院",
                "住院費用",
            }
        )
    if contains_topic(label, "consultation", "veterinary consultation", "general consultation", "specialist consultation", "獸醫診症", "普通科獸醫診金", "專科獸醫診金"):
        aliases.update(
            {
                "consultation",
                "veterinary consultation",
                "general consultation",
                "specialist consultation",
                "獸醫診症",
                "診症",
                "診金",
            }
        )
    if contains_topic(label, "chemotherapy", "化療"):
        aliases.update({"chemotherapy", "化療"})
    if contains_topic(label, "funeral", "final expenses", "殮葬", "葬禮", "葬仪"):
        aliases.update({"funeral expenses", "funeral service expenses", "final expenses benefit", "殮葬服務費用", "殮葬費用"})
    if contains_topic(label, "third party", "legal liability", "第三者責任"):
        aliases.update({"third party liability", "third party legal liability", "第三者責任保障", "第三者責任"})
    if contains_topic(label, "emergency boarding", "pet sitting", "緊急寄宿", "寵物托管", "宠物托管"):
        aliases.update({"emergency boarding", "emergency pet sitting care", "緊急寄宿", "寵物托管"})
    if contains_topic(label, "surgical benefit", "surgery", "手術"):
        aliases.update({"surgical benefit", "surgery", "手術", "手術費用"})

    aliases.discard("")
    return aliases


def normalize_list_item_label(label: str) -> str:
    cleaned = normalize_definition_label(label)
    cleaned = re.sub(r"^[a-z]\)\s*", "", cleaned)
    cleaned = re.sub(r"^[ivxlcdm]+\)\s*", "", cleaned)
    cleaned = re.sub(r"^[0-9]+\)\s*", "", cleaned)
    cleaned = re.sub(r"^[0-9]+\.\s*", "", cleaned)
    cleaned = re.sub(r"\s+", " ", cleaned).strip(" .;,:-")
    if not cleaned:
        return ""
    if len(cleaned) > 120:
        cleaned = re.split(r"[.;:](?:\s+|$)", cleaned, maxsplit=1)[0].strip()
    return cleaned


def infer_markdown_table_metadata(units: list[SemanticUnit]) -> dict[str, str]:
    headers: list[str] = []
    row_labels: list[str] = []
    column_count = 0
    row_count = 0
    for unit in units:
        for table in parse_markdown_tables(unit.body):
            column_count = max(column_count, len(table["headers"]))
            row_count += len(table["rows"])
            headers.extend(table["headers"])
            row_labels.extend(row[0] for row in table["rows"] if row)

    normalized_headers = sorted({normalize_definition_label(value) for value in headers if value.strip()})
    normalized_row_labels = sorted({normalize_definition_label(value) for value in row_labels if value.strip()})
    return {
        "contains_markdown_table": "true" if row_count > 0 else "false",
        "table_headers": ", ".join(normalized_headers),
        "table_row_labels": ", ".join(normalized_row_labels),
        "table_column_count": str(column_count) if column_count > 0 else "",
        "table_row_count": str(row_count) if row_count > 0 else "",
    }


def infer_plan_limit_metadata(units: list[SemanticUnit]) -> dict[str, str]:
    lines = [
        line.rstrip()
        for unit in units
        for line in unit.body.splitlines()
        if line.strip()
    ]
    plan_lines = [line.strip() for line in lines if is_plan_limit_line(line)]
    has_plan_limits = bool(plan_lines)
    has_non_plan_content = any(not is_plan_limit_line(line) for line in lines)
    component_kind = "mixed" if has_plan_limits and has_non_plan_content else ("plan_limit_only" if has_plan_limits else "")
    return {
        "has_plan_limit_lines": "true" if has_plan_limits else "false",
        "plan_limit_line_count": str(len(plan_lines)),
        "plan_limit_component_kind": component_kind,
    }


def parse_markdown_tables(text: str) -> list[dict[str, list[list[str]] | list[str]]]:
    lines = [line.rstrip() for line in text.splitlines()]
    tables: list[dict[str, list[list[str]] | list[str]]] = []
    idx = 0
    while idx < len(lines):
        if not is_markdown_table_start(lines, idx):
            idx += 1
            continue
        header_cells = parse_markdown_table_cells(lines[idx])
        idx += 2
        rows: list[list[str]] = []
        while idx < len(lines) and is_markdown_table_row(lines[idx]):
            rows.append(parse_markdown_table_cells(lines[idx]))
            idx += 1
        if header_cells:
            tables.append({"headers": header_cells, "rows": rows})
    return tables


def parse_markdown_table_cells(line: str) -> list[str]:
    stripped = line.strip().strip("|")
    return [cell.strip() for cell in stripped.split("|")]


def infer_cost_share_metadata(units: list[SemanticUnit]) -> dict[str, str]:
    kind = infer_cost_share_kind_for_units(units)
    raw_text = unit_bundle_text(units)
    normalized = normalize_topic_text(raw_text)
    unit_types = {unit.anchor_map.get("unit", "").strip().lower() for unit in units}
    value_type = infer_cost_share_value_type(raw_text)
    value_dependencies = infer_cost_share_value_dependencies(normalized)

    return {
        "cost_share_kind": kind,
        "cost_share_evidence": infer_cost_share_evidence(unit_types, normalized, kind),
        "cost_share_has_numeric": "true" if value_type else "false",
        "cost_share_value_type": value_type,
        "cost_share_value_dependencies": ", ".join(value_dependencies),
        "cost_share_mentions_table": "true"
        if contains_topic(normalized, "table of benefits", "policy schedule", "承保表")
        else "false",
    }


def infer_cost_share_kind_for_units(units: list[SemanticUnit]) -> str:
    kinds = detect_cost_share_kinds(unit_bundle_text(units))
    if len(kinds) == 1:
        return next(iter(kinds))
    if len(kinds) > 1:
        return "mixed"
    return ""


def unit_bundle_text(units: list[SemanticUnit]) -> str:
    return "\n".join(
        [
            " ".join(unit.path)
            + "\n"
            + " ".join(unit.anchor_lines)
            + "\n"
            + unit.body
            for unit in units
        ]
    )


def detect_cost_share_kinds(text: str) -> set[str]:
    normalized = normalize_topic_text(text)
    kinds: set[str] = set()

    if contains_topic(
        normalized,
        "co-insurance",
        "coinsurance",
        "共同保險",
        "共同保险",
        "co-payment",
        "copayment",
        "co payment",
        "percentage of eligible expense",
        "percentage of any claims",
        "policyholder must contribute after paying the deductible",
        "must contribute after paying the deductible",
        "amount shall be borne by the insured",
        "索償金額的一個百分比",
    ):
        kinds.add("co_insurance")

    if contains_topic(normalized, "賠償比率", "reimbursement ratio", "reimbursement rate", "indemnity ratio", "claim ratio"):
        kinds.add("reimbursement_ratio")

    if contains_topic(
        normalized,
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
        "definition: excess",
        "定義：自付額",
        "定義：自負額",
        "excess amount",
        "(excess)",
    ):
        kinds.add("deductible")
    elif re.search(r"\bexcess of\s+(hk\$|港幣|\d)", normalized):
        kinds.add("deductible")
    elif extract_hkd_amount(text) is not None and contains_topic(normalized, "deductible", "自負額", "自负额", "自付額", "自付额"):
        kinds.add("deductible")

    return kinds


def infer_cost_share_evidence(unit_types: set[str], normalized_text: str, kind: str) -> str:
    if not kind and not contains_topic(normalized_text, "table of benefits", "policy schedule", "承保表"):
        return ""
    if "definition" in unit_types:
        return "definition"
    if "exclusion" in unit_types:
        return "exclusion"
    if "benefit" in unit_types or "waiting_period" in unit_types:
        return "benefit"
    if contains_topic(normalized_text, "table of benefits", "policy schedule", "承保表"):
        return "table_reference"
    if "general_condition" in unit_types:
        return "general_condition"
    return "reference"


def infer_cost_share_value_type(text: str) -> str:
    if extract_percentage(text) is not None:
        return "percentage"
    if extract_hkd_amount(text) is not None:
        return "hkd_amount"
    return ""


def infer_cost_share_value_dependencies(normalized_text: str) -> list[str]:
    dependencies: list[str] = []
    if contains_topic(normalized_text, "table of benefits", "保障項目表"):
        dependencies.append("table_of_benefits")
    if contains_topic(normalized_text, "policy schedule", "承保表", "保單承保表", "保单承保表"):
        dependencies.append("policy_schedule")
    return dependencies


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


def contains_mri_ct_topic(text: str) -> bool:
    return contains_topic(
        text,
        "mri",
        "ct scan",
        "ct-scan",
        "computed tomography",
        "核磁共振",
        "電腦斷層掃描",
        "电脑断层扫描",
    ) or contains_ascii_term(text, "ct")


def contains_topic(text: str, *needles: str) -> bool:
    return any(_contains_topic_needle(text, needle) for needle in needles)


def _contains_topic_needle(text: str, needle: str) -> bool:
    needle = needle.lower().strip()
    if not needle:
        return False
    if is_ascii_phrase(needle):
        return contains_ascii_phrase(text, needle)
    return needle in text


def contains_ascii_phrase(text: str, phrase: str) -> bool:
    pattern = r"(?<![a-z0-9])" + re.escape(phrase) + r"(?![a-z0-9])"
    return re.search(pattern, text) is not None


def contains_ascii_term(text: str, term: str) -> bool:
    pattern = r"(?<![a-z0-9])" + re.escape(term) + r"(?![a-z0-9])"
    return re.search(pattern, text) is not None


def is_ascii_phrase(value: str) -> bool:
    return all(ord(char) < 128 for char in value)
