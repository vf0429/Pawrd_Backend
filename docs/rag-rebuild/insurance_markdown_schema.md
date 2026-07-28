# Insurance Markdown Schema v1

## Purpose
This schema defines a normalized, provider-agnostic markdown format for HK pet insurance policy corpus files used by RAG ingestion. It is designed to:

- preserve contractual meaning from source PDFs
- standardize structure across providers
- support page-anchored citations
- enable provider-aware chunking later
- avoid contaminating retrieval with TOC, promo copy, or manual summaries

## Output Location
Normalized corpus files must live under:

`assets/rag_normalized/hk_insurance/<provider>/`

Legacy corpus under `assets/rag/hk_insurance/` remains unchanged.

## File Naming
One normalized markdown file per provider, language, and policy edition:

`<provider>_<product>_<lang>_<edition>.md`

Examples:

- `one_degree_pet_ceo_plan_en_2025-12-31.md`
- `one_degree_pet_ceo_plan_zh_2025-12-31.md`
- `bluecross_lovepet_en_pcp-05-2024.md`

## Front Matter
Each file begins with YAML front matter:

```yaml
---
schema_version: rag-policy-v1
provider: one_degree
product: pet_ceo_plan
policy_type: pet_insurance
language: en
region: hk
source_format: pdf
source_file: assets/rag/hk_insurance/one_degree/onedegree_policy.pdf
source_version_label: "Applicable to policies purchased on or after 31 December 2025"
source_effective_date: 2025-12-31
source_page_start: 29
source_page_end: 59
normalization_status: draft
normalization_method: manual_pdf_first
contains_bilingual_parallel_text: false
legacy_markdown_reference:
  - assets/rag/hk_insurance/one_degree/one_degree_policy.md
excluded_content_types:
  - table_of_contents
  - marketing_intro
  - duplicate_language_stream
  - manual_summary
---
```

Required fields:

- `schema_version`
- `provider`
- `product`
- `policy_type`
- `language`
- `region`
- `source_file`
- `source_version_label`
- `normalization_status`

Recommended fields:

- `source_effective_date`
- `source_page_start`
- `source_page_end`
- `source_page_reference_scheme`
- `legacy_markdown_reference`
- `excluded_content_types`

### Page reference scheme
`Source: pX` must refer to the page label visible in the source document when such a label exists. This is not necessarily the same as the PDF physical page index.

Recommended values for `source_page_reference_scheme`:

- `printed_label`
- `pdf_physical`

Examples:

- OneDegree English uses `p33-p59` because the visible footer labels are `33 of 59` through `59 of 59`.
- If a source PDF has no visible page labels, use physical PDF pages and set `source_page_reference_scheme: pdf_physical`.

## Structural Rules

### 1. Canonical hierarchy
Use markdown headings to preserve official policy hierarchy:

- `#` document title
- `##` top-level policy section
- `###` numbered clause or subsection
- `####` normalized unit within a clause when needed

Do not invent deeper heading levels unless the source meaning requires it.

### 2. Source anchors
Every major semantic unit must carry a compact source anchor immediately under the heading:

```md
> Source: p34
> Clause: 1.1
```

If the unit spans multiple pages:

```md
> Source: p34-p35
> Clause: 1.1
```

If a provider has no official clause number for a subunit, omit `Clause` and keep the page anchor.

### 3. Allowed content units
Normalized corpus should favor these unit types:

- coverage definition
- benefit item
- eligibility rule
- waiting period rule
- exclusion list
- claim rule
- renewal/cancellation rule
- definition term
- territorial/scope rule

### 4. Disallowed content
Do not include:

- table of contents
- duplicated bilingual stream in the same file
- decorative thank-you text
- manual commentary
- rewritten summary labels not in source
- retrieval-oriented hints like “critical” or “important” unless present in source

### 5. Tables and lists
When the source presents a table, normalize to a repeated section pattern rather than raw tabular OCR when possible.

Example:

```md
#### Injuries
> Source: p34
> Unit: coverage_definition

We cover medical expenses of diagnosed injuries caused by accidents suffered by the pet.

Exclusion:
- We do not cover injuries caused by accidents occurring before the end of the waiting period.
```

### 6. Definitions
Each defined term should become its own normalized subsection so retrieval can hit precise definitions:

```md
### Definition: Waiting Period
> Source: p2
> Clause: 27
> Unit: definition

...
```

### 7. Cross references
Retain contractual cross references from the source in plain text:

```md
Cross reference:
- See Clause 4.6 for upgrade and downgrade coverage calculations.
```

Do not resolve or paraphrase them away.

## Recommended Unit Template

```md
### 1.3 Cancer Cash Benefit
> Source: p38
> Clause: 1.3
> Unit: benefit

#### Benefit Trigger
> Source: p38
> Unit: trigger

...

#### Eligibility
> Source: p38
> Unit: eligibility

...

#### Exclusions
> Source: p38
> Unit: exclusion

...
```

## Chunking-Oriented Metadata Conventions
The schema should support future chunking boundaries without embedding chunk IDs yet. Writers should preserve:

- official clause number
- provider
- product
- language
- source page range
- unit type
- benefit/exclusion/definition identity

These fields can later be parsed into metadata by a schema-aware loader.

## Provider-Aware Chunking Implications
Chunking should prefer:

- one benefit item per chunk
- one waiting-period rule per chunk
- one exclusion block per chunk
- one definition per chunk
- one cross-page conditional rule per chunk if semantically indivisible

Chunking should avoid:

- mixing definitions with benefits
- mixing claims procedures with medical coverage
- mixing multiple benefit sections just because they are short
- splitting age/eligibility conditions away from the benefit they constrain unless linked bidirectionally

## Authoring Rules
- Transcribe from PDF source first.
- Preserve source meaning; only normalize layout.
- Fix OCR whitespace, line wrapping, and bullet continuity.
- Do not merge distinct rules only because they are similar.
- If the source is ambiguous, record it in a short inline note:

```md
> Normalization note: source line-wrap merged from the PDF; wording unchanged.
```

- Keep language-specific files separate.

## Validation Checklist
- Front matter is complete.
- No TOC or intro filler remains.
- Every major unit has a source page anchor.
- Clause numbering matches source.
- Exclusions are kept with their owning section.
- Definitions are split into retrievable units.
- No manual summary headings were invented.
