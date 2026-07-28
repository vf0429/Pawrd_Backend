from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from llama_index.core.schema import TextNode

try:
    from .chunking import build_chunk_records, chunk_markdown_document
except ImportError:
    from chunking import build_chunk_records, chunk_markdown_document

try:
    from .runtime import (
        AnswerResult,
        build_deterministic_answer,
        chronic_condition_backfill_nodes,
        compute_corpus_fingerprint,
        collect_cost_share_facts,
        comparison_provider_backfill_nodes,
        detect_query_intent,
        extract_cost_share_fact,
        generic_limit_answer_score,
        generic_exclusion_backfill_nodes,
        intent_summary,
        lexical_backfill_nodes,
        rerank_score,
        select_answer_nodes,
        select_general_age_limit_answer_nodes,
        select_generic_limit_answer_nodes,
        select_cost_share_answer_nodes,
        write_index_metadata,
        index_exists,
    )
except ImportError:
    from runtime import (
        AnswerResult,
        build_deterministic_answer,
        compute_corpus_fingerprint,
        collect_cost_share_facts,
        comparison_provider_backfill_nodes,
        detect_query_intent,
        extract_cost_share_fact,
        generic_limit_answer_score,
        generic_exclusion_backfill_nodes,
        intent_summary,
        lexical_backfill_nodes,
        rerank_score,
        select_answer_nodes,
        select_general_age_limit_answer_nodes,
        select_generic_limit_answer_nodes,
        select_cost_share_answer_nodes,
        write_index_metadata,
        index_exists,
    )

try:
    from .config import PrototypeConfig
except ImportError:
    from config import PrototypeConfig

try:
    from .payloads import build_query_payload, build_source_payload
except ImportError:
    from payloads import build_query_payload, build_source_payload

try:
    from .request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )
except ImportError:
    from request_validation import (
        RequestValidationError,
        build_validation_error_payload,
        validate_language,
        validate_max_sources,
        validate_provider,
    )

try:
    from .serve import RAGHandler, build_capabilities_payload, read_index_metadata
except ImportError:
    from serve import RAGHandler, build_capabilities_payload, read_index_metadata


