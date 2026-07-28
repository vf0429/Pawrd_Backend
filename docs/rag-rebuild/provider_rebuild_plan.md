# Provider Rebuild Plan v1

## Scope
This document defines the first-pass normalized reconstruction strategy for:

- OneDegree
- Blue Cross
- MSIG
- Prudential

The goal is not to optimize retrieval parameters yet. The goal is to create a clean corpus layer that later supports provider-aware chunking.

## OneDegree

### Source of truth
- English PDF: `assets/rag/hk_insurance/one_degree/onedegree_policy.pdf`
- Chinese PDF: `assets/rag/hk_insurance/one_degree/onedegree_policy_zh.pdf`

### Current corpus problems
- Legacy markdown has already been human-rewritten into a friendlier structure.
- It introduces summary labels and emphasis markers not reliably traceable to source PDF headings.
- Some benefit logic is broken into editorial subsections rather than official clause boundaries.
- This makes later chunking unstable because chunks may reflect editor choices rather than policy structure.

### Official structure observed from PDF
- Cover and notes
- TOC
- `Section A: What You Get From Your Cover`
- `1. What Your Policy Covers`
  - `1.1 Covered Medical Conditions`
  - `1.2 Covered medical expenses`
  - `1.3 Cancer cash benefit`
  - `1.4 Critical illness cash benefit`
  - `1.5 Feline infectious peritonitis additional coverage`
  - `1.6 Advanced Critical illness cash benefit`
- `2. What Your Policy Does Not Cover`
  - `2.1 Waiting Period`
  - `2.2 Pre-existing Medical Conditions`
  - `2.3 Excluded Medical Conditions`
  - `2.4 Excluded treatment and care`
- `Section B: How Your Cover Works`
- `Section C: Important Notes About Your Policy`
- `7. Definitions`

### Reconstruction strategy
1. Build the normalized file from the PDF clause tree, not from the old markdown.
2. Exclude cover page, thank-you page, and table of contents from the retrieval body.
3. Retain official `Section A/B/C` and clause numbers as the primary heading hierarchy.
4. Inside dense clauses, create normalized subunits only when they reflect distinct semantic retrieval targets:
   - benefit trigger
   - eligibility condition
   - payout rule
   - exclusion
   - cross-reference rule
5. For `1.1 Covered Medical Conditions`, keep these as separate units:
   - injuries
   - illness
   - chronic medical conditions list
   - chronic condition coverage rule for pets age `<= 4`
   - chronic condition limitation rule for pets age `>= 5`
6. For `1.2 Covered medical expenses`, split by benefit type:
   - surgery
   - overnight hospitalisation
   - imaging test
   - laboratory test
   - prescribed medication
   - general consultation
   - specialist consultation
7. For cash-benefit clauses like `1.3`, normalize into:
   - trigger
   - amount/payout mechanics
   - eligibility
   - exclusions
8. Keep `2.x` exclusions separate from `1.x` benefits even when they relate closely; later chunking can link them via metadata.
9. Reconstruct English first. Keep Chinese as a parallel normalized file with the same clause map.

### First output files
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

### Chunking signals to preserve
- section letter: `A`, `B`, `C`
- clause number: `1.1`, `1.2`, `2.1`, `4.6`, etc.
- unit type: benefit, exclusion, definition, waiting_period, eligibility, renewal_rule
- special policy dimensions:
  - age threshold
  - chronic condition status
  - cash benefit vs reimbursement benefit
  - upgrade/downgrade dependency

### Key risks
- OneDegree wording spans page breaks inside the same condition block; normalization must not split meaning at page boundaries.
- Legacy eval references point to old line numbers and will need remapping later.

## Blue Cross

### Source of truth
- PDF: `assets/rag/hk_insurance/bluecross/LovePet_Insurance_TnC.pdf`

### Current corpus problems
- Legacy markdown is closer to the source than OneDegree but still flattens the source structure.
- The PDF is bilingual, while the current markdown effectively selects an English stream without explicitly modeling that choice.
- Definitions, benefits, exclusions, and conditions are all retrievable targets, but they are not yet normalized into stable unit types.

