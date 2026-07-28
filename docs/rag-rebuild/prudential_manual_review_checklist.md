# Prudential Manual Review Checklist

## Status

`assets/rag_normalized/hk_insurance/prudential/` is now close to a manual-review state. The normalized English and Chinese markdown preserve the benefit hierarchy, section-specific exclusions, general exclusions, definitions, and conditions, but Prudential contains several legal-density areas that should be checked visually against the PDF before the provider is treated as fully review-ready.

## Items To Manually Review In PDF

1. General Exceptions legal-density blocks on `p2-p5` and `p8-p9`

Files:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_en.md`
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

Reason:
- These blocks contain sanctions, terrorism, asbestos, data/software, and nuclear/war wording.
- Manual review should confirm no long exclusion sentence was compressed in a way that changes scope.

2. Claims, jurisdiction, and arbitration wording on `p4-p5` and `p10-p11`

Files:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_en.md`
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

Reason:
- Clauses `XII`, `XX`, and `XXI` are likely future Q&A targets and are legally sensitive.
- Manual review should confirm deadlines, abandonment rules, English-language arbitration wording, and court-jurisdiction wording remain literal enough for retrieval.

3. Chinese eligibility wording around age, microchip, and residency on `p10`

File:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

Reason:
- Clauses `II` to `V` define age limits, dog microchip requirement, cat identity-proof requirement, ownership, and residence rules.
- These are operationally important and easy to over-normalize when reformatting.

4. Chinese breed and regulated-species wording in General Exceptions on `p8-p9`

File:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

Reason:
- The breed list, dangerous-dogs references, and protected-species rule are high-risk for naming drift.
- Manual review should confirm the normalized Chinese wording matches the printed source closely enough.

5. Section 1 waiting periods and co-payment wording on `p1` and `p6`

Files:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_en.md`
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`

Reason:
- Waiting periods and the 30% co-payment rule are high-frequency answer targets.
- Manual review should confirm the normalized units did not unintentionally merge or soften the trigger conditions.

## Current Confidence

- Structure and clause mapping: high
- Benefit sections and waiting periods: high
- Definitions: high
- Conditions / claims procedure: medium-high
- Chinese legal-style wording fidelity: medium-high