class SemanticChunkingTests(unittest.TestCase):
    def test_semantic_chunking_balances_nested_units(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            policy = Path(tmp) / "one_degree"
            policy.mkdir()
            path = policy / "sample_en.md"
            path.write_text(
                """---
schema_version: rag-policy-v1
provider: one_degree
product: pet_ceo_plan
policy_type: pet_insurance
language: en
region: hk
source_file: source.pdf
source_version_label: test
normalization_status: draft
---

# Policy

## Section A

### 1. Coverage

> Source: p1
> Clause: 1
> Unit: section

#### 1.1 Covered Conditions

> Source: p1
> Clause: 1.1
> Unit: coverage_definition

##### Injuries

> Source: p1
> Clause: 1.1
> Unit: benefit

This clause explains injury coverage in a fairly detailed way.

##### Illness

> Source: p1-p2
> Clause: 1.1
> Unit: benefit

This clause explains illness coverage in a fairly detailed way.

#### 1.2 Waiting Period

> Source: p2
> Clause: 1.2
> Unit: waiting_period

Illness has a 30 day waiting period. Cancer has a 180 day waiting period. Bodily injury is covered immediately after the policy starts.
""",
                encoding="utf-8",
            )

            chunks = build_chunk_records(Path(tmp), chunk_size=110, chunk_overlap=20)
            self.assertGreaterEqual(len(chunks), 2)
            self.assertTrue(any("##### Injuries" in chunk.text for chunk in chunks))
            self.assertTrue(any("#### 1.2 Waiting Period" in chunk.text for chunk in chunks))
            self.assertTrue(all(int(chunk.metadata["token_estimate"]) > 0 for chunk in chunks))

    def test_chunk_document_preserves_clause_metadata(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Waiting Periods

> Source: p3
> Clause: 2.1
> Unit: waiting_period

- Illness: 30 days
- Cancer: 180 days
""",
            metadata={
                "provider": "bluecross",
                "source_name": "sample.md",
                "source_path": "bluecross/sample.md",
                "language": "en",
            },
            chunk_size=120,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        chunk = chunks[0]
        self.assertEqual(chunk.metadata["clauses"], "2.1")
        self.assertEqual(chunk.metadata["unit_types"], "waiting_period")
        self.assertEqual(chunk.metadata["section_path"], "Policy > Benefits")
        self.assertIn("waiting_period", chunk.metadata["topic_tags"])

    def test_waiting_period_siblings_do_not_recombine_into_one_large_bundle(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Exclusions

### Waiting Periods

#### Standard Waiting Period

> Source: p3
> Clause: 2.1.1-2.1.3
> Unit: waiting_period

Cancer has a 180 day waiting period. Other illnesses have a 30 day waiting period.

#### MRI Waiting Period

> Source: p3
> Clause: 2.1.4
> Unit: waiting_period

MRI has a 180 day waiting period.

#### Renewal Waiting Period

> Source: p3
> Clause: 2.1.5
> Unit: waiting_period

Renewal has no waiting period.
""",
            metadata={
                "provider": "one_degree",
                "source_name": "waiting.md",
                "source_path": "one_degree/waiting.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 3)
        self.assertTrue(any(chunk.metadata["clauses"] == "2.1.1-2.1.3" for chunk in chunks))
        self.assertTrue(all(chunk.metadata["bundle_unit_count"] == "1" for chunk in chunks))

    def test_long_renewal_rule_splits_into_tighter_parts(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## General Conditions

### 17. Renewal

> Source: p6
> Clause: 17
> Unit: renewal_rule

At the expiry of this Policy, subject to the right of the Company to terminate this Policy as provided herein, this Policy shall be automatically renewed for another Period of Insurance subject to the successful collection of premium at such rate or on such terms as the Company may determine depending on the benefits and the scope of coverage at the time of each Renewal.

Renewal of this Policy is guaranteed up to age 13 of the Insured Pet. Any Renewal above age 13 is subject to underwriting approval.

The Company reserves the right to revise the benefits, premiums, terms and conditions, and to make changes to this Policy upon Renewal.

If the Company decides to cease offering or suspend this plan, the Company will endeavour to transfer the Insured Pet to another available insurance plan for pet.

In the event that the Policyholder disagrees with the Renewal, he may give a written notice to the Company within 30 days from the Renewal Date of this Policy to cancel such Renewal.

The Policyholder will be entitled to a full refund of the premium paid for such Renewal, provided that:
- no claim has been made within such Cooling-off Period
- coupons issued to the Insured Pet for such Renewal, if any, are not being used within the Cooling-off Period and are returned to the Company

Exception:
- claims made within the Cooling-off Period seeking reimbursement of Eligible Expenses incurred before the termination of the Policy

The Company shall notify the Policyholder in writing no less than 30 days in advance of the Renewal Date effecting such revision specifying, among others, the revised Policy Schedule, the new premium and its effective date.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "renewal_long.md",
                "source_path": "bluecross/renewal_long.md",
                "language": "en",
            },
            chunk_size=700,
            chunk_overlap=120,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(all(chunk.metadata["clauses"] == "17" for chunk in chunks))
        self.assertTrue(any("> Part: 1/2" in chunk.text for chunk in chunks))
        self.assertLessEqual(max(chunk.token_estimate for chunk in chunks), 360)

    def test_short_definitions_still_bundle_to_avoid_tiny_chunks(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Accident

> Source: p5
> Clause: Definition
> Unit: definition

An unexpected accident.

### Definition: Illness

> Source: p5
> Clause: Definition
> Unit: definition

A disease or sickness.

### Definition: Vet

> Source: p5
> Clause: Definition
> Unit: definition

A licensed veterinarian.
""",
            metadata={
                "provider": "prudential",
                "source_name": "definitions.md",
                "source_path": "prudential/definitions.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        self.assertEqual(chunks[0].metadata["unit_types"], "definition")
        self.assertEqual(chunks[0].metadata["bundle_unit_count"], "3")

    def test_markdown_table_metadata_is_extracted_from_chunk(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Renewal

### No Claim Discount

> Source: p5
> Clause: NCD
> Unit: claim_rule

| No claim period immediately preceding Renewal | Discount Rate |
|---|---|
| 1 year | 5% |
| 2 consecutive years | 10% |
| 3 consecutive years | 15% |
""",
            metadata={
                "provider": "bluecross",
                "source_name": "ncd.md",
                "source_path": "bluecross/ncd.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        chunk = chunks[0]
        self.assertEqual(chunk.metadata["contains_markdown_table"], "true")
        self.assertEqual(chunk.metadata["table_headers"], "discount rate, no claim period immediately preceding renewal")
        self.assertEqual(chunk.metadata["table_row_labels"], "1 year, 2 consecutive years, 3 consecutive years")
        self.assertEqual(chunk.metadata["table_column_count"], "2")
        self.assertEqual(chunk.metadata["table_row_count"], "3")
        self.assertIn("markdown_table", chunk.metadata["topic_tags"])

    def test_large_markdown_table_splits_by_rows_and_repeats_header(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Renewal

### Discount Matrix

> Source: p6
> Clause: Discount Matrix
> Unit: claim_rule

The applicable discount schedule is shown below.

| No claim period | Discount Rate |
|---|---|
| 1 year | 5% |
| 2 years | 10% |
| 3 years | 15% |
| 4 years | 20% |
| 5 years | 25% |
| 6 years | 30% |
| 7 years | 35% |
| 8 years | 40% |
| 9 consecutive policy years without claims | 45% |
| 10 consecutive policy years without claims | 50% |
| 11 consecutive policy years without claims | 55% |
| 12 consecutive policy years without claims | 60% |
| 13 consecutive policy years without claims | 65% |
| 14 consecutive policy years without claims | 70% |
| 15 consecutive policy years without claims | 75% |
| 16 consecutive policy years without claims | 80% |
""",
            metadata={
                "provider": "bluecross",
                "source_name": "discount_matrix.md",
                "source_path": "bluecross/discount_matrix.md",
                "language": "en",
            },
            chunk_size=140,
            chunk_overlap=20,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(all("| No claim period | Discount Rate |" in chunk.text for chunk in chunks))
        self.assertTrue(any("| 1 year | 5% |" in chunk.text for chunk in chunks))
        self.assertTrue(any("| 16 consecutive policy years without claims | 80% |" in chunk.text for chunk in chunks))

    def test_exclusion_chunk_extracts_list_item_labels(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Exclusions

### Section 1 Exclusions

> Source: p2
> Clause: Section 1 Exclusions
> Unit: exclusion

The Company shall not be liable for any:
- pre-existing conditions
- costs of treatment related to:
  - dentistry
  - organ transplantation
""",
            metadata={
                "provider": "bluecross",
                "source_name": "exclusions.md",
                "source_path": "bluecross/exclusions.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        labels = set(part.strip() for part in chunks[0].metadata["list_item_labels"].split(",") if part.strip())
        self.assertIn("pre-existing conditions", labels)
        self.assertIn("costs of treatment related to", labels)
        self.assertIn("dentistry", labels)
        self.assertIn("organ transplantation", labels)
        self.assertEqual(chunks[0].metadata["list_item_count"], "4")

    def test_benefit_chunk_extracts_benefit_labels(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 1 Medical Coverage

#### B. Room and Board

> Source: p2
> Clause: 1.B
> Unit: benefit

We shall cover the cost of boarding Your Pet at a licensed veterinary clinic or hospital.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "benefit.md",
                "source_path": "bluecross/benefit.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        labels = set(part.strip() for part in chunks[0].metadata["benefit_labels"].split(",") if part.strip())
        self.assertIn("room and board", labels)
        self.assertIn("hospitalisation", labels)
        self.assertIn("hospitalization", labels)

    def test_benefit_table_chunk_extracts_nested_benefit_labels(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Table of Benefits

### Benefit Matrix Summary

> Source: p6
> Clause: Table of Benefits
> Unit: benefit_table

The Table of Benefits contains plan-specific maximum limits for:

- Section I Surgical Benefit, including Clinical and Surgical Benefit, Room and Board Expenses, and Post Surgical Treatment Benefit;
- Section II Chemotherapy;
""",
            metadata={
                "provider": "msig",
                "source_name": "benefit_table.md",
                "source_path": "msig/benefit_table.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        labels = set(part.strip() for part in chunks[0].metadata["benefit_labels"].split(",") if part.strip())
        self.assertIn("room and board expenses", labels)
        self.assertIn("room and board", labels)
        self.assertIn("hospitalisation", labels)
        self.assertIn("chemotherapy", labels)

    def test_select_answer_nodes_prefers_markdown_table_for_discount_query(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the Blue Cross discount rate after 3 consecutive years without claims?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### No Claim Discount\n\n> Source: p4\n> Clause: No Claim Discount\n> Unit: renewal_rule\n\n"
                        "Provided that no benefit has been paid or is payable under this Policy during the respective no claim period, the corresponding discount rate shall be:\n\n"
                        "| No claim period immediately preceding Renewal | Discount Rate |\n"
                        "|---|---|\n"
                        "| 1 year | 5% |\n"
                        "| 2 consecutive years | 10% |\n"
                        "| 3 consecutive years | 15% |\n"
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross_lovepet_en_pcp-05-2024.md",
                        "source_path": "bluecross/ncd.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "No Claim Discount",
                        "section_path": "LovePet Insurance Policy > Renewal",
                        "topic_tags": "renewal, markdown_table",
                        "contains_markdown_table": "true",
                        "table_headers": "no claim period immediately preceding renewal, discount rate",
                        "table_row_labels": "1 year, 2 consecutive years, 3 consecutive years",
                        "table_column_count": "2",
                        "table_row_count": "3",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Company\nBlue Cross (Asia-Pacific) Insurance Limited.",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross_lovepet_en_pcp-05-2024.md",
                        "source_path": "bluecross/company.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "4",
                        "section_path": "LovePet Insurance Policy > Definitions",
                        "topic_tags": "definition",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["bluecross/ncd.md"])

    def test_build_deterministic_answer_extracts_markdown_table_discount_rate(self) -> None:
        intent = detect_query_intent("What is the Blue Cross discount rate after 3 consecutive years without claims?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### No Claim Discount\n\n> Source: p4\n> Clause: No Claim Discount\n> Unit: renewal_rule\n\n"
                        "Provided that no benefit has been paid or is payable under this Policy during the respective no claim period, the corresponding discount rate shall be:\n\n"
                        "| No claim period immediately preceding Renewal | Discount Rate |\n"
                        "|---|---|\n"
                        "| 1 year | 5% |\n"
                        "| 2 consecutive years | 10% |\n"
                        "| 3 consecutive years | 15% |\n"
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross_lovepet_en_pcp-05-2024.md",
                        "source_path": "bluecross/ncd.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "No Claim Discount",
                        "section_path": "LovePet Insurance Policy > Renewal",
                        "topic_tags": "renewal, markdown_table",
                        "contains_markdown_table": "true",
                        "table_headers": "no claim period immediately preceding renewal, discount rate",
                        "table_row_labels": "1 year, 2 consecutive years, 3 consecutive years",
                        "table_column_count": "2",
                        "table_row_count": "3",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        self.assertIn("15%", result[0])
        self.assertIn("Discount Rate", result[0])

    def test_build_deterministic_answer_extracts_multiple_markdown_table_rows(self) -> None:
        intent = detect_query_intent("What are the Blue Cross discount rates after 1 year and 3 consecutive years without claims?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### No Claim Discount\n\n> Source: p4\n> Clause: No Claim Discount\n> Unit: renewal_rule\n\n"
                        "| No claim period immediately preceding Renewal | Discount Rate |\n"
                        "|---|---|\n"
                        "| 1 year | 5% |\n"
                        "| 2 consecutive years | 10% |\n"
                        "| 3 consecutive years | 15% |\n"
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross_lovepet_en_pcp-05-2024.md",
                        "source_path": "bluecross/ncd.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "No Claim Discount",
                        "section_path": "LovePet Insurance Policy > Renewal",
                        "topic_tags": "renewal, markdown_table",
                        "contains_markdown_table": "true",
                        "table_headers": "no claim period immediately preceding renewal, discount rate",
                        "table_row_labels": "1 year, 2 consecutive years, 3 consecutive years",
                        "table_column_count": "2",
                        "table_row_count": "3",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        self.assertIn("5%", result[0])
        self.assertIn("15%", result[0])
        self.assertEqual(result[1]["type"], "markdown_table_multi")

    def test_build_deterministic_answer_summarizes_markdown_table_overview(self) -> None:
        intent = detect_query_intent("What are the Blue Cross no claim discount rates?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### No Claim Discount\n\n> Source: p4\n> Clause: No Claim Discount\n> Unit: renewal_rule\n\n"
                        "| No claim period immediately preceding Renewal | Discount Rate |\n"
                        "|---|---|\n"
                        "| 1 year | 5% |\n"
                        "| 2 consecutive years | 10% |\n"
                        "| 3 consecutive years | 15% |\n"
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross_lovepet_en_pcp-05-2024.md",
                        "source_path": "bluecross/ncd.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "No Claim Discount",
                        "section_path": "LovePet Insurance Policy > Renewal",
                        "topic_tags": "renewal, markdown_table",
                        "contains_markdown_table": "true",
                        "table_headers": "no claim period immediately preceding renewal, discount rate",
                        "table_row_labels": "1 year, 2 consecutive years, 3 consecutive years",
                        "table_column_count": "2",
                        "table_row_count": "3",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        self.assertIn("5%", result[0])
        self.assertIn("10%", result[0])
        self.assertIn("15%", result[0])
        self.assertEqual(result[1]["type"], "markdown_table_overview")

    def test_rerank_score_prefers_markdown_table_node_for_english_discount_overview_query(self) -> None:
        intent = detect_query_intent("What are the Blue Cross no claim discount rates?")
        table_node = SimpleNamespace(
            score=0.0,
            node=SimpleNamespace(
                text=(
                    "### No Claim Discount\n\n"
                    "| No claim period immediately preceding Renewal | Discount Rate |\n"
                    "|---|---|\n"
                    "| 1 year | 5% |\n"
                    "| 2 consecutive years | 10% |\n"
                    "| 3 consecutive years | 15% |\n"
                ),
                metadata={
                    "provider": "bluecross",
                    "source_path": "bluecross/ncd.md",
                    "chunk_index": "1",
                    "unit_types": "renewal_rule",
                    "clauses": "No Claim Discount",
                    "section_path": "LovePet Insurance Policy > No Claim Discount",
                    "topic_tags": "renewal, markdown_table",
                    "contains_markdown_table": "true",
                    "table_headers": "discount rate, no claim period immediately preceding renewal",
                    "table_row_labels": "1 year, 2 consecutive years, 3 consecutive years",
                },
            ),
        )
        exclusion_node = SimpleNamespace(
            score=0.0,
            node=SimpleNamespace(
                text="#### Exclusions Applicable to Section 2\nThe Company shall not be liable for the first HK$3,000 of each and every claim.",
                metadata={
                    "provider": "bluecross",
                    "source_path": "bluecross/exclusions.md",
                    "chunk_index": "2",
                    "unit_types": "exclusion",
                    "clauses": "Section 2 Exclusions",
                    "section_path": "LovePet Insurance Policy > Benefit Provisions > Section 2 Third Party Liability",
                    "topic_tags": "exclusion",
                    "contains_markdown_table": "false",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, table_node), rerank_score(intent, exclusion_node))

    def test_general_condition_age_eligibility_isolated_from_neighboring_conditions(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## General Conditions

### Condition Precedent

> Source: p3
> Clause: General Conditions.1
> Unit: general_condition

The due observance and fulfilment of the terms and conditions are conditions precedent to liability.

### Pet Eligibility

> Source: p3
> Clause: General Conditions.2
> Unit: general_condition

- be micro-chipped;
- be at least sixteen (16) weeks old and below nine (9) years old at the Commencement Date;
- complete all required Vaccinations; and
- not be a Working Pet.

### Ownership

> Source: p3
> Clause: General Conditions.3
> Unit: general_condition

The policyholder must remain the owner of the pet.
""",
            metadata={
                "provider": "msig",
                "source_name": "msig.md",
                "source_path": "MSIG/msig.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 3)
        pet_eligibility = next(chunk for chunk in chunks if chunk.metadata["clauses"] == "General Conditions.2")
        self.assertEqual(pet_eligibility.metadata["bundle_unit_count"], "1")
        self.assertIn("eligibility", pet_eligibility.metadata["topic_tags"])
        self.assertIn("age_limit", pet_eligibility.metadata["topic_tags"])

    def test_definition_coinsurance_isolated_from_neighboring_definitions(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Accident / Accidental

> Source: p1
> Clause: 1
> Unit: definition

An unforeseen event.

### Definition: Co-Insurance

> Source: p1
> Clause: 2
> Unit: definition

A percentage of Eligible Expense that the Policyholder must contribute after paying the deductible (if any) in a Policy Year.

### Definition: Company

> Source: p1
> Clause: 3
> Unit: definition

The insurer.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "bluecross.md",
                "source_path": "bluecross/definitions.md",
                "language": "en",
            },
            chunk_size=240,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 3)
        coinsurance = next(chunk for chunk in chunks if chunk.metadata["clauses"] == "2")
        self.assertEqual(coinsurance.metadata["bundle_unit_count"], "1")
        self.assertIn("co-insurance", coinsurance.text.lower())
        self.assertEqual(coinsurance.metadata["cost_share_kind"], "co_insurance")
        self.assertEqual(coinsurance.metadata["cost_share_evidence"], "definition")
        self.assertEqual(coinsurance.metadata["cost_share_has_numeric"], "false")
        self.assertEqual(coinsurance.metadata["cost_share_value_type"], "")
        self.assertEqual(coinsurance.metadata["cost_share_value_dependencies"], "")
        self.assertEqual(coinsurance.metadata["cost_share_mentions_table"], "false")
        self.assertEqual(coinsurance.metadata["definition_labels"], "co-insurance")

    def test_definition_copayment_isolated_from_neighboring_definitions(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Company

> Source: p2
> Clause: 1
> Unit: definition

The insurer.

### Definition: Co-payment

> Source: p2
> Clause: 2
> Unit: definition

The percentage of eligible expense that remains payable by the policyholder for each covered claim.

### Definition: Veterinary Surgeon

> Source: p2
> Clause: 3
> Unit: definition

A registered veterinary practitioner.
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential_defs.md",
                "source_path": "prudential/definitions.md",
                "language": "en",
            },
            chunk_size=240,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 3)
        copayment = next(chunk for chunk in chunks if chunk.metadata["clauses"] == "2")
        self.assertEqual(copayment.metadata["bundle_unit_count"], "1")
        self.assertEqual(copayment.metadata["definition_labels"], "co-payment")

    def test_definition_self_pay_label_isolated_from_neighboring_definitions(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## 定義

### 定義：保障期

> Source: p4
> Clause: 4
> Unit: definition

保障的生效期間。

### 定義：自付額

> Source: p4
> Clause: 5
> Unit: definition

受保人於每次索償中需要自行承擔的固定金額。

### 定義：獸醫

> Source: p4
> Clause: 6
> Unit: definition

依法註冊的獸醫。
""",
            metadata={
                "provider": "bluecross",
                "source_name": "bluecross_zh_defs.md",
                "source_path": "bluecross/definitions_zh.md",
                "language": "zh",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 3)
        deductible = next(chunk for chunk in chunks if chunk.metadata["clauses"] == "5")
        self.assertEqual(deductible.metadata["bundle_unit_count"], "1")
        self.assertEqual(deductible.metadata["definition_labels"], "自付額")

    def test_long_bulleted_definition_isolated_and_keeps_clean_labels(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Accident / Accidental Injury

> Source: p8
> Clause: 8
> Unit: definition

An unexpected event that:
- is sudden and external
- causes bodily injury to the insured pet
- requires treatment by a veterinarian

### Definition: Policyholder

> Source: p8
> Clause: 9
> Unit: definition

The owner named in the schedule.
""",
            metadata={
                "provider": "one_degree",
                "source_name": "one_degree_defs.md",
                "source_path": "one_degree/definitions.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 2)
        accident = next(chunk for chunk in chunks if chunk.metadata["clauses"] == "8")
        self.assertEqual(accident.metadata["bundle_unit_count"], "1")
        self.assertEqual(accident.metadata["definition_labels"], "accident, accidental injury")

    def test_cost_share_metadata_detects_bluecross_deductible_clause(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 2 Exclusions

> Source: p3
> Clause: Section 2 Exclusions
> Unit: exclusion

- the first HK$3,000 of each and every claim (Excess)
""",
            metadata={
                "provider": "bluecross",
                "source_name": "bluecross_deductible.md",
                "source_path": "bluecross/deductible.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        chunk = chunks[0]
        self.assertEqual(chunk.metadata["cost_share_kind"], "deductible")
        self.assertEqual(chunk.metadata["cost_share_evidence"], "exclusion")
        self.assertEqual(chunk.metadata["cost_share_has_numeric"], "true")
        self.assertEqual(chunk.metadata["cost_share_value_type"], "hkd_amount")

    def test_cost_share_metadata_tracks_policy_schedule_dependency_for_reimbursement_rate(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Reimbursement Rate

> Source: p9
> Clause: 9
> Unit: definition

The reimbursement rate specified in the Policy Schedule that applies to eligible medical expenses.
""",
            metadata={
                "provider": "one_degree",
                "source_name": "one_degree_defs.md",
                "source_path": "one_degree/definitions.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        chunk = chunks[0]
        self.assertEqual(chunk.metadata["cost_share_kind"], "reimbursement_ratio")
        self.assertEqual(chunk.metadata["cost_share_value_dependencies"], "policy_schedule")
        self.assertIn("policy_schedule", chunk.metadata["topic_tags"])
        self.assertEqual(chunk.metadata["definition_labels"], "reimbursement rate")

    def test_cost_share_metadata_does_not_treat_excess_of_limit_as_deductible(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 1 – "PAWcare" Medical Expenses

#### D. Chemotherapy and Heart Diseases Treatment Benefit

> Source: p1
> Clause: 1.D
> Unit: benefit

The Company will indemnify the Insured against the incurred additional cost under this Section 1-D Chemotherapy and Heart Diseases Treatment Benefit in excess of the maximum limit per visit under Section 1-A.vi.

Plan A: HK$5,000 per year (HK$2,500 per visit).
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential_1d.md",
                "source_path": "prudential/1d.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertGreaterEqual(len(chunks), 1)
        matched = [
            chunk
            for chunk in chunks
            if "in excess of the maximum limit per visit" in chunk.text
            or "Plan A: HK$5,000 per year" in chunk.text
        ]
        self.assertTrue(matched)
        self.assertTrue(all(chunk.metadata["cost_share_kind"] == "" for chunk in matched))
        self.assertTrue(all(chunk.metadata["cost_share_evidence"] == "" for chunk in matched))

    def test_benefit_chunking_isolates_plan_limit_lines_from_coverage_prose(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 1

#### B. Room and Board

> Source: p3
> Clause: 1.B
> Unit: benefit

The Company will cover room and board expenses necessarily incurred for hospital confinement due to Illness or Bodily Injury.

Plan A: HK$3,500 per year (HK$250 per day).
Plan B: HK$7,000 per year (HK$500 per day).
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential_1b.md",
                "source_path": "prudential/1b.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 2)
        coverage_chunks = [chunk for chunk in chunks if "room and board expenses necessarily incurred" in chunk.text]
        plan_chunks = [chunk for chunk in chunks if "Plan A: HK$3,500 per year" in chunk.text]

        self.assertEqual(len(coverage_chunks), 1)
        self.assertEqual(len(plan_chunks), 1)
        self.assertNotIn("Plan A: HK$3,500 per year", coverage_chunks[0].text)
        self.assertEqual(coverage_chunks[0].metadata["has_plan_limit_lines"], "false")
        self.assertEqual(coverage_chunks[0].metadata["plan_limit_component_kind"], "")

        self.assertNotIn("room and board expenses necessarily incurred", plan_chunks[0].text)
        self.assertEqual(plan_chunks[0].metadata["has_plan_limit_lines"], "true")
        self.assertEqual(plan_chunks[0].metadata["plan_limit_line_count"], "2")
        self.assertEqual(plan_chunks[0].metadata["plan_limit_component_kind"], "plan_limit_only")

    def test_benefit_chunking_keeps_coverage_only_clause_as_single_chunk(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 1

#### C. Consultation Benefit

> Source: p2
> Clause: 1.C
> Unit: benefit

The Company will cover consultation fees necessarily incurred for veterinary outpatient visits arising from Illness or Bodily Injury.

- Follow-up consultation is included when medically necessary.
- Referral consultation is included when arranged by the attending Vet.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "bluecross_1c.md",
                "source_path": "bluecross/1c.md",
                "language": "en",
            },
            chunk_size=260,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        chunk = chunks[0]
        self.assertIn("consultation fees necessarily incurred", chunk.text)
        self.assertEqual(chunk.metadata["has_plan_limit_lines"], "false")
        self.assertEqual(chunk.metadata["plan_limit_line_count"], "0")
        self.assertEqual(chunk.metadata["plan_limit_component_kind"], "")

    def test_long_numbered_list_splits_into_semantic_items(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Exceptions

### Section 2 Exclusions

> Source: p2
> Clause: Section 2 Exclusions
> Unit: exclusion

The Company will not be liable for:
1. The first HK$3,000 of each and every claim
2. Liability in respect of Bodily Injury and disease to the Insured and any person under a contract of service with the Insured or the Insured's Family arising out of and in the course of such person's employment
3. Liability in respect of loss of or damage to property belonging to or in the custody or control of the Insured or the Insured's Family or any person under a contract of service
4. Liability arising directly or indirectly from:
   - (a) any wilful or malicious act or criminal activity
   - (b) the pursuit by the Insured or the Insured's Family of any trade business profession or employment
   - (c) any agreement and such liability would not have attached in the absence of such agreement
5. Any loss, damage, liability, expense, fines, penalties or any other amount directly or indirectly caused by Coronavirus (COVID-19)
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential.md",
                "source_path": "prudential/exclusions.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(any("1. The first HK$3,000" in chunk.text for chunk in chunks))
        self.assertTrue(any("4. Liability arising directly or indirectly from:" in chunk.text for chunk in chunks))
        self.assertTrue(any("- (a) any wilful or malicious act" in chunk.text for chunk in chunks))
        self.assertTrue(any("- (c) any agreement" in chunk.text for chunk in chunks))
        self.assertTrue(all("The Company will not be liable for:" in chunk.text for chunk in chunks))
        self.assertTrue(any("5. Any loss, damage, liability" in chunk.text for chunk in chunks))

    def test_nested_bullets_stay_with_parent_list_item(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Coverage

### Section 1 Benefits

> Source: p6
> Clause: 1.A
> Unit: benefit

The Company shall pay the following expenses:
- (i) Surgery fee
- (ii) Operating theatre fee
- (iii) Anesthetist fee
- (iv) Euthanasia fee
- (v) Miscellaneous fee
- (vi) X-ray, ultrasound and laboratory fees
  - CT Scan
  - MRI Scan
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential.md",
                "source_path": "prudential/benefits.md",
                "language": "en",
            },
            chunk_size=160,
            chunk_overlap=20,
        )

        self.assertGreaterEqual(len(chunks), 1)
        vi_chunks = [chunk.text for chunk in chunks if "- (vi) X-ray, ultrasound and laboratory fees" in chunk.text]
        self.assertTrue(vi_chunks)
        self.assertTrue(any("CT Scan" in chunk and "MRI Scan" in chunk for chunk in vi_chunks))

    def test_nested_exclusion_sub_bullets_split_when_parent_item_is_oversized(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Exceptions

### Section 2 Exclusions

> Source: p2
> Clause: Section 2 Exclusions
> Unit: exclusion

The Company will not be liable for:
4. Liability arising directly or indirectly from:
   - (a) any wilful or malicious act or criminal activity committed by the insured or family members
   - (b) the pursuit by the insured or the insured's family of any trade, business, profession or employment of any kind
   - (c) any agreement and such liability would not have attached in the absence of such agreement
   - (d) the transmission of any communicable disease, virus, contamination or similar biological hazard by the insured or family members
   - (e) pollution, seepage, discharge, dispersal or release of any irritating, contaminating or toxic substance into the environment
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential_nested_exclusions.md",
                "source_path": "prudential/nested_exclusions.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(any("- (a) any wilful or malicious act" in chunk.text for chunk in chunks))
        self.assertTrue(any("- (e) pollution, seepage" in chunk.text for chunk in chunks))
        self.assertTrue(all("4. Liability arising directly or indirectly from:" in chunk.text for chunk in chunks))
        self.assertLessEqual(max(chunk.token_estimate for chunk in chunks), 260)

    def test_waiting_period_with_multiple_rules_splits_into_tighter_parts(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Benefits

### Section 1 – "PAWcare" Medical Expenses

#### Waiting Periods (Section 1)

> Source: p1
> Clause: Waiting Periods
> Unit: waiting_period

- Illness (other than cancer): benefit payable provided that the Illness first occurs after the expiry of the Waiting Period of 30 days from the effective date of this Policy.
- Cancer: benefit payable provided that the cancer first occurs after the expiry of the Waiting Period of 180 days from the effective date of this Policy.
- Bodily Injury: benefit payable provided that the relevant Bodily Injury first occurs after the expiry of the Waiting Period of 7 days from the effective date of this Policy.
- Waiting Period Waiver: if the same Insured Furry Kid has continuously been insured with medical coverage similar to this Policy by another insurer in Hong Kong for at least one year immediately before the effective date of this Policy.
- Co-payment: All claims under Section 1 – "PAWcare" Medical Expenses is subject to a Co-payment of 30%.
""",
            metadata={
                "provider": "prudential",
                "source_name": "prudential_waiting.md",
                "source_path": "prudential/waiting.md",
                "language": "en",
            },
            chunk_size=700,
            chunk_overlap=120,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(all(chunk.metadata["clauses"] == "Waiting Periods" for chunk in chunks))
        self.assertLessEqual(max(chunk.token_estimate for chunk in chunks), 260)

    def test_policy_intro_long_clause_splits_into_tighter_parts(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Insuring Clause

> Source: p1
> Unit: policy_intro

The Policyholder and the Company agree that:
- this Policy and any endorsement attached to this Policy shall be read together as one contract;
- the terms, conditions and exclusions contained in the Policy Schedule shall be read in accordance with and shall not be construed so as to modify, add to or in any way vary the terms, conditions and exclusions contained herein;
- the application, proposal and declaration together with any information supplied shall form the basis of this contract and be deemed incorporated herein;
- the Company shall provide indemnity subject to all limits, conditions, exclusions and compliance requirements stated in the Policy;
- the Insured and any claimant shall fully observe, satisfy and perform all warranties, obligations, declarations and conditions precedent before any liability can attach under this Policy;
- any statement, declaration or information supplied for underwriting or renewal purposes shall remain material to the Company’s decision to continue, vary, suspend or cancel cover under this Policy.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "bluecross_policy_intro.md",
                "source_path": "bluecross/policy_intro.md",
                "language": "en",
            },
            chunk_size=700,
            chunk_overlap=120,
        )

        self.assertGreaterEqual(len(chunks), 2)
        self.assertTrue(all(chunk.metadata["unit_types"] == "policy_intro" for chunk in chunks))
        self.assertLessEqual(max(chunk.token_estimate for chunk in chunks), 240)

    def test_topic_tags_detect_standard_vs_addon_waiting_periods(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Exclusions

### Waiting Periods

#### Standard Waiting Period

> Source: p3
> Clause: 2.1.1-2.1.3
> Unit: waiting_period

Cancer has a 180 day waiting period. Other illnesses have a 30 day waiting period.

#### Critical Illness Cash Benefit Waiting Period

> Source: p3
> Clause: 2.1.7
> Unit: waiting_period

This optional critical illness add-on has a 180 day waiting period.
""",
            metadata={
                "provider": "one_degree",
                "source_name": "tags.md",
                "source_path": "one_degree/tags.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        by_clause = {chunk.metadata["clauses"]: chunk for chunk in chunks}
        self.assertIn("standard_waiting_period", by_clause["2.1.1-2.1.3"].metadata["topic_tags"])
        self.assertIn("cancer", by_clause["2.1.1-2.1.3"].metadata["topic_tags"])
        self.assertIn("addon_waiting_period", by_clause["2.1.7"].metadata["topic_tags"])

    def test_waiting_period_definition_also_gets_waiting_period_topic_tags(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Waiting Period

> Source: p3
> Clause: 27
> Unit: definition

Waiting period means:
- cancer claims: 90 days
- injury claims: 7 days
- other illnesses: 30 days
""",
            metadata={
                "provider": "bluecross",
                "source_name": "definition_waiting.md",
                "source_path": "bluecross/definition_waiting.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        tags = chunks[0].metadata["topic_tags"]
        self.assertIn("definition", tags)
        self.assertIn("waiting_period", tags)
        self.assertIn("standard_waiting_period", tags)
        self.assertNotIn("mri_ct", tags)

    def test_waiting_period_definition_does_not_match_ct_inside_respect(self) -> None:
        chunks = chunk_markdown_document(
            body="""
# Policy

## Definitions

### Definition: Waiting Period

> Source: p3
> Clause: 27
> Unit: definition

A period of:
- in respect of any claim arising from cancer, 90 days;
- in respect of any claim arising from an injury, 7 days;
- in respect of any claim arising from other illnesses, 30 days.
""",
            metadata={
                "provider": "bluecross",
                "source_name": "definition_respect.md",
                "source_path": "bluecross/definition_respect.md",
                "language": "en",
            },
            chunk_size=220,
            chunk_overlap=20,
        )

        self.assertEqual(len(chunks), 1)
        tags = chunks[0].metadata["topic_tags"]
        self.assertIn("waiting_period", tags)
        self.assertIn("standard_waiting_period", tags)
        self.assertNotIn("mri_ct", tags)

    def test_waiting_period_rerank_prefers_direct_clause_over_add_on(self) -> None:
        intent = detect_query_intent("OneDegree 的癌症等候期是几多日？")
        standard = SimpleNamespace(
            score=0.45,
            node=SimpleNamespace(
                text="標準等候期 ... 癌症（惡性腫瘤）的等候期為 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.1-2.1.3",
                },
            ),
        )
        addon = SimpleNamespace(
            score=0.62,
            node=SimpleNamespace(
                text="高級危疾現金保障等候期 ... 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Critical Illness Cash Benefit",
                    "clauses": "2.1.9",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, standard), rerank_score(intent, addon))

    def test_waiting_period_rerank_penalizes_mri_clause_for_cancer_query(self) -> None:
        intent = detect_query_intent("OneDegree 的癌症等候期是几多日？")
        standard = SimpleNamespace(
            score=0.40,
            node=SimpleNamespace(
                text="標準等候期 ... 癌症（惡性腫瘤）的等候期為 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.1-2.1.3",
                },
            ),
        )
        mri = SimpleNamespace(
            score=0.62,
            node=SimpleNamespace(
                text="MRI/CT 等候期為 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.4",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, standard), rerank_score(intent, mri))

    def test_waiting_period_rerank_penalizes_renewal_clause_for_generic_query(self) -> None:
        intent = detect_query_intent("OneDegree 的癌症等候期是几多日？")
        standard = SimpleNamespace(
            score=0.30,
            node=SimpleNamespace(
                text="標準等候期 ... 癌症（惡性腫瘤）的等候期為 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.1-2.1.3",
                },
            ),
        )
        renewal = SimpleNamespace(
            score=0.55,
            node=SimpleNamespace(
                text="若為本保單同一計劃或年度保障總額較低的計劃續保，則不設等候期。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.5",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, standard), rerank_score(intent, renewal))

    def test_waiting_period_rerank_penalizes_pre_existing_clause_for_generic_query(self) -> None:
        intent = detect_query_intent("OneDegree 的癌症等候期是几多日？")
        standard = SimpleNamespace(
            score=0.30,
            node=SimpleNamespace(
                text="標準等候期 ... 癌症（惡性腫瘤）的等候期為 180 天。",
                metadata={
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Benefits > Waiting Periods",
                    "clauses": "2.1.1-2.1.3",
                },
            ),
        )
        pre_existing = SimpleNamespace(
            score=0.58,
            node=SimpleNamespace(
                text="若你的寵物於保單等候期完結前 ... 將視為投保前已存在病況。",
                metadata={
                    "unit_types": "exclusion",
                    "section_path": "Policy > Exclusions",
                    "clauses": "2.2",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, standard), rerank_score(intent, pre_existing))

    def test_rerank_score_prefers_matching_exclusion_list_item_labels(self) -> None:
        intent = detect_query_intent("Is organ transplant excluded in Blue Cross?")
        matching = SimpleNamespace(
            score=0.45,
            node=SimpleNamespace(
                text="The Company shall not be liable for costs of treatment related to certain excluded procedures.",
                metadata={
                    "unit_types": "exclusion",
                    "section_path": "Policy > Exclusions",
                    "clauses": "Section 1 Exclusions",
                    "list_item_labels": "pre-existing conditions, dentistry, organ transplantation",
                    "list_item_count": "3",
                },
            ),
        )
        non_matching = SimpleNamespace(
            score=0.45,
            node=SimpleNamespace(
                text="The Company shall not be liable for various excluded items.",
                metadata={
                    "unit_types": "exclusion",
                    "section_path": "Policy > Exclusions",
                    "clauses": "Section 1 Exclusions",
                    "list_item_labels": "administrative fees, waiting period, cremation",
                    "list_item_count": "3",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, matching), rerank_score(intent, non_matching))

    def test_select_answer_nodes_prefers_matching_exclusion_for_coverage_query(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 牙科治療包唔包？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將不會負責任何與下列治療有關之費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/exclusions.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 1 Exclusions",
                        "list_item_labels": "已存在之狀況, 牙科, 器官移植",
                        "list_item_count": "3",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將支付受保寵物於受保期內因疾病或受傷在獸醫診所內招致任何下列之支出。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/benefit.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.A",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["bluecross/exclusions.md"])

    def test_build_deterministic_answer_formats_generic_exclusion_answer(self) -> None:
        intent = detect_query_intent("Blue Cross 牙科治療包唔包？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將不會負責任何與下列治療有關之費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/exclusions.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 1 Exclusions",
                        "list_item_labels": "已存在之狀況, 牙科, 器官移植",
                        "list_item_count": "3",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("明確將「牙科」列為不保事項", answer)
        self.assertEqual(structured["type"], "generic_exclusion_single")
        self.assertTrue(structured["excluded"])
        self.assertEqual(structured["matched_label"], "牙科")

    def test_generic_exclusion_backfill_nodes_pulls_matching_exclusion_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 牙科治療包唔包？")
        docs = {
            "a": TextNode(
                metadata={
                    "provider": "bluecross",
                    "language": "zh",
                    "source_path": "bluecross/exclusions.md",
                    "chunk_index": "1",
                    "unit_types": "exclusion",
                    "clauses": "Section 1 Exclusions",
                    "list_item_labels": "已存在之狀況, 牙科, 器官移植",
                    "list_item_count": "3",
                },
                text="本公司將不會負責任何與下列治療有關之費用。",
            ),
            "b": TextNode(
                metadata={
                    "provider": "bluecross",
                    "language": "zh",
                    "source_path": "bluecross/benefit.md",
                    "chunk_index": "2",
                    "unit_types": "benefit",
                    "clauses": "1.A",
                },
                text="本公司將支付受保寵物於受保期內因疾病或受傷在獸醫診所內招致任何下列之支出。",
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        backfill = generic_exclusion_backfill_nodes(cfg, index, intent, provider="bluecross", language="zh")
        self.assertEqual([node.node.metadata["source_path"] for node in backfill], ["bluecross/exclusions.md"])

    def test_select_answer_nodes_prunes_weak_waiting_period_supporting_sources(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree 的癌症等候期是几多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="標準等候期 ... 癌症（惡性腫瘤）的等候期為 180 天。",
                    metadata={
                        "source_path": "one_degree/policy.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "section_path": "Policy > Waiting Periods",
                        "clauses": "2.1.1-2.1.3",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="若為本保單同一計劃續保，則不設等候期。",
                    metadata={
                        "source_path": "one_degree/policy.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "section_path": "Policy > Waiting Periods",
                        "clauses": "2.1.5",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="MRI/CT 等候期為 180 天。",
                    metadata={
                        "source_path": "one_degree/policy.md",
                        "chunk_index": "3",
                        "unit_types": "waiting_period",
                        "section_path": "Policy > Waiting Periods",
                        "clauses": "2.1.4",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(len(selected), 1)
        self.assertEqual(selected[0].node.metadata["clauses"], "2.1.1-2.1.3")

    def test_detect_query_intent_marks_pre_existing_and_exclusion(self) -> None:
        intent = detect_query_intent("Blue Cross 投保前已存在病況包唔包？")
        self.assertTrue(intent.wants_pre_existing)
        self.assertTrue(intent.wants_exclusion)
        self.assertTrue(intent.wants_coverage)

    def test_detect_query_intent_marks_consult_limit_query(self) -> None:
        intent = detect_query_intent("OneDegree 普通科診金最高賠償額係幾多？")
        self.assertTrue(intent.wants_consult)
        self.assertTrue(intent.asks_limit)

    def test_detect_query_intent_marks_renewal_query(self) -> None:
        intent = detect_query_intent("Can Prudential be renewed automatically?")
        self.assertTrue(intent.asks_renewal)

    def test_detect_query_intent_marks_cost_share_query(self) -> None:
        intent = detect_query_intent("Prudential 自負額係幾多？")
        self.assertTrue(intent.asks_cost_share)
        self.assertTrue(detect_query_intent("What is the co-payment?").asks_cost_share)
        self.assertTrue(detect_query_intent("Prudential Co-payment is how much?").asks_cost_share)

    def test_detect_query_intent_marks_cash_benefit_age_limit_query(self) -> None:
        intent = detect_query_intent("OneDegree 高級危疾現金保障有咩年齡限制？")
        self.assertTrue(intent.asks_cash_benefit)
        self.assertTrue(intent.asks_age_limit)

    def test_detect_query_intent_marks_fip_addon_queries(self) -> None:
        waiting_intent = detect_query_intent("OneDegree 貓傳染性腹膜炎額外保額等候期係幾多日？")
        self.assertTrue(waiting_intent.asks_addon_benefit)
        self.assertTrue(waiting_intent.wants_waiting_period)

        eligibility_intent = detect_query_intent("OneDegree 貓傳染性腹膜炎額外保額有咩受保條件？")
        self.assertTrue(eligibility_intent.asks_addon_benefit)
        self.assertTrue(eligibility_intent.asks_eligibility)

    def test_detect_query_intent_marks_chronic_condition_queries(self) -> None:
        intent = detect_query_intent("OneDegree 慢性病況 5 歲或以上仲包唔包？")
        self.assertTrue(intent.asks_chronic_condition)
        self.assertTrue(intent.wants_coverage)

    def test_pre_existing_query_prefers_pre_existing_clause(self) -> None:
        intent = detect_query_intent("Blue Cross 投保前已存在病況包唔包？")
        direct = SimpleNamespace(
            score=0.30,
            node=SimpleNamespace(
                text="投保前已存在病況將不獲賠償。",
                metadata={
                    "unit_types": "exclusion",
                    "section_path": "Policy > Exclusions",
                    "clauses": "2.2",
                    "topic_tags": "pre_existing, exclusion",
                },
            ),
        )
        generic = SimpleNamespace(
            score=0.60,
            node=SimpleNamespace(
                text="一般保障包括診症及手術費用。",
                metadata={
                    "unit_types": "benefit",
                    "section_path": "Policy > Benefits",
                    "clauses": "1.2",
                    "topic_tags": "benefit, illness",
                },
            ),
        )

        self.assertGreater(rerank_score(intent, direct), rerank_score(intent, generic))

    def test_select_answer_nodes_for_pre_existing_prefers_direct_exclusion_then_definition(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 投保前已存在病況包唔包？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將不會負責任何：- 已存在之狀況。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/exclusion.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 1 Exclusions",
                        "topic_tags": "pre_existing, exclusion",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="指受保寵物在保單生效日期前已存在或出現徵狀的疾病、傷患或身體狀況。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/definition.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "20",
                        "topic_tags": "pre_existing, definition",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="一般保障包括診症及手術費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/benefit.md",
                        "chunk_index": "3",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["clauses"] for node in selected], ["Section 1 Exclusions"])

    def test_intent_summary_includes_key_flags(self) -> None:
        intent = detect_query_intent("Blue Cross 投保前已存在病況包唔包？")
        summary = intent_summary(intent)
        self.assertIn("pre_existing", summary)
        self.assertIn("coverage", summary)
        self.assertIn("exclusion", summary)

    def test_detect_query_intent_extracts_target_providers_for_comparison(self) -> None:
        intent = detect_query_intent("OneDegree 同 Blue Cross 癌症等候期分別係幾多日？")
        self.assertTrue(intent.wants_comparison)
        self.assertEqual(intent.target_providers, ("one_degree", "bluecross"))

    def test_select_answer_nodes_for_comparison_prefers_named_providers(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree 同 Blue Cross 癌症等候期分別係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="OneDegree 癌症等候期 180 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_path": "one_degree/a.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,cancer,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="Blue Cross 癌症等候期 90 天。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/a.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "Waiting Periods",
                        "topic_tags": "definition,waiting_period,cancer,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="Prudential 癌症等候期 180 天。",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/a.md",
                        "chunk_index": "3",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "topic_tags": "waiting_period,cancer,standard_waiting_period",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["provider"] for node in selected], ["one_degree", "bluecross"])

    def test_comparison_provider_backfill_guarantees_named_provider_candidates(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree 同 Blue Cross 癌症等候期分別係幾多日？")
        docs = {
            "a": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "zh",
                    "source_path": "one_degree/a.md",
                    "chunk_index": "1",
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Waiting Periods",
                    "clauses": "2.1.1-2.1.3",
                    "topic_tags": "waiting_period,cancer,standard_waiting_period",
                },
                text="OneDegree 癌症等候期 180 天。",
            ),
            "b": TextNode(
                metadata={
                    "provider": "bluecross",
                    "language": "zh",
                    "source_path": "bluecross/b.md",
                    "chunk_index": "2",
                    "unit_types": "definition",
                    "section_path": "Policy > Definitions",
                    "clauses": "27",
                    "topic_tags": "definition,waiting_period,cancer,standard_waiting_period",
                },
                text="Blue Cross 癌症等候期 90 天。",
            ),
            "c": TextNode(
                metadata={
                    "provider": "prudential",
                    "language": "zh",
                    "source_path": "prudential/c.md",
                    "chunk_index": "3",
                    "unit_types": "waiting_period",
                    "section_path": "Policy > Waiting Periods",
                    "clauses": "10",
                    "topic_tags": "waiting_period,cancer,standard_waiting_period",
                },
                text="Prudential 癌症等候期 180 天。",
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        selected = comparison_provider_backfill_nodes(cfg, index, intent, language="zh")
        self.assertEqual([node.node.metadata["provider"] for node in selected], ["one_degree", "bluecross"])

    def test_build_deterministic_answer_formats_waiting_period_comparison(self) -> None:
        intent = detect_query_intent("OneDegree 同 Blue Cross 癌症等候期分別係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="OneDegree 癌症等候期 180 天，其他病況 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/a.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,cancer,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="Blue Cross 癌症 90 天；受傷 7 天；其他病況 30 天。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/b.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "27",
                        "topic_tags": "definition,waiting_period,cancer,standard_waiting_period,injury",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("OneDegree", answer)
        self.assertIn("180 日", answer)
        self.assertIn("Blue Cross", answer)
        self.assertIn("90 日", answer)
        self.assertEqual(structured["type"], "waiting_period_comparison")
        self.assertEqual([fact["provider"] for fact in structured["facts"]], ["one_degree", "bluecross"])

    def test_build_deterministic_answer_formats_generic_limit_comparison_en(self) -> None:
        intent = detect_query_intent("Compare Blue Cross and Prudential room and board annual limits.")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### B. Room and Board\n"
                        "The Company shall cover the Insured Pet for the cost incurred in a licensed Vet clinic "
                        "for a confinement for a period not less than 12 consecutive hours due to sickness or injury."
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/room_board_en.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit",
                        "benefit_labels": "room and board, hospitalisation, hospitalization",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### B. Room and Board\n"
                        "The Company will pay the incurred cost of the Insured Furry Kid in a licensed Vet clinic "
                        "or hospital for a confinement of no less than 12 consecutive hours.\n"
                        "Plan A: HK$3,500 per year (HK$250 per day).\n"
                        "Plan B: HK$7,000 per year (HK$500 per day)."
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/room_board_en.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit",
                        "benefit_labels": "room and board, hospitalisation, hospitalization",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("Based on the policy clauses retrieved so far:", answer)
        self.assertIn("Blue Cross", answer)
        self.assertIn("no explicit limit amount was found", answer)
        self.assertIn("Prudential", answer)
        self.assertIn("Plan A: HK$3,500 per year", answer)
        self.assertEqual(structured["type"], "benefit_limit_comparison")
        self.assertEqual([fact["provider"] for fact in structured["facts"]], ["bluecross", "prudential"])
        self.assertEqual(structured["facts"][0]["status"], "partial_evidence")
        self.assertEqual(structured["facts"][1]["status"], "ok")

    def test_build_deterministic_answer_formats_generic_limit_comparison_zh(self) -> None:
        intent = detect_query_intent("Compare Blue Cross and Prudential 住院費用最高賠償額。")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### B) 住院費用\n本公司將支付受保寵物於受保期內因疾病或受傷而需於獸醫診所住院不少於連續12小時之費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/room_board_zh.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit",
                        "benefit_labels": "住院費用, room and board, hospitalisation",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### B. 住院費用\n"
                        "本公司將支付受保毛孩於保險期內因疾病或身體損傷而需在持牌獸醫診所或醫院不少於連續12小時之住院費用。\n"
                        "計劃A：港幣3,500/年（每天最多港幣250）。\n"
                        "計劃B：港幣7,000/年（每天最多港幣500）。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/room_board_zh.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit",
                        "benefit_labels": "住院費用, room and board, hospitalisation",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("根據目前檢索到的保單條款：", answer)
        self.assertIn("Blue Cross", answer)
        self.assertIn("沒有明確的最高賠償額數字", answer)
        self.assertIn("Prudential", answer)
        self.assertIn("計劃A：港幣3,500/年", answer)
        self.assertEqual(structured["type"], "benefit_limit_comparison")
        self.assertEqual([fact["provider"] for fact in structured["facts"]], ["bluecross", "prudential"])
        self.assertEqual(structured["facts"][0]["status"], "partial_evidence")
        self.assertEqual(structured["facts"][1]["status"], "ok")

    def test_build_deterministic_answer_prefers_direct_cancer_waiting_period_over_other_illness_days(self) -> None:
        intent = detect_query_intent("OneDegree 同 Blue Cross 癌症等候期分別係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "本保單設有等候期。除了癌症（惡性腫瘤）、癲癇、四肢癱瘓及髕骨脫臼外，"
                        "其他病況的等候期均為 28 天。癌症（惡性腫瘤）的等候期為 180 天。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/a.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,cancer,standard_waiting_period,illness",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="就受保寵物因癌症引致的任何索償：90天。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/b.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "27",
                        "topic_tags": "definition,waiting_period,cancer,standard_waiting_period",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, _ = result
        self.assertIn("180 日", answer)
        self.assertNotIn("28 日（條款 2.1.1-2.1.3）", answer)

    def test_build_deterministic_answer_formats_single_provider_english_waiting_period(self) -> None:
        intent = detect_query_intent("What is the Blue Cross waiting period for injury?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "A period of: in respect of any claim arising from cancer, 90 days; "
                        "in respect of any claim arising from an Injury of the Insured Pet, 7 days; "
                        "in respect of any other conditions, 30 days."
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/b.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "27",
                        "topic_tags": "definition,waiting_period,cancer,injury,standard_waiting_period",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("Blue Cross injury waiting period is 7 days", answer)
        self.assertEqual(structured["type"], "waiting_period_single")
        self.assertEqual(structured["days"], 7)

    def test_build_deterministic_answer_formats_single_provider_pre_existing_zh(self) -> None:
        intent = detect_query_intent("Blue Cross 投保前已存在病況包唔包？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將不會負責任何：- 已存在之狀況。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/exclusion.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 1 Exclusions",
                        "topic_tags": "pre_existing, exclusion",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="指受保寵物在保單生效日期前已存在或出現徵狀的疾病、傷患或身體狀況。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/definition.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "20",
                        "topic_tags": "pre_existing, definition",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("明確將投保前已存在病況列為不保事項", answer)
        self.assertEqual(structured["type"], "pre_existing_single")
        self.assertTrue(structured["excluded"])

    def test_build_deterministic_answer_formats_single_provider_pre_existing_en(self) -> None:
        intent = detect_query_intent("Does Prudential cover pre-existing conditions?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company will not be liable for: - (a) Pre-existing conditions",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/exclusion.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 1 Exclusions",
                        "topic_tags": "pre_existing, exclusion",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("explicitly excludes pre-existing conditions", answer)
        self.assertEqual(structured["type"], "pre_existing_single")
        self.assertTrue(structured["excluded"])

    def test_select_answer_nodes_for_consult_prefers_direct_consult_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 包唔包獸醫診症？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將支付受保寵物於受保期內因疾病或受傷而接受獸醫診症時的所有獸醫費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="一般保障包括 X 光檢查、超聲波檢查及化驗費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/generic.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.A",
                        "topic_tags": "benefit",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["clauses"] for node in selected], ["1.C"])

    def test_select_answer_nodes_for_consult_limit_comparison_prefers_direct_consult_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Compare Blue Cross and Prudential veterinary consultation limits.")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company shall cover the Insured Pet for all Vet Expenses made for the consultation carried out by a Vet.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company will pay the actual expenses necessarily and reasonably incurred for the cremation and funeral service for the Insured Furry Kid. Plan A maximum: HK$1,500 per policy year.",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/funeral.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "Section 3",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company will provide the following for the Insured Furry Kid: all Vet expenses made for the consultation carried out by a Vet. Plan A: HK$8,000 per year (HK$400 per visit, max 20 visits). Plan B: HK$16,000 per year (HK$800 per visit, max 20 visits).",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/consult.md",
                        "chunk_index": "3",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["provider"] for node in selected], ["bluecross", "prudential"])
        self.assertEqual([node.node.metadata["clauses"] for node in selected], ["1.C", "1.C"])

    def test_comparison_provider_backfill_prefers_consult_clause_for_named_provider(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Compare Blue Cross and Prudential veterinary consultation limits.")
        docs = {
            "bluecross_consult": TextNode(
                metadata={
                    "provider": "bluecross",
                    "language": "en",
                    "source_path": "bluecross/consult.md",
                    "chunk_index": "1",
                    "unit_types": "benefit",
                    "section_path": "Policy > Benefits > Veterinary Consultation",
                    "clauses": "1.C",
                    "topic_tags": "benefit, consult",
                },
                text="The Company shall cover the Insured Pet for all Vet Expenses made for the consultation carried out by a Vet.",
            ),
            "prudential_funeral": TextNode(
                metadata={
                    "provider": "prudential",
                    "language": "en",
                    "source_path": "prudential/funeral.md",
                    "chunk_index": "2",
                    "unit_types": "benefit",
                    "section_path": "Policy > Benefits > Funeral Expenses",
                    "clauses": "Section 3",
                    "topic_tags": "benefit",
                },
                text="The Company will pay the actual expenses necessarily and reasonably incurred for the cremation and funeral service. Plan A maximum: HK$1,500 per policy year.",
            ),
            "prudential_consult": TextNode(
                metadata={
                    "provider": "prudential",
                    "language": "en",
                    "source_path": "prudential/consult.md",
                    "chunk_index": "3",
                    "unit_types": "benefit",
                    "section_path": "Policy > Benefits > Veterinary Consultation",
                    "clauses": "1.C",
                    "topic_tags": "benefit, consult",
                },
                text="The Company will provide all Vet expenses made for the consultation carried out by a Vet. Plan A: HK$8,000 per year (HK$400 per visit, max 20 visits).",
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        selected = comparison_provider_backfill_nodes(cfg, index, intent, language="en")
        self.assertEqual([node.node.metadata["provider"] for node in selected], ["bluecross", "prudential"])
        self.assertEqual([node.node.metadata["clauses"] for node in selected], ["1.C", "1.C"])

    def test_build_deterministic_answer_formats_consult_coverage_zh(self) -> None:
        intent = detect_query_intent("Blue Cross 包唔包獸醫診症？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司將支付受保寵物於受保期內因疾病或受傷而接受獸醫診症時的所有獸醫費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("涵蓋獸醫診症", answer)
        self.assertEqual(structured["type"], "consult_coverage_single")
        self.assertEqual(structured["consultation_label"], "獸醫診症")

    def test_build_deterministic_answer_merges_consult_labels_for_one_degree(self) -> None:
        intent = detect_query_intent("OneDegree 包唔包獸醫診症？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="我們將賠償註冊普通科獸醫診金。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/general.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.2",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="我們將賠償註冊專科獸醫診金及緊急診症費用。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/specialist.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.2",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("涵蓋獸醫診症", answer)
        self.assertEqual(structured["type"], "consult_coverage_single")

    def test_build_deterministic_answer_formats_consult_limit_zh(self) -> None:
        intent = detect_query_intent("Prudential 獸醫診症最高賠償額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "本公司將支付受保毛孩下列之費用：因疾病或身體損傷而接受獸醫診症時的所有獸醫費用。"
                        "計劃A：港幣8,000/年（每次最多港幣400，計劃A及B均只限最多20次）。"
                        "計劃B：港幣16,000/年（每次最多港幣800，計劃A及B均只限最多20次）。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("計劃A：港幣8,000/年", answer)
        self.assertIn("每次最多港幣400", answer)
        self.assertEqual(structured["type"], "consult_limit_single")
        self.assertEqual(structured["plan_limits"]["A"]["max_visits"], "20")

    def test_build_deterministic_answer_formats_consult_limit_comparison_en(self) -> None:
        intent = detect_query_intent("Compare Blue Cross and Prudential veterinary consultation limits.")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company shall cover the Insured Pet for all Vet Expenses made for the consultation carried out by a Vet.",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "The Company will provide the following for the Insured Furry Kid: "
                        "all Vet expenses made for the consultation carried out by a Vet for Illness or Bodily Injury. "
                        "Plan A: HK$8,000 per year (HK$400 per visit, max 20 visits). "
                        "Plan B: HK$16,000 per year (HK$800 per visit, max 20 visits)."
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/consult.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("Based on the policy clauses retrieved so far:", answer)
        self.assertIn("Blue Cross", answer)
        self.assertIn("no explicit limit amount was found", answer)
        self.assertIn("Prudential", answer)
        self.assertIn("Plan A: HK$8,000 per year", answer)
        self.assertEqual(structured["type"], "consult_limit_comparison")
        self.assertEqual([fact["provider"] for fact in structured["facts"]], ["bluecross", "prudential"])
        self.assertEqual(structured["facts"][0]["status"], "partial_evidence")
        self.assertEqual(structured["facts"][1]["status"], "ok")

    def test_build_deterministic_answer_formats_consult_limit_comparison_missing_provider(self) -> None:
        intent = detect_query_intent("Compare MSIG and Prudential veterinary consultation limits.")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "The Company will provide the following for the Insured Furry Kid: "
                        "all Vet expenses made for the consultation carried out by a Vet for Illness or Bodily Injury. "
                        "Plan A: HK$8,000 per year (HK$400 per visit, max 20 visits). "
                        "Plan B: HK$16,000 per year (HK$800 per visit, max 20 visits)."
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/consult.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("Prudential", answer)
        self.assertIn("Plan A: HK$8,000 per year", answer)
        self.assertEqual(structured["type"], "consult_limit_comparison")
        by_provider = {fact["provider"]: fact["status"] for fact in structured["facts"]}
        self.assertEqual(by_provider["prudential"], "ok")
        self.assertEqual(by_provider["msig"], "missing_evidence")

    def test_build_deterministic_answer_formats_consult_limit_missing_numbers(self) -> None:
        intent = detect_query_intent("What is the Blue Cross annual limit for vet consultation?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="The Company shall cover the Insured Pet for all Vet Expenses made for the consultation carried out by a Vet.",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/consult.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.C",
                        "topic_tags": "benefit, consult",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("no explicit limit amount was found", answer)
        self.assertEqual(structured["type"], "consult_limit_single")
        self.assertFalse(structured["has_explicit_limit"])

    def test_build_deterministic_answer_formats_generic_limit_zh(self) -> None:
        intent = detect_query_intent("Prudential 住院費用最高賠償額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### B. 住院費用\n"
                        "本公司將支付受保毛孩於保險期內因疾病或身體損傷而需在持牌獸醫診所或醫院不少於連續12小時之住院費用。\n"
                        "計劃A：港幣3,500/年（每天最多港幣250）。\n"
                        "計劃B：港幣7,000/年（每天最多港幣500）。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/room_board.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit, injury",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("計劃A：港幣3,500/年", answer)
        self.assertIn("計劃B：港幣7,000/年", answer)
        self.assertEqual(structured["type"], "benefit_limit_single")
        self.assertEqual(structured["benefit_label"], "B. 住院費用")

    def test_build_deterministic_answer_formats_generic_limit_en(self) -> None:
        intent = detect_query_intent("What is the annual limit for Prudential room and board?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### B. Room and Board\n"
                        "The Company will pay the incurred cost of the Insured Furry Kid in a licensed Vet clinic or hospital for a confinement of no less than 12 consecutive hours.\n"
                        "Plan A: HK$3,500 per year (HK$250 per day).\n"
                        "Plan B: HK$7,000 per year (HK$500 per day)."
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/room_board_en.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "topic_tags": "benefit, injury",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("Plan A: HK$3,500 per year", answer)
        self.assertIn("Plan B: HK$7,000 per year", answer)
        self.assertEqual(structured["type"], "benefit_limit_single")
        self.assertEqual(structured["benefit_label"], "B. Room and Board")

    def test_build_deterministic_answer_formats_generic_limit_missing_numbers(self) -> None:
        intent = detect_query_intent("What is the annual limit for MSIG surgery?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### Benefit Matrix Summary\n"
                        "The Table of Benefits contains plan-specific maximum annual limits for surgery-related benefits:\n"
                        "- Section I Surgical Benefit, including Clinical and Surgical Benefit.\n"
                        "- Section II Hospitalisation Benefit.\n"
                    ),
                    metadata={
                        "provider": "msig",
                        "source_name": "msig.md",
                        "source_path": "msig/benefit_table.md",
                        "chunk_index": "2",
                        "unit_types": "benefit_table",
                        "clauses": "Table of Benefits",
                        "topic_tags": "benefit_table",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("no explicit limit amount was found", answer)
        self.assertEqual(structured["type"], "benefit_limit_single")
        self.assertFalse(structured["has_explicit_limit"])

    def test_build_deterministic_answer_formats_renewal_age_zh(self) -> None:
        intent = detect_query_intent("Blue Cross 可以續保到幾歲？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### 17. 續保\n"
                        "受本公司享有終止本保單權利之條款約束下，於保單期屆滿時，本保單將自動續保至下一個受保期。\n"
                        "如符合上述規定，本公司保證受保寵物可續保至13歲。任何13歲以上之續保須經核保審批。"
                    ),
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/renewal.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "17",
                        "topic_tags": "renewal",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("13 歲", answer)
        self.assertEqual(structured["type"], "renewal_rule_single")
        self.assertEqual(structured["guaranteed_age"], 13)

    def test_build_deterministic_answer_formats_renewal_auto_en(self) -> None:
        intent = detect_query_intent("Can Prudential be renewed automatically?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### XVIII. Renewal\n"
                        "If the payment method selected is by credit card the Policy will be renewed automatically on a yearly basis upon the successful premium collection."
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/renewal_en.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "XVIII",
                        "topic_tags": "renewal",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("renewed automatically", answer)
        self.assertEqual(structured["type"], "renewal_rule_single")
        self.assertTrue(structured["auto_renew"])

    def test_build_deterministic_answer_formats_upgrade_waiting_period_zh(self) -> None:
        intent = detect_query_intent("OneDegree 升級後等候期會點計？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 升級等候期\n"
                        "若你於續保本保單時升級至年度保障總額較高的計劃，增加的年度保障總額部份將設有按條款 2.1.3 所訂明的等候期。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/upgrade_waiting.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.6",
                        "topic_tags": "waiting_period,upgrade",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 標準等候期\n其他病況的等候期均為 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/standard_waiting.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,standard_waiting_period",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("新增保障部份會設有等候期", answer)
        self.assertEqual(structured["type"], "upgrade_rule_single")
        self.assertTrue(structured["upgrade_waiting_period"])

    def test_build_deterministic_answer_formats_critical_cash_benefit_age_limit_zh(self) -> None:
        intent = detect_query_intent("OneDegree 危疾現金保障有咩年齡限制？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 年齡限制\n危疾現金保障只供 1 歲至 9 歲的寵物投保，並於你的寵物到達 10 歲起無法續保。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/critical_cash_age.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.4.6",
                        "topic_tags": "eligibility,critical_illness",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 年齡限制\n高級危疾現金保障只供 1 歲至 9 歲的寵物投保，續保時則不設年齡限制。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/advanced_cash_age.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.6.6",
                        "topic_tags": "eligibility,critical_illness",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("1 歲至 9 歲", answer)
        self.assertIn("10 歲起無法續保", answer)
        self.assertEqual(structured["type"], "addon_eligibility_single")
        self.assertEqual(structured["clauses"], "1.4.6")
        self.assertEqual(structured["min_age"], 1)
        self.assertEqual(structured["max_age"], 9)
        self.assertEqual(structured["renewal_cutoff_age"], 10)

    def test_build_deterministic_answer_formats_advanced_cash_benefit_age_limit_en(self) -> None:
        intent = detect_query_intent("What is the age limit for OneDegree advanced critical illness cash benefit?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### Age Limit\nThis cash benefit is only available for Pet from 1 year old to 9 years old and not renewable once Your Pet becomes 10 years old.",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/critical_cash_age_en.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.4.6",
                        "topic_tags": "eligibility,critical_illness",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### Age Limit\nThis cash benefit is only available for Pet from 1 year old to 9 years old for new purchase. There is no Age limit for renewals.",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/advanced_cash_age_en.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.6.6",
                        "topic_tags": "eligibility,critical_illness",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("from 1 to 9 years old", answer)
        self.assertIn("no age limit for renewals", answer)
        self.assertEqual(structured["type"], "addon_eligibility_single")
        self.assertEqual(structured["clauses"], "1.6.6")
        self.assertTrue(structured["renewal_no_age_limit"])

    def test_build_deterministic_answer_formats_fip_waiting_period_zh(self) -> None:
        intent = detect_query_intent("OneDegree 貓傳染性腹膜炎額外保額等候期係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 標準等候期\n其他病況的等候期均為 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/standard_waiting.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 貓傳染性腹膜炎額外保額等候期\n若於條款 1.5 所述的附加保障適用於你的保單，此附加保障的等候期為包括貓傳染性腹膜炎額外保額之保障期的第一天開始計算 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/fip_waiting.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.8",
                        "topic_tags": "waiting_period,addon_waiting_period,fip",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("28 日", answer)
        self.assertIn("貓傳染性腹膜炎額外保額", answer)
        self.assertEqual(structured["type"], "addon_waiting_period_single")
        self.assertEqual(structured["clauses"], "2.1.8")

    def test_build_deterministic_answer_formats_fip_eligibility_conditions_zh(self) -> None:
        intent = detect_query_intent("OneDegree 貓傳染性腹膜炎額外保額有咩受保條件？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 受保條件\n"
                        "- 由獸醫向你的寵物處方該藥物。\n"
                        "- 你的寵物首次因貓傳染性腹膜炎出現症狀、確診、使用藥物、接受醫療建議或治療的日期，必須為條款 2.1 所訂明的等候期之後。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/fip_eligibility.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.5",
                        "topic_tags": "eligibility,fip",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 年齡限制\n高級危疾現金保障只供 1 歲至 9 歲的寵物投保，續保時則不設年齡限制。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/advanced_cash_age.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.6.6",
                        "topic_tags": "eligibility,critical_illness",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("受保條件如下", answer)
        self.assertIn("由獸醫向你的寵物處方該藥物", answer)
        self.assertNotIn("）。：", answer)
        self.assertEqual(structured["type"], "addon_conditions_single")
        self.assertEqual(structured["clauses"], "1.5")
        self.assertEqual(len(structured["conditions"]), 2)

    def test_build_deterministic_answer_prefers_upgrade_age_block_when_age_is_asked(self) -> None:
        intent = detect_query_intent("OneDegree 升級至較高年度保障總額的計劃幾歲後唔接受？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### 升級\n升級計劃後新增的保障部份將設等候期。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/upgrade_waiting.md",
                        "chunk_index": "1",
                        "unit_types": "renewal_rule",
                        "clauses": "4.6.1",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="你可於保單續保日前的 30 天內申請更改保障計劃，惟需經我們批准。若你的寵物於即將續保時已屆 12 歲，我們不接受升級至具有更高年度保障總額的計劃。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/change_plan.md",
                        "chunk_index": "2",
                        "unit_types": "renewal_rule",
                        "clauses": "4.5",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("12 歲", answer)
        self.assertIn("不接受升級", answer)
        self.assertEqual(structured["type"], "upgrade_rule_single")
        self.assertEqual(structured["age_upgrade_block"], 12)

    def test_build_deterministic_answer_formats_chronic_condition_age_5_rule_zh(self) -> None:
        intent = detect_query_intent("OneDegree 慢性病況 5 歲或以上仲包唔包？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 慢性病況限制規則 — 寵物年齡 5 歲或以上\n"
                        "若你的寵物在以下日期（較早者為準）已屆 5 歲或以上：\n"
                        "- 在首次購買本保單時；或\n"
                        "- 你升級本保單至比原計劃具有更高年度保障總額的計劃的保單續保日；\n"
                        "我們僅於你的寵物首次因以上慢性疾病出現症狀、確診、使用藥物、接受醫療建議或治療的保單年度提供保障。惟續保後，有關慢性疾病將不再受保。\n"
                        "任何情況下，若你的寵物於條款 2.1 所述適用於首個保單年度及升級保單的保單等候期完結前，因任何慢性病況出現症狀、確診、使用藥物、接受醫療建議或治療，相關的慢性疾病將不獲賠償。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age5.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("5 歲或以上", answer)
        self.assertIn("續保後", answer)
        self.assertIn("不再受保", answer)
        self.assertEqual(structured["type"], "chronic_condition_rule_single")
        self.assertEqual(structured["primary_rule_kind"], "age_5_or_above")

    def test_build_deterministic_answer_formats_chronic_condition_upgrade_rule_en(self) -> None:
        intent = detect_query_intent("If a OneDegree pet is already 5 years old, are chronic conditions still covered after plan upgrade?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### Chronic Condition Limitation Rule — Pet Age 5 or Above\n"
                        "If Your Pet is 5 years old or above on the earlier of:\n"
                        "- Your first Policy Start Date; or\n"
                        "- the Policy Renewal Date of which You upgrade the Policy to a plan with a higher Annual Limit than the original plan;\n"
                        "We will only provide coverage for the above Chronic Medical Conditions in the policy year that Your Pet first developed Symptoms or received a Diagnosis, medication, advice, or treatment. The related Chronic Medical Conditions will be excluded from the subsequent renewal policies.\n"
                        "In any event, we do not cover any Chronic Medical Conditions for which Your Pet has developed Symptoms or received a Diagnosis, medication, advice, or treatment before the end of the Waiting Period applicable to first Policy Year and Upgrade of Policy set out in part 2.1."
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age5_en.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### Upgrade\n"
                        "Waiting Period applies to the Additional Coverage due to upgrade of plan.\n"
                        "If the upgrade of plan happens on or after Your Pet's turning 5 years old, the age-relevant condition on Chronic Medical Conditions coverage set out in part 1.1 applies to the Additional Coverage but the Original Coverage is unaffected."
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_upgrade_en.md",
                        "chunk_index": "2",
                        "unit_types": "renewal_rule",
                        "clauses": "4.6.1",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("5 years old or above", answer)
        self.assertIn("excluded from subsequent renewals", answer)
        self.assertIn("Original Coverage is unaffected", answer)
        self.assertEqual(structured["type"], "chronic_condition_rule_single")
        self.assertEqual(structured["primary_rule_kind"], "age_5_or_above")
        self.assertTrue(structured["original_coverage_unaffected"])

    def test_build_deterministic_answer_formats_general_chronic_condition_summary_zh(self) -> None:
        intent = detect_query_intent("OneDegree 慢性病況點計？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 慢性病況保障規則 — 寵物年齡 4 歲或以下\n"
                        "若你的寵物在以下日期（較後者為準）為 4 歲或以下，"
                        "及於適用等候期完結前未曾因以上慢性病況出現症狀、確診、使用藥物、接受醫療建議或治療，我們將全面保障上述的慢性病況。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age4_zh.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 慢性病況限制規則 — 寵物年齡 5 歲或以上\n"
                        "若你的寵物在以下日期（較早者為準）已屆 5 歲或以上，"
                        "我們僅於你的寵物首次因以上慢性疾病出現症狀、確診、使用藥物、接受醫療建議或治療的保單年度提供保障。惟續保後，有關慢性疾病將不再受保。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age5_zh.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### 升級\n"
                        "升級計劃後新增的保障部份將設等候期。\n"
                        "若升級計劃發生在你的寵物滿 5 歲或之後，條款 1.1 所述的慢性病況年齡相關條件適用於新增保障，但原保障不受影響。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_upgrade_zh.md",
                        "chunk_index": "3",
                        "unit_types": "renewal_rule",
                        "clauses": "4.6.1",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("兩個年齡分支", answer)
        self.assertIn("4 歲或以下", answer)
        self.assertIn("5 歲或以上", answer)
        self.assertEqual(structured["type"], "chronic_condition_rule_summary")
        self.assertTrue(structured["has_age_4_or_below_rule"])
        self.assertTrue(structured["has_age_5_or_above_rule"])

    def test_select_answer_nodes_keeps_chronic_age_rule_and_upgrade_rule_together(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("If a OneDegree pet is already 5 years old, are chronic conditions still covered after plan upgrade?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### Chronic Condition Coverage Rule — Pet Age 4 or Below\n"
                        "If Your Pet is 4 years old or below on the later of the first Policy Start Date or the Policy Renewal Date of upgrade, "
                        "and no symptoms happen before the Waiting Period ends, those Chronic Medical Conditions are fully covered."
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age4_en.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### Chronic Condition Limitation Rule — Pet Age 5 or Above\n"
                        "If Your Pet is 5 years old or above on the earlier of the first Policy Start Date or the renewal date of upgrade, "
                        "the related Chronic Medical Conditions are only covered in the policy year they first arise and are excluded from subsequent renewal policies."
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age5_en.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "##### Upgrade\n"
                        "Waiting Period applies to the Additional Coverage due to upgrade of plan.\n"
                        "If the upgrade of plan happens on or after Your Pet's turning 5 years old, the age-relevant condition on Chronic Medical Conditions coverage set out in part 1.1 applies to the Additional Coverage but the Original Coverage is unaffected."
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_upgrade_en.md",
                        "chunk_index": "3",
                        "unit_types": "renewal_rule",
                        "clauses": "4.6.1",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected[:2]],
            ["one_degree/chronic_age5_en.md", "one_degree/chronic_upgrade_en.md"],
        )

    def test_select_answer_nodes_for_general_chronic_query_keeps_both_age_branches(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("How does OneDegree cover chronic conditions?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### Chronic Condition Coverage Rule — Pet Age 4 or Below\nThose chronic conditions are fully covered if no related symptoms happen before the Waiting Period ends.",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age4_en.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### Chronic Condition Limitation Rule — Pet Age 5 or Above\nThose chronic conditions are only covered in the policy year they first arise and are excluded from subsequent renewals.",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_age5_en.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "clauses": "1.1",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="##### Upgrade\nIf the upgrade of plan happens on or after Your Pet's turning 5 years old, the age-relevant condition applies to the Additional Coverage but the Original Coverage is unaffected.",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/chronic_upgrade_en.md",
                        "chunk_index": "3",
                        "unit_types": "renewal_rule",
                        "clauses": "4.6.1",
                        "topic_tags": "upgrade,renewal",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected[:2]],
            ["one_degree/chronic_age4_en.md", "one_degree/chronic_age5_en.md"],
        )

    def test_chronic_condition_backfill_pulls_age_rule_and_upgrade_rule(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("If a OneDegree pet is already 5 years old, are chronic conditions still covered after plan upgrade?")
        docs = {
            "a": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_age4_en.md",
                    "chunk_index": "1",
                    "unit_types": "eligibility",
                    "section_path": "Policy > Covered Medical Conditions",
                    "clauses": "1.1",
                    "topic_tags": "eligibility",
                },
                text=(
                    "##### Chronic Condition Coverage Rule — Pet Age 4 or Below\n"
                    "If Your Pet is 4 years old or below on the later of the first Policy Start Date or the Policy Renewal Date of upgrade, "
                    "those Chronic Medical Conditions are fully covered."
                ),
            ),
            "b": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_age5_en.md",
                    "chunk_index": "2",
                    "unit_types": "eligibility",
                    "section_path": "Policy > Covered Medical Conditions",
                    "clauses": "1.1",
                    "topic_tags": "eligibility",
                },
                text=(
                    "##### Chronic Condition Limitation Rule — Pet Age 5 or Above\n"
                    "If Your Pet is 5 years old or above on the earlier of the first Policy Start Date or the renewal date of upgrade, "
                    "the related Chronic Medical Conditions are only covered in the policy year they first arise and are excluded from subsequent renewal policies."
                ),
            ),
            "c": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_upgrade_en.md",
                    "chunk_index": "3",
                    "unit_types": "renewal_rule",
                    "section_path": "Policy > Upgrade",
                    "clauses": "4.6.1",
                    "topic_tags": "upgrade,renewal",
                },
                text=(
                    "##### Upgrade\n"
                    "Waiting Period applies to the Additional Coverage due to upgrade of plan.\n"
                    "If the upgrade of plan happens on or after Your Pet's turning 5 years old, the age-relevant condition on Chronic Medical Conditions coverage set out in part 1.1 applies to the Additional Coverage but the Original Coverage is unaffected."
                ),
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        selected = chronic_condition_backfill_nodes(cfg, index, intent, provider="one_degree", language="en")
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected[:2]],
            ["one_degree/chronic_age5_en.md", "one_degree/chronic_upgrade_en.md"],
        )

    def test_chronic_condition_backfill_for_general_query_pulls_both_age_branches(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("How does OneDegree cover chronic conditions?")
        docs = {
            "a": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_age4_en.md",
                    "chunk_index": "1",
                    "unit_types": "eligibility",
                    "section_path": "Policy > Covered Medical Conditions",
                    "clauses": "1.1",
                    "topic_tags": "eligibility",
                },
                text="##### Chronic Condition Coverage Rule — Pet Age 4 or Below\nThose chronic conditions are fully covered if no related symptoms happen before the Waiting Period ends.",
            ),
            "b": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_age5_en.md",
                    "chunk_index": "2",
                    "unit_types": "eligibility",
                    "section_path": "Policy > Covered Medical Conditions",
                    "clauses": "1.1",
                    "topic_tags": "eligibility",
                },
                text="##### Chronic Condition Limitation Rule — Pet Age 5 or Above\nThose chronic conditions are only covered in the policy year they first arise and are excluded from subsequent renewals.",
            ),
            "c": TextNode(
                metadata={
                    "provider": "one_degree",
                    "language": "en",
                    "source_path": "one_degree/chronic_upgrade_en.md",
                    "chunk_index": "3",
                    "unit_types": "renewal_rule",
                    "section_path": "Policy > Upgrade",
                    "clauses": "4.6.1",
                    "topic_tags": "upgrade,renewal",
                },
                text="##### Upgrade\nIf the upgrade of plan happens on or after Your Pet's turning 5 years old, the age-relevant condition applies to the Additional Coverage but the Original Coverage is unaffected.",
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        selected = chronic_condition_backfill_nodes(cfg, index, intent, provider="one_degree", language="en")
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected[:2]],
            ["one_degree/chronic_age4_en.md", "one_degree/chronic_age5_en.md"],
        )

    def test_select_answer_nodes_for_general_age_limit_prefers_eligibility_over_benefit_table(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the age limit for MSIG?")
        nodes = [
            SimpleNamespace(
                score=0.9,
                node=SimpleNamespace(
                    text=(
                        "### Benefit Matrix Summary\n"
                        "The Table of Benefits contains plan-specific maximum limits for:\n"
                        "- Total Annual Coverage for Sections I to III; and\n"
                        "- Maximum limits for various sections."
                    ),
                    metadata={
                        "provider": "msig",
                        "source_name": "msig.md",
                        "source_path": "msig/benefit_table.md",
                        "chunk_index": "1",
                        "unit_types": "benefit_table",
                        "section_path": "Policy > Table of Benefits",
                        "clauses": "Table of Benefits",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.3,
                node=SimpleNamespace(
                    text=(
                        "### Pet Eligibility\n"
                        "Unless We agree in writing otherwise, the Pet must:\n"
                        "- be micro-chipped;\n"
                        "- be at least sixteen (16) weeks old and below nine (9) years old at the Commencement Date;\n"
                        "- complete all required Vaccinations; and\n"
                        "- not be a Working Pet."
                    ),
                    metadata={
                        "provider": "msig",
                        "source_name": "msig.md",
                        "source_path": "msig/pet_eligibility.md",
                        "chunk_index": "2",
                        "unit_types": "general_condition",
                        "section_path": "Policy > General Conditions",
                        "clauses": "General Conditions.2",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["source_path"], "msig/pet_eligibility.md")

    def test_select_general_age_limit_answer_nodes_filters_extra_eligibility_noise(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the age limit for Prudential?")
        nodes = [
            SimpleNamespace(
                score=0.7,
                node=SimpleNamespace(
                    text="### II. Age Limit\nUnless otherwise specified in the Schedule, the Insured Furry Kid must be aged between 13 weeks and 8 years old, renewal of this Policy will be subject to underwriting.",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential_policy_en.md",
                        "source_path": "prudential/age_limit.md",
                        "chunk_index": "1",
                        "unit_types": "eligibility",
                        "section_path": "Policy > Conditions",
                        "clauses": "II",
                        "topic_tags": "eligibility,age_limit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.68,
                node=SimpleNamespace(
                    text="### IV. Owner of the Insured Furry Kid\nThe Insured must be the sole owner of the Insured Furry Kid.",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential_policy_en.md",
                        "source_path": "prudential/owner.md",
                        "chunk_index": "2",
                        "unit_types": "eligibility",
                        "section_path": "Policy > Conditions",
                        "clauses": "IV",
                        "topic_tags": "eligibility",
                    },
                ),
            ),
        ]

        selected = select_general_age_limit_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["prudential/age_limit.md"])

    def test_lexical_backfill_for_general_age_limit_finds_pet_eligibility_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the age limit for MSIG?")
        docs = {
            "a": TextNode(
                metadata={
                    "provider": "msig",
                    "language": "en",
                    "source_path": "msig/benefit_table.md",
                    "chunk_index": "1",
                    "unit_types": "benefit_table",
                    "section_path": "Policy > Table of Benefits",
                    "clauses": "Table of Benefits",
                    "topic_tags": "benefit",
                },
                text=(
                    "### Benefit Matrix Summary\n"
                    "The Table of Benefits contains plan-specific maximum limits for Total Annual Coverage."
                ),
            ),
            "b": TextNode(
                metadata={
                    "provider": "msig",
                    "language": "en",
                    "source_path": "msig/pet_eligibility.md",
                    "chunk_index": "2",
                    "unit_types": "general_condition",
                    "section_path": "Policy > General Conditions",
                    "clauses": "General Conditions.2",
                    "topic_tags": "",
                },
                text=(
                    "### Pet Eligibility\n"
                    "Unless We agree in writing otherwise, the Pet must be at least sixteen (16) weeks old and below nine (9) years old at the Commencement Date."
                ),
            ),
        }
        index = SimpleNamespace(storage_context=SimpleNamespace(docstore=SimpleNamespace(docs=docs)))

        backfill = lexical_backfill_nodes(cfg, index, intent.raw_question, intent, provider="msig", language="en")
        self.assertEqual(backfill[0].node.metadata["source_path"], "msig/pet_eligibility.md")

    def test_select_answer_nodes_prefers_critical_cash_benefit_waiting_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree 危疾現金保障等候期係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.9,
                node=SimpleNamespace(
                    text="##### 標準等候期\n其他病況的等候期均為 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/standard_waiting.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.6,
                node=SimpleNamespace(
                    text=(
                        "##### 危疾現金保障等候期\n"
                        "若於條款 1.4 所述的附加保障適用於你的保單，此附加保障的等候期為包括危疾現金保障之保障期的第一天開始計算 180 天。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/critical_cash_waiting.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.7",
                        "topic_tags": "waiting_period,addon_waiting_period,critical_illness",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.7,
                node=SimpleNamespace(
                    text=(
                        "##### 高級危疾現金保障等候期\n"
                        "若於條款 1.6 所述的附加保障適用於你的保單，此附加保障的等候期為包括高級危疾現金保障之保障期的第一天開始計算 180 天。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/advanced_cash_waiting.md",
                        "chunk_index": "3",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.9",
                        "topic_tags": "waiting_period,addon_waiting_period,critical_illness",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["clauses"], "2.1.7")

        result = build_deterministic_answer(intent, selected)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("180 日", answer)
        self.assertEqual(structured["type"], "addon_waiting_period_single")
        self.assertEqual(structured["clauses"], "2.1.7")

    def test_select_answer_nodes_prefers_advanced_cash_benefit_waiting_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree 高級危疾現金保障等候期係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.9,
                node=SimpleNamespace(
                    text="##### 標準等候期\n其他病況的等候期均為 28 天。",
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/standard_waiting.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.1-2.1.3",
                        "topic_tags": "waiting_period,standard_waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.7,
                node=SimpleNamespace(
                    text=(
                        "##### 危疾現金保障等候期\n"
                        "若於條款 1.4 所述的附加保障適用於你的保單，此附加保障的等候期為包括危疾現金保障之保障期的第一天開始計算 180 天。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/critical_cash_waiting.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.7",
                        "topic_tags": "waiting_period,addon_waiting_period,critical_illness",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.6,
                node=SimpleNamespace(
                    text=(
                        "##### 高級危疾現金保障等候期\n"
                        "若於條款 1.6 所述的附加保障適用於你的保單，此附加保障的等候期為包括高級危疾現金保障之保障期的第一天開始計算 180 天。"
                    ),
                    metadata={
                        "provider": "one_degree",
                        "source_name": "one_degree.md",
                        "source_path": "one_degree/advanced_cash_waiting.md",
                        "chunk_index": "3",
                        "unit_types": "waiting_period",
                        "clauses": "2.1.9",
                        "topic_tags": "waiting_period,addon_waiting_period,critical_illness",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["clauses"], "2.1.9")

        result = build_deterministic_answer(intent, selected)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("180 日", answer)
        self.assertEqual(structured["type"], "addon_waiting_period_single")
        self.assertEqual(structured["clauses"], "2.1.9")

    def test_build_deterministic_answer_formats_cost_share_with_percentage(self) -> None:
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/costshare.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("30%", answer)
        self.assertEqual(structured["type"], "cost_share_single")
        self.assertEqual(structured["value"], "30%")

    def test_build_deterministic_answer_formats_cost_share_multi_for_multiple_deductibles(self) -> None:
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### 項目一 -「衛您寵」醫療費用保障\n"
                        "#### 等候期（項目一）\n"
                        "自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/costshare1.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目一 -「衛您寵」醫療費用保障",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### 項目二 - 「寵有責」第三者法律責任保障\n"
                        "項目二所有索償均附設自負額為每宗索償港幣3,000。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/costshare2.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "Section 2",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目二 - 「寵有責」第三者法律責任保障",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("30%", answer)
        self.assertIn("港幣3,000", answer)
        self.assertEqual(structured["type"], "cost_share_multi")
        self.assertEqual(len(structured["facts"]), 2)

    def test_build_deterministic_answer_formats_cost_share_definition_without_numeric_value(self) -> None:
        intent = detect_query_intent("What is the co-insurance for Blue Cross?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="A percentage of Eligible Expense specified in Section 1 and Section 4 of the Table of Benefits that the Policyholder must contribute after paying the deductible.",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/definition.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "3",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("co-insurance", answer)
        self.assertIn("does not include a specific numeric value", answer)
        self.assertEqual(structured["type"], "cost_share_single")
        self.assertEqual(structured["kind"], "co_insurance")

    def test_build_deterministic_answer_formats_prudential_copayment_definition(self) -> None:
        intent = detect_query_intent("What is the Prudential co-payment?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="A percentage of any claims, of which the amount shall be borne by the Insured under Section 1 – \"PAWcare\" Medical Expenses.",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/copayment.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "Definition",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("co-payment", answer)
        self.assertIn("does not include a specific numeric value", answer)
        self.assertEqual(structured["kind"], "co_insurance")
        self.assertEqual(structured["surface_label"], "co-payment")

    def test_build_deterministic_answer_preserves_bluecross_zh_self_pay_term(self) -> None:
        intent = detect_query_intent("Blue Cross 的自付額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### 適用於第二部分之不保事項\n- 每次及每項索償之首港幣3,000元（自付額）",
                    metadata={
                        "provider": "bluecross",
                        "source_name": "bluecross.md",
                        "source_path": "bluecross/deductible.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "Section 2 Exclusions",
                        "topic_tags": "cost_share, cost_share_deductible",
                        "cost_share_kind": "deductible",
                        "cost_share_evidence": "exclusion",
                        "cost_share_has_numeric": "true",
                        "cost_share_value_type": "hkd_amount",
                        "cost_share_mentions_table": "false",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("自付額", answer)
        self.assertEqual(structured["surface_label"], "自付額")

    def test_build_deterministic_waiting_period_answer_handles_zh_day_suffix_ri(self) -> None:
        intent = detect_query_intent("Prudential 等候期係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### 等候期（項目一）\n"
                        "- 疾病（癌症除外）：疾病首次發生應在本保單生效日起的30日等候期終結後。\n"
                        "- 癌症：有關癌症首次發生應在本保單生效日起的180日等候期終結後。\n"
                        "- 身體損傷：有關身體損傷首次發生應在本保單生效日起的7日等候期終結後。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential_policy_zh.md",
                        "source_path": "prudential/waiting_periods_zh.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "topic_tags": "waiting_period",
                    },
                ),
            ),
        ]

        result = build_deterministic_answer(intent, nodes)
        self.assertIsNotNone(result)
        answer, structured = result
        self.assertIn("30", answer)
        self.assertEqual(structured["type"], "waiting_period_single")

    def test_select_cost_share_answer_nodes_prefers_direct_prudential_deductible_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="如出現任何可能根據本保單提出索償的情況，投保人應立即以書面通知本公司。",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/claim_rule.md",
                        "chunk_index": "1",
                        "unit_types": "claim_rule",
                        "clauses": "XII",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 條件",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司不會負責在承保表中列明為自負額條款內的自負額金額。",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/general_exclusions.md",
                        "chunk_index": "2",
                        "unit_types": "exclusion",
                        "clauses": "General Exclusions",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 一般不保項目",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/waiting_periods.md",
                        "chunk_index": "3",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 等候期（項目一）",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["clauses"], "Waiting Periods")

    def test_select_cost_share_answer_nodes_filters_percentage_table_noise_for_coinsurance(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the Blue Cross co-insurance?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Co-Insurance\nA percentage of Eligible Expense that the Policyholder must contribute after paying the deductible (if any) in a Policy Year.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/coinsurance.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "3",
                        "section_path": "LovePet Insurance Policy > Definitions",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### Short Period Rate Table\nWhen the Policyholder cancels the Policy and no claim has been made, the premium to be charged shall be 30% of the annual premium.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/short_period.md",
                        "chunk_index": "2",
                        "unit_types": "renewal_rule",
                        "clauses": "2",
                        "section_path": "LovePet Insurance Policy > General Conditions",
                        "topic_tags": "renewal",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["bluecross/coinsurance.md"])

    def test_select_cost_share_answer_nodes_keeps_only_primary_for_non_numeric_definition_answer(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the Blue Cross co-insurance?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Co-Insurance\nA percentage of Eligible Expense specified in Section 1 and Section 4 of the Table of Benefits that the Policyholder must contribute after paying the deductible (if any) in a Policy Year.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/coinsurance.md",
                        "source_name": "bluecross.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "3",
                        "section_path": "LovePet Insurance Policy > Definitions",
                        "topic_tags": "definition, cost_share, cost_share_co_insurance",
                        "cost_share_kind": "co_insurance",
                        "cost_share_evidence": "definition",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "table_of_benefits",
                        "cost_share_mentions_table": "true",
                        "definition_labels": "co-insurance",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="## Benefit Provisions\nAll benefits payable pursuant to Sections 1-5 below are subject to the maximum limits, Annual Limit, sub-limits and sum insured as stated in the Table of Benefits for the plan selected by the Policyholder, AND subject to the terms, conditions, exclusions, Excess and Co-Insurance of this Policy.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/benefit_intro.md",
                        "source_name": "bluecross.md",
                        "chunk_index": "2",
                        "unit_types": "section",
                        "clauses": "",
                        "section_path": "LovePet Insurance Policy",
                        "topic_tags": "cost_share",
                        "cost_share_kind": "mixed",
                        "cost_share_evidence": "table_reference",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "table_of_benefits",
                        "cost_share_mentions_table": "true",
                        "topic_tags": "cost_share, table_of_benefits",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected],
            ["bluecross/coinsurance.md", "bluecross/benefit_intro.md"],
        )

    def test_select_cost_share_answer_nodes_uses_policy_schedule_support_for_missing_reimbursement_value(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("OneDegree reimbursement ratio is how much?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Reimbursement Rate\nThe reimbursement rate specified in the Policy Schedule that applies to eligible medical expenses.",
                    metadata={
                        "provider": "one_degree",
                        "source_path": "one_degree/reimbursement_ratio.md",
                        "source_name": "one_degree.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "Definition",
                        "section_path": "Policy > Definitions",
                        "topic_tags": "definition, cost_share, cost_share_reimbursement_ratio, policy_schedule",
                        "cost_share_kind": "reimbursement_ratio",
                        "cost_share_evidence": "definition",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "policy_schedule",
                        "cost_share_mentions_table": "true",
                        "definition_labels": "reimbursement rate",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Policy Schedule\nThe Policy Schedule attached to this Policy sets out coverage details and reimbursement selections.",
                    metadata={
                        "provider": "one_degree",
                        "source_path": "one_degree/policy_schedule.md",
                        "source_name": "one_degree.md",
                        "chunk_index": "2",
                        "unit_types": "definition",
                        "clauses": "Definition",
                        "section_path": "Policy > Definitions",
                        "topic_tags": "definition, policy_schedule",
                        "cost_share_kind": "",
                        "cost_share_evidence": "table_reference",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "policy_schedule",
                        "cost_share_mentions_table": "true",
                        "definition_labels": "policy schedule",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected],
            ["one_degree/reimbursement_ratio.md", "one_degree/policy_schedule.md"],
        )

    def test_select_cost_share_answer_nodes_prefers_reimbursement_rate_definition_over_coverage_clause(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the OneDegree reimbursement ratio?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### Definition: Reimbursement Rate\nThe percentage of the amount that We will reimburse You for the treatments for covered Medical Conditions.",
                    metadata={
                        "provider": "one_degree",
                        "source_path": "one_degree/reimbursement_rate_definition.md",
                        "source_name": "one_degree.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "7",
                        "section_path": "Pet CEO Plan Insurance Policy > Section C: Important Notes About Your Policy > 7. Definitions",
                        "topic_tags": "definition, cost_share, cost_share_reimbursement_ratio, policy_schedule",
                        "cost_share_kind": "reimbursement_ratio",
                        "cost_share_evidence": "definition",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "",
                        "cost_share_mentions_table": "false",
                        "definition_labels": "reimbursement rate",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="We will reimburse Your expenses in any covered medical treatments up to the Annual Limit and Reimbursement Rate stated in Your Policy Schedule, provided that:",
                    metadata={
                        "provider": "one_degree",
                        "source_path": "one_degree/coverage_definition.md",
                        "source_name": "one_degree.md",
                        "chunk_index": "2",
                        "unit_types": "coverage_definition",
                        "clauses": "1.2",
                        "section_path": "Pet CEO Plan Insurance Policy > Section A: What You Get From Your Cover > 1. What Your Policy Covers",
                        "topic_tags": "cost_share, policy_schedule",
                        "cost_share_kind": "reimbursement_ratio",
                        "cost_share_evidence": "table_reference",
                        "cost_share_has_numeric": "false",
                        "cost_share_value_type": "",
                        "cost_share_value_dependencies": "policy_schedule",
                        "cost_share_mentions_table": "true",
                        "definition_labels": "",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected][:1],
            ["one_degree/reimbursement_rate_definition.md"],
        )

    def test_select_cost_share_answer_nodes_prefers_bluecross_deductible_over_coinsurance_for_zh_query(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 的自付額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### 定義：共同保險\n指保單持有人在支付每個保單年度的符合索償資格的費用後（如有），必須按保障項目表第一及第四部分的比率分擔的合資格費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/coinsurance.md",
                        "chunk_index": "1",
                        "unit_types": "definition",
                        "clauses": "3",
                        "section_path": "LovePet 寵物保險 — 條款及細則 > 釋義",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### 適用於第二部分之不保事項\n- 每次及每項索償之首港幣3,000元（自付額）",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/deductible.md",
                        "chunk_index": "2",
                        "unit_types": "exclusion",
                        "clauses": "Section 2 Exclusions",
                        "section_path": "LovePet 寵物保險 — 條款及細則 > 保障條款 > 第二部分",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["bluecross/deductible.md"])

    def test_select_cost_share_answer_nodes_ignores_excess_of_limit_false_positive(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the Prudential deductible?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        '#### D. Chemotherapy and Heart Diseases Treatment Benefit\n'
                        'The Company will indemnify the Insured against the incurred additional cost under this Section 1-D Chemotherapy and Heart Diseases Treatment Benefit in excess of the maximum limit per visit under Section 1-A.vi.\n'
                        'Plan A: HK$5,000 per year (HK$2,500 per visit).'
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/1d.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.D",
                        "section_path": 'PRUCHOICE FURKID CARE Insurance Policy > Benefits > Section 1 – "PAWcare" Medical Expenses',
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text='### Section 2 – "PAWhaviour" Third Party Legal Liability\nAll claims under Section 2 is subject to an excess of HK$3,000 per claim.',
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/section2.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "Section 2",
                        "section_path": "PRUCHOICE FURKID CARE Insurance Policy > Benefits",
                        "topic_tags": "benefit, cost_share, cost_share_deductible",
                        "cost_share_kind": "deductible",
                        "cost_share_evidence": "benefit",
                        "cost_share_has_numeric": "true",
                        "cost_share_value_type": "hkd_amount",
                        "cost_share_mentions_table": "false",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["prudential/section2.md"])

    def test_select_cost_share_answer_nodes_keeps_multiple_distinct_deductibles(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text='### 項目一 -「衛您寵」醫療費用保障\n自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。',
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/section1.md",
                        "source_name": "prudential.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目一 -「衛您寵」醫療費用保障",
                        "topic_tags": "cost_share, cost_share_deductible",
                        "cost_share_kind": "deductible",
                        "cost_share_evidence": "benefit",
                        "cost_share_has_numeric": "true",
                        "cost_share_value_type": "percentage",
                        "cost_share_mentions_table": "false",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text='### 項目二 - 「寵有責」第三者法律責任保障\n項目二所有索償均附設自負額為每宗索償港幣3,000。',
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/section2.md",
                        "source_name": "prudential.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "Section 2",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目二 - 「寵有責」第三者法律責任保障",
                        "topic_tags": "cost_share, cost_share_deductible",
                        "cost_share_kind": "deductible",
                        "cost_share_evidence": "benefit",
                        "cost_share_has_numeric": "true",
                        "cost_share_value_type": "hkd_amount",
                        "cost_share_mentions_table": "false",
                    },
                ),
            ),
        ]

        selected = select_cost_share_answer_nodes(cfg, intent, nodes)
        self.assertEqual(
            [node.node.metadata["source_path"] for node in selected],
            ["prudential/section1.md", "prudential/section2.md"],
        )

    def test_select_answer_nodes_waiting_period_filters_cost_share_tail_chunk(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Prudential 等候期係幾多日？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### 等候期（項目一）\n"
                        "- 疾病（癌症除外）：疾病首次發生應在本保單生效日起的30日等候期終結後。\n"
                        "- 癌症：有關癌症首次發生應在本保單生效日起的180日等候期終結後。\n"
                        "- 身體損傷：有關身體損傷首次發生應在本保單生效日起的7日等候期終結後。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential_policy_zh.md",
                        "source_path": "prudential/waiting_part1.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "topic_tags": "waiting_period",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "#### 等候期（項目一）\n"
                        "- 自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。\n"
                        "項目一總年度賠償上限：計劃A最多港幣35,000，計劃B最多港幣90,000。"
                    ),
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential_policy_zh.md",
                        "source_path": "prudential/waiting_part2.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "topic_tags": "waiting_period",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["prudential/waiting_part1.md"])

    def test_extract_cost_share_fact_prefers_direct_numeric_clause(self) -> None:
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="本公司不會負責在承保表中列明為自負額條款內的自負額金額。",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/general_exclusions.md",
                        "chunk_index": "1",
                        "unit_types": "exclusion",
                        "clauses": "General Exclusions",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 一般不保項目",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/waiting_periods.md",
                        "chunk_index": "2",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 等候期（項目一）",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        fact = extract_cost_share_fact(intent, nodes)
        self.assertIsNotNone(fact)
        self.assertEqual(fact.clauses, "Waiting Periods")
        self.assertEqual(fact.value, "30%")

    def test_extract_cost_share_fact_collection_keeps_multiple_prudential_deductibles(self) -> None:
        intent = detect_query_intent("Prudential 自負額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### 項目一 -「衛您寵」醫療費用保障\n自負額：項目一 -「衛您寵」醫療費用保障的所有索償均附設30%自負額。",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/waiting_periods.md",
                        "chunk_index": "1",
                        "unit_types": "waiting_period",
                        "clauses": "Waiting Periods",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目一 -「衛您寵」醫療費用保障",
                        "topic_tags": "",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="### 項目二 - 「寵有責」第三者法律責任保障\n項目二所有索償均附設自負額為每宗索償港幣3,000。",
                    metadata={
                        "provider": "prudential",
                        "source_name": "prudential.md",
                        "source_path": "prudential/section2.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "Section 2",
                        "section_path": "保誠精選「寵愛寶」寵物保障保單 > 保障項目 > 項目二 - 「寵有責」第三者法律責任保障",
                        "topic_tags": "",
                    },
                ),
            ),
        ]

        facts = collect_cost_share_facts(intent, nodes)
        self.assertEqual(len(facts), 2)
        self.assertEqual({fact.value for fact in facts}, {"30%", "港幣3,000"})

    def test_select_generic_limit_answer_nodes_prefers_matching_benefit_heading(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the annual limit for Prudential room and board?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### D. Chemotherapy and Heart Diseases Treatment Benefit\nPlan A: HK$5,000 per year (HK$2,500 per visit).\nPlan B: HK$10,000 per year (HK$5,000 per visit).",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/1d.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.D",
                        "section_path": "Policy > Benefits",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### B. Room and Board\nPlan A: HK$3,500 per year (HK$250 per day).\nPlan B: HK$7,000 per year (HK$500 per day).",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/1b.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "section_path": "Policy > Benefits",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### Section 4B – Furry Kid Owners' Travel Delay Support\nSection 4 total annual limit: Plan A: HK$1,500 per year (HK$250 per day, max 6 days). Plan B: HK$3,000 per year (HK$500 per day, max 6 days).",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/4b.md",
                        "chunk_index": "3",
                        "unit_types": "benefit",
                        "clauses": "4B",
                        "section_path": "Policy > Benefits",
                        "topic_tags": "benefit",
                    },
                ),
            ),
        ]

        selected = select_generic_limit_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["clauses"], "1.B")

    def test_select_generic_limit_answer_nodes_prefers_bluecross_hospitalisation_heading(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Blue Cross 住院費用最高賠償額係幾多？")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### 第四部分 緊急寄宿\n如保單持有人於保單生效期間住院多於連續4天，本公司將支付必要的寵物托管費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/4.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "Section 4",
                        "section_path": "LovePet 寵物保險 — 條款及細則 > 保障條款",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### B) 住院費用\n本公司將支付受保寵物於受保期內因疾病或受傷而需於獸醫診所住院不少於連續12小時之費用。",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/1b.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "section_path": "LovePet 寵物保險 — 條款及細則 > 保障條款 > 第一部分 醫療保障",
                        "topic_tags": "benefit",
                    },
                ),
            ),
        ]

        selected = select_generic_limit_answer_nodes(cfg, intent, nodes)
        self.assertEqual(selected[0].node.metadata["clauses"], "1.B")

    def test_select_answer_nodes_for_limit_comparison_prefers_limit_scored_provider_nodes(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("Compare Blue Cross and Prudential room and board annual limits.")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### B. Room and Board\nThe Company shall cover the Insured Pet for confinement of no less than 12 consecutive hours.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/a.md",
                        "chunk_index": "1",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "benefit_labels": "room and board, hospitalisation",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### Z. Emergency Boarding\nPlan A: HK$1,000 per year.",
                    metadata={
                        "provider": "bluecross",
                        "source_path": "bluecross/z.md",
                        "chunk_index": "2",
                        "unit_types": "benefit",
                        "clauses": "1.Z",
                        "benefit_labels": "emergency boarding",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text="#### B. Room and Board\nPlan A: HK$3,500 per year.\nPlan B: HK$7,000 per year.",
                    metadata={
                        "provider": "prudential",
                        "source_path": "prudential/a.md",
                        "chunk_index": "3",
                        "unit_types": "benefit",
                        "clauses": "1.B",
                        "benefit_labels": "room and board, hospitalisation",
                        "topic_tags": "benefit",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["provider"] for node in selected], ["bluecross", "prudential"])
        self.assertEqual([node.node.metadata["clauses"] for node in selected], ["1.B", "1.B"])

    def test_select_answer_nodes_for_limit_query_prefers_benefit_table_over_exclusion_noise(self) -> None:
        cfg = PrototypeConfig.load()
        intent = detect_query_intent("What is the MSIG annual limit for hospitalisation?")
        nodes = [
            SimpleNamespace(
                score=0.0,
                node=SimpleNamespace(
                    text=(
                        "### Benefit Matrix Summary\n"
                        "The Table of Benefits contains plan-specific maximum limits for:\n"
                        "- Section I Surgical Benefit, including Clinical and Surgical Benefit, Room and Board Expenses, and Post Surgical Treatment Benefit;\n"
                        "- Section II Chemotherapy;\n"
                        "- Section III Final Expenses Benefit;\n"
                        "- Total Annual Coverage for Sections I to III.\n"
                    ),
                    metadata={
                        "provider": "msig",
                        "source_path": "msig/benefit_table.md",
                        "chunk_index": "1",
                        "unit_types": "benefit_table",
                        "clauses": "Table of Benefits",
                        "section_path": "Policy > Table of Benefits",
                    },
                ),
            ),
            SimpleNamespace(
                score=2.7,
                node=SimpleNamespace(
                    text="#### Conduct, Procedure and Condition Exclusions\n- experimental or investigational treatment or medicine;",
                    metadata={
                        "provider": "msig",
                        "source_path": "msig/exclusion.md",
                        "chunk_index": "2",
                        "unit_types": "exclusion",
                        "clauses": "General Exclusions.4",
                        "section_path": "Policy > General Exclusions",
                    },
                ),
            ),
        ]

        selected = select_answer_nodes(cfg, intent, nodes)
        self.assertEqual([node.node.metadata["source_path"] for node in selected], ["msig/benefit_table.md"])

    def test_generic_limit_score_uses_benefit_labels_for_hospitalisation_query(self) -> None:
        intent = detect_query_intent("What is the annual limit for hospitalisation?")
        matching = SimpleNamespace(
            score=0.0,
            node=SimpleNamespace(
                text="#### B. Room and Board\nWe shall cover boarding cost.",
                metadata={
                    "unit_types": "benefit",
                    "clauses": "1.B",
                    "benefit_labels": "room and board, hospitalisation, hospitalization",
                },
            ),
        )
        non_matching = SimpleNamespace(
            score=0.0,
            node=SimpleNamespace(
                text="#### Section 4 Emergency Boarding\nWe shall cover pet sitting cost.",
                metadata={
                    "unit_types": "benefit",
                    "clauses": "Section 4",
                    "benefit_labels": "emergency boarding, pet sitting",
                },
            ),
        )

        self.assertGreater(generic_limit_answer_score(intent, matching), generic_limit_answer_score(intent, non_matching))
        self.assertGreater(
            select_generic_limit_answer_nodes(PrototypeConfig.load(), intent, [matching, non_matching])[0].node.metadata["clauses"],
            ""
        )
        self.assertEqual(
            select_generic_limit_answer_nodes(PrototypeConfig.load(), intent, [matching, non_matching])[0].node.metadata["clauses"],
            "1.B",
        )

    def test_answer_result_mode_can_follow_structured_type(self) -> None:
        result = AnswerResult(
            text="x",
            nodes=[],
            mode="deterministic_pre_existing_single",
            structured={"type": "pre_existing_single"},
        )
        self.assertEqual(result.mode, "deterministic_pre_existing_single")

    def test_build_source_payload_maps_expected_fields(self) -> None:
        node = SimpleNamespace(
            score=1.25,
            node=SimpleNamespace(
                text="Example snippet\nwith newline.",
                metadata={
                    "provider": "prudential",
                    "language": "en",
                    "product": "pruchoice_furkid_care",
                    "source_name": "prudential_policy_en.md",
                    "section_path": "Policy > Benefits",
                    "clauses": "1.B",
                    "unit_types": "benefit",
                    "topic_tags": "benefit, illness",
                },
            ),
        )

        payload = build_source_payload(node)

        self.assertEqual(payload["provider"], "prudential")
        self.assertEqual(payload["language"], "en")
        self.assertEqual(payload["product"], "pruchoice_furkid_care")
        self.assertEqual(payload["source_name"], "prudential_policy_en.md")
        self.assertEqual(payload["section_path"], "Policy > Benefits")
        self.assertEqual(payload["clauses"], "1.B")
        self.assertEqual(payload["unit_types"], "benefit")
        self.assertEqual(payload["topic_tags"], "benefit, illness")
        self.assertEqual(payload["score"], 1.25)
        self.assertEqual(payload["snippet"], "Example snippet\nwith newline.")

    def test_build_query_payload_uses_max_sources_and_preserves_structured_answer(self) -> None:
        nodes = [
            SimpleNamespace(
                score=0.9,
                node=SimpleNamespace(
                    text="First source",
                    metadata={
                        "provider": "prudential",
                        "language": "en",
                        "product": "pruchoice_furkid_care",
                        "source_name": "prudential_policy_en.md",
                        "section_path": "Policy > Benefits",
                        "clauses": "1.B",
                        "unit_types": "benefit",
                        "topic_tags": "benefit",
                    },
                ),
            ),
            SimpleNamespace(
                score=0.4,
                node=SimpleNamespace(
                    text="Second source",
                    metadata={
                        "provider": "prudential",
                        "language": "en",
                        "product": "pruchoice_furkid_care",
                        "source_name": "prudential_policy_en.md",
                        "section_path": "Policy > Benefits",
                        "clauses": "1.B",
                        "unit_types": "benefit",
                        "topic_tags": "benefit, illness",
                    },
                ),
            ),
        ]
        result = AnswerResult(
            text="answer",
            nodes=nodes,
            mode="deterministic_benefit_limit_single",
            structured={"type": "benefit_limit_single", "clauses": "1.B"},
        )

        payload = build_query_payload(
            question="What is the annual limit for Prudential room and board?",
            provider="prudential",
            language="en",
            intent="limit, providers=prudential",
            result=result,
            max_sources=1,
            processing_ms=42,
        )

        self.assertEqual(payload["question"], "What is the annual limit for Prudential room and board?")
        self.assertEqual(payload["provider"], "prudential")
        self.assertEqual(payload["language"], "en")
        self.assertEqual(payload["intent"], "limit, providers=prudential")
        self.assertEqual(payload["answer"], "answer")
        self.assertEqual(payload["answer_mode"], "deterministic_benefit_limit_single")
        self.assertEqual(payload["structured_answer"], {"type": "benefit_limit_single", "clauses": "1.B"})
        self.assertTrue(payload["disclaimer"])
        self.assertEqual(payload["processing_ms"], 42)
        self.assertEqual(len(payload["sources"]), 1)
        self.assertEqual(payload["sources"][0]["snippet"], "First source")

    def test_validate_provider_accepts_known_values_and_normalizes_case(self) -> None:
        self.assertEqual(validate_provider("PrUdEnTiAl"), "prudential")
        self.assertEqual(validate_provider(" one_degree "), "one_degree")
        self.assertIsNone(validate_provider(None))

    def test_validate_provider_rejects_unknown_values(self) -> None:
        with self.assertRaises(RequestValidationError) as exc:
            validate_provider("bolttech")
        self.assertEqual(exc.exception.code, "invalid_provider")
        payload = build_validation_error_payload(exc.exception)
        self.assertEqual(payload["error"], "invalid_provider")
        self.assertIn("supported_providers", payload)

    def test_validate_language_accepts_known_values_and_normalizes_case(self) -> None:
        self.assertEqual(validate_language("EN"), "en")
        self.assertEqual(validate_language(" zh "), "zh")
        self.assertIsNone(validate_language(None))

    def test_validate_language_rejects_unknown_values(self) -> None:
        with self.assertRaises(RequestValidationError) as exc:
            validate_language("fr")
        self.assertEqual(exc.exception.code, "invalid_language")
        payload = build_validation_error_payload(exc.exception)
        self.assertEqual(payload["error"], "invalid_language")
        self.assertIn("supported_languages", payload)

    def test_validate_max_sources_accepts_valid_values_and_defaults(self) -> None:
        self.assertEqual(validate_max_sources(None, default_max_sources=6, max_allowed=6), 6)
        self.assertEqual(validate_max_sources("2", default_max_sources=6, max_allowed=6), 2)

    def test_validate_max_sources_rejects_invalid_values(self) -> None:
        with self.assertRaises(RequestValidationError) as exc:
            validate_max_sources("0", default_max_sources=6, max_allowed=6)
        self.assertEqual(exc.exception.code, "invalid_max_sources")
        payload = build_validation_error_payload(exc.exception, max_allowed_sources=6)
        self.assertEqual(payload["error"], "invalid_max_sources")
        self.assertEqual(payload["max_allowed_sources"], 6)

    def test_read_index_metadata_returns_empty_dict_for_missing_or_invalid_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            missing = read_index_metadata(Path(tmp))
            self.assertEqual(missing, {})

            bad_dir = Path(tmp) / "bad"
            bad_dir.mkdir()
            (bad_dir / "prototype_index_meta.json").write_text("{not-json", encoding="utf-8")
            invalid = read_index_metadata(bad_dir)
            self.assertEqual(invalid, {})

    def test_compute_corpus_fingerprint_changes_when_markdown_changes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            first = root / "a.md"
            second = root / "README.md"
            first.write_text("# A\nhello\n", encoding="utf-8")
            second.write_text("ignore me", encoding="utf-8")

            initial = compute_corpus_fingerprint(root)
            self.assertTrue(initial)

            first.write_text("# A\nhello world\n", encoding="utf-8")
            updated = compute_corpus_fingerprint(root)
            self.assertNotEqual(initial, updated)

    def test_index_exists_rejects_stale_corpus_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            data_path = root / "data"
            data_path.mkdir()
            (data_path / "policy.md").write_text("# Policy\ncontent\n", encoding="utf-8")

            persist_dir = root / "persist"
            persist_dir.mkdir()
            (persist_dir / "docstore.json").write_text("{}", encoding="utf-8")

            cfg = PrototypeConfig.load()
            cfg.data_path = data_path
            cfg.persist_dir = persist_dir
            cfg.chunk_size = 700
            cfg.chunk_overlap = 120

            write_index_metadata(cfg, document_count=1)
            self.assertTrue(index_exists(cfg))

            (data_path / "policy.md").write_text("# Policy\nchanged content\n", encoding="utf-8")
            self.assertFalse(index_exists(cfg))

    def test_build_capabilities_payload_exposes_service_boundaries(self) -> None:
        cfg = PrototypeConfig.load()
        payload = build_capabilities_payload(
            cfg,
            {
                "built_at_utc": "2026-05-29T12:00:00+00:00",
                "chunker_version": "semantic-v9",
                "document_count": 582,
                "chunk_size": 700,
                "chunk_overlap": 120,
                "data_path": "/tmp/rag_data",
                "corpus_fingerprint": "stale-fingerprint",
                "source_markdown_file_count": 8,
                "supported_provider_count": 4,
            },
        )

        self.assertTrue(payload["ok"])
        self.assertEqual(payload["service"], "llamaindex_rag_prototype")
        self.assertEqual(payload["supported_providers"], ["one_degree", "bluecross", "msig", "prudential"])
        self.assertEqual(payload["supported_languages"], ["en", "zh"])
        self.assertEqual(payload["default_max_sources"], cfg.answer_max_sources)
        self.assertEqual(payload["max_allowed_sources"], cfg.answer_max_sources)
        self.assertEqual(payload["query_methods"], ["GET", "POST"])
        self.assertEqual(payload["query_routes"]["capabilities"], "/capabilities")
        self.assertEqual(payload["query_routes"]["healthz"], "/healthz")
        self.assertEqual(payload["query_routes"]["readyz"], "/readyz")
        self.assertEqual(payload["index"]["built_at_utc"], "2026-05-29T12:00:00+00:00")
        self.assertFalse(payload["index"]["is_fresh"])
        self.assertEqual(payload["index"]["chunker_version"], "semantic-v9")
        self.assertEqual(payload["index"]["document_count"], 582)
        self.assertEqual(payload["index"]["chunk_size"], 700)
        self.assertEqual(payload["index"]["chunk_overlap"], 120)
        self.assertEqual(payload["index"]["data_path"], "/tmp/rag_data")
        self.assertEqual(payload["index"]["source_markdown_file_count"], 8)
        self.assertEqual(payload["index"]["supported_provider_count"], 4)

    def test_rag_handler_runtime_state_is_lazy(self) -> None:
        original = RAGHandler._runtime_state
        try:
            RAGHandler._runtime_state = None
            self.assertIsNone(RAGHandler._runtime_state)
            self.assertFalse(RAGHandler.runtime_loaded())
            RAGHandler._runtime_state = {"cfg": object(), "index": object(), "index_metadata": {}}
            self.assertTrue(RAGHandler.runtime_loaded())
        finally:
            RAGHandler._runtime_state = original


if __name__ == "__main__":
    unittest.main()