### Official structure observed from PDF
- Insuring Clause
- Territorial Limit
- Definitions
- Benefit Provisions
  - `Section 1 Medical Coverage`
  - `Section 2 Third Party Liability`
  - `Section 3 Funeral Service Expenses`
  - `Section 4 Emergency Boarding`
  - `Section 5 Overseas Cover`
- General Exclusions
- General Conditions

### Reconstruction strategy
1. Produce an English normalized file first from the English text stream of the bilingual PDF.
2. Do not keep both languages in the same normalized file.
3. Preserve the contractual ordering because Blue Cross defines terms before benefits.
4. Split `Definitions` into one subsection per defined term.
5. For `Section 1 Medical Coverage`, split by official benefit item:
   - Clinical and Surgical Expenses
   - Room and Board
   - Veterinary Consultation
   - Chemotherapy Benefit
   - Behavioural Treatment Expenses
6. Keep `Exclusions Applicable to SECTION 1` as a dedicated exclusion block owned by Section 1.
7. Repeat the same pattern for Sections 2-5:
   - section heading
   - coverage block
   - section-specific exclusions block
8. Keep `General Exclusions` as its own top-level section because it affects all benefits and should be retrievable independently.
9. Keep `Territorial Limit` and `Overseas Cover` separate; they overlap conceptually but are not the same clause.

### First output files
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_en_pcp-05-2024.md`

### Chunking signals to preserve
- clause family: insuring_clause, territorial_limit, definition, section_1, general_exclusion, general_condition
- benefit type:
  - medical
  - liability
  - funeral
  - boarding
  - overseas_extension
- waiting period category:
  - cancer_or_chronic_renal_disease
  - injury
  - other_conditions

### Key risks
- PDF extraction may interleave bilingual fragments if the extraction method changes; reconstruction should stick to a verified extraction workflow.
- Blue Cross uses lists and inline legal phrases that can be damaged by aggressive bullet normalization.

## MSIG

### Source of truth
- PDF (external canonical source): `https://hk.aonhappytails.com/getmedia/d5e3dd2c-ef4c-4998-b76e-2ed3d89b3ee0/PETBR0A_protected.pdf`
- Legacy local markdown references:
  - `assets/rag/hk_insurance/MSIG/PETBR0A_policy_en.md`
  - `assets/rag/hk_insurance/MSIG/PETBR0A_policy_zh.md`

### Current corpus problems
- Existing `PETBR0A_policy_en.md` and `PETBR0A_policy_zh.md` are already "structured/organized editions", not guaranteed verbatim policy transcripts.
- The markdown explicitly points to an external source file path outside repo (`/Users/vfzzz/Desktop/PETBR0A_protected.pdf`), so provenance is not stable inside repository context.
- There are no local MSIG raw PDFs under `assets/rag/hk_insurance/MSIG/`, which blocks reproducible PDF-first rebuild unless we store a canonical copy or stable fetch rule.

### Official structure observed from source PDF
- Policy heading and insuring agreement
- Definitions
- Benefit sections:
  - `SECTION I SURGICAL BENEFIT`
  - `SECTION II CHEMOTHERAPY BENEFIT`
  - `SECTION III FINAL EXPENSES BENEFIT`
  - `SECTION IV THIRD PARTY LEGAL LIABILITY BENEFIT`
- `NO CLAIM DISCOUNT`
- `GENERAL CONDITIONS`
- `CLAIMS CONDITIONS`
- `GENERAL EXCLUSIONS`
- `LIMITATION` and privacy appendix content

### Reconstruction strategy
1. Treat the external PDF as source of truth and transcribe from PDF, not from existing markdown.
2. Create a reproducible source reference path in front matter, including the stable URL and retrieval date.
3. Preserve MSIG's section identity (`SECTION I` to `SECTION IV`) as the main benefit hierarchy.
4. Split each section into stable retrieval units:
   - coverage trigger
   - reimbursable items
   - section-specific exclusions
   - territorial/legal scope
