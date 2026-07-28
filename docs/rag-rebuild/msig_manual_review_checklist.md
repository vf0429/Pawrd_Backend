# MSIG Manual Review Checklist

## Status

`assets/rag_normalized/hk_insurance/MSIG/` is now close to a manual-review state. The normalized English and Chinese markdown have been rebuilt against the source PDF at clause level instead of summary level, with major recovery across definitions, benefits, liability rules, no-claim discount, general conditions, claims conditions, general exclusions, and limitations.

## Items To Manually Review In PDF

1. Chinese policy wording density across Definitions on `p1-p2`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`

Reason:
- Definitions are now materially restored, but the Chinese PDF is an unofficial translation note plus dense quoted-term style.
- Manual review should confirm whether any defined-term phrasing should be made more literal for quote-sensitive retrieval.

Highest-priority definitions:
- `醫學上必要的`
- `已存在的情況`
- `用品`
- `外科手術`
- `有工作的寵物`

2. Section IV Chinese liability wording on `p2-p3`

File:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Lines to inspect:
- `就於初審時非由「香港」具司法管轄權的法庭發出或頒令的裁決...`
- `多寵物 / 多保單上限`

Reason:
- The operational meaning is preserved.
- The source Chinese sentence structure is dense and court-jurisdiction wording is easy to over-normalize.

3. General Conditions clauses 8-13 on `p3`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Reason:
- These clauses are legally loaded and now much fuller than before.
- Manual review should confirm there is no over-compression in misrepresentation, alteration, cancellation, other-insurance, and trust/assignment wording.

4. Claims arbitration wording on `p4`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Reason:
- The arbitration clause includes procedural detail, condition-precedent wording, and a 12-month abandonment rule.
- This is important for legal-fact retrieval and should be visually checked against the PDF.

5. General Exclusions clause 4 detail density on `p4-p5`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Reason:
- Clause 4 is the longest and most retrieval-critical block.
- It now includes preventive-care failure, specific-activity recurrence, diagnostic-test carve-outs, alternative therapy exclusions, and out-of-hours surgery cost exclusions.
- Manual review should confirm no bullet was merged or split in a way that changes legal scope.

6. War / sanctions legal boilerplate on `p4-p5`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Reason:
- These clauses are semantically preserved but still normalized for readability.
- If RAG use later requires quote-like legal output, these may need an even more literal pass.

7. Limitation 4 post-spay/neuter update process on `p5`

Files:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`

Reason:
- This clause contains a useful operational rule: post-commencement spay/neuter can update records if no claim has been made.
- Worth confirming word-for-word because it may affect downstream claim-answer logic.

## Current Confidence

- Structure and clause mapping: high
- Benefits Sections I-IV: high
- No Claim Discount: high
- General Conditions: medium-high
- Claims Conditions: medium-high
- General Exclusions clause coverage: medium-high
- Chinese legal-style wording fidelity: medium