5. Keep `NO CLAIM DISCOUNT` as a standalone rule section because it is a common Q&A target.
6. Keep `GENERAL CONDITIONS`, `CLAIMS CONDITIONS`, and `GENERAL EXCLUSIONS` as separate top-level sections.
7. Separate English and Chinese outputs; do not mix bilingual streams in one file.
8. If local canonical PDF copy is added later, keep URL and local file path both in front matter to preserve provenance.

### First output files
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

### Chunking signals to preserve
- provider section id: `section_i`, `section_ii`, `section_iii`, `section_iv`
- unit type: benefit, exclusion, claims_rule, ncd_rule, general_condition, definition
- payout mode:
  - reimbursement
  - liability indemnity
  - final-expense special handling
- waiting/limitation dimensions:
  - illness waiting period
  - hereditary/congenital limitation window
  - pre-existing condition linkage

### Key risks
- External source URL availability risk: if link changes, rebuild repeatability drops unless local archival copy exists.
- Existing legacy markdown may have merged bullet lists and paraphrase artifacts; avoid inheriting those structures into normalized files.

## Prudential

### Source of truth
- PDF: `assets/rag/hk_insurance/prudential/PRUChoice_Furkid_Care_Policy_0324.pdf`

### Current corpus problems
- The normalized markdown already exists, but Prudential is not yet consistently represented in the rebuild control documents.
- This creates drift between the actual supported-provider set and the planning/review workflow.
- Prudential contains several dense legal sections where over-normalization risk is higher than in the benefit summaries:
  - claims procedure
  - arbitration / jurisdiction
  - sanctions / terrorism / asbestos exclusions
  - eligibility conditions around age, microchip, and residency

### Official structure observed from source PDF
- Insuring Clause
- Benefits
  - `Section 1 "PAWcare" Medical Expenses`
  - `Section 2 "PAWhaviour" Third Party Legal Liability`
  - `Section 3 "PAWradise" Funeral Expenses`
  - `Section 4 "PAWcation" Emergency Pet Sitting Care`
- Section-specific exceptions
- General Exceptions
- Definitions
- Conditions
  - `I` to `XXIII`

### Reconstruction strategy
1. Treat the local Prudential PDF as the source of truth and keep the normalized markdown PDF-first.
2. Preserve the four benefit sections as the main coverage hierarchy before moving into exclusions, definitions, and conditions.
3. Keep Section 1 waiting periods as a dedicated normalized unit because they are a frequent retrieval target.
4. Keep section-specific exceptions separate from `General Exceptions`.
5. Split `Definitions` into one retrievable subsection per defined term.
6. Keep `Conditions` clause-by-clause from `I` to `XXIII`, with special care around:
   - eligibility
   - claims handling
   - renewal / termination
   - arbitration / jurisdiction
7. Preserve English and Chinese as separate normalized files with their own page anchors.

### First output files
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_en.md`
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

### Chunking signals to preserve
- provider section id: `section_1`, `section_2`, `section_3`, `section_4`
- unit type: benefit, waiting_period, exclusion, definition, eligibility, claim_rule, renewal_rule
- payout mode:
  - reimbursement
  - liability indemnity
  - funeral-expense reimbursement
  - pet-sitting reimbursement
- policy dimensions:
  - co-payment
  - plan A vs plan B limits
  - dog microchip requirement
  - cat identity-proof requirement

### Key risks
- General Exceptions and Conditions contain long legal prose where wording compression can accidentally narrow or widen scope.
- English and Chinese page labels differ materially (`p1-p5` vs `p6-p11`), so anchor consistency must be checked per language rather than assumed symmetrical.

## Cross-Provider Rules
- Use the same front matter schema for all providers.
- Preserve provider-specific clause numbering instead of forcing synthetic global numbering.
- Normalize unit types, not wording style.
- Keep exclusions near their owning sections in structure, while still preserving top-level general exclusions.

## What comes next after this plan
1. Finish provider-by-provider proofreading and manual review for OneDegree, Blue Cross, MSIG, and Prudential.
2. Add a schema-aware loader that can parse front matter and heading/unit metadata.
3. Define provider-aware chunk boundaries from normalized units.
4. Only then evaluate whether reranking is still needed.
