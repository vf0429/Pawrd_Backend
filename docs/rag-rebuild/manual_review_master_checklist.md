# Manual Review Master Checklist

This file tracks provider-by-provider manual review items after the normalized markdown reaches approximately 90% confidence.

## OneDegree

Status: near manual review

References:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`
- `docs/rag-rebuild/one_degree_manual_review_checklist.md`

Manual review focus:
- Chronic-condition age split and upgrade interaction on `p34-p35` / `p6-p7`
- Section 1.2 benefit wording boundaries on `p35-p37` / `p8`
- Cancer / critical illness cash-benefit trigger wording on `p38-p39` / `p9`
- Definitions and plan-upgrade cross references on later condition pages

Confidence:
- Structure and clause mapping: high
- Benefits and waiting-period units: high
- Cash-benefit sections: medium-high
- Definitions / conditions legal fidelity: medium-high

## Blue Cross

Status: near manual review

References:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_en_pcp-05-2024.md`
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`
- `docs/rag-rebuild/bluecross_manual_review_checklist.md`

Manual review focus:
- General Conditions clause density from `p5-p7`
- Claims Provisions wording density on `p8`
- Chinese wording for Section 4 Emergency Boarding on `p4-p5`
- Chinese wording for Section 5 Overseas Cover on `p5`
- Chinese wording for Section 1 exclusions on `p3-p4`

Confidence:
- Definitions: high
- Sections 1-5 benefits/exclusions: high
- General Conditions: medium-high
- Claims Provisions: medium-high

## MSIG

Status: near manual review

References:
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_en_petbr0a.md`
- `assets/rag_normalized/hk_insurance/MSIG/msig_happy_tails_zh_petbr0a_chi.md`
- `docs/rag-rebuild/msig_manual_review_checklist.md`

Manual review focus:
- Chinese definitions wording density on `p1-p2`
- Section IV liability / court-jurisdiction wording on `p2-p3`
- General Conditions clauses 8-13 on `p3`
- Claims arbitration wording on `p4`
- General Exclusions clause 4 density on `p4-p5`
- War / sanctions legal boilerplate on `p4-p5`

Confidence:
- Structure and clause mapping: high
- Benefits and liability sections: high
- No Claim Discount: high
- General Conditions: medium-high
- Claims Conditions: medium-high
- Chinese legal-style wording fidelity: medium

## Prudential

Status: near manual review

References:
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_en.md`
- `assets/rag_normalized/hk_insurance/prudential/prudential_policy_zh.md`
- `docs/rag-rebuild/prudential_manual_review_checklist.md`

Manual review focus:
- General Exceptions legal-density sections on `p2-p5` / `p8-p9`
- Claims / arbitration / jurisdiction wording on `p4-p5` / `p10-p11`
- Chinese microchip / eligibility wording on `p10`
- Chinese breed and sanctions boilerplate on `p8-p9`

Confidence:
- Structure and clause mapping: high
- Benefit sections and waiting periods: high
- Definitions: high
- Conditions / claims procedure: medium-high
- Chinese legal-style wording fidelity: medium-high

## On Hold

### Bolttech

Status: on hold, excluded from current supported-provider answer scope

References:
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_en.md`
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_zh.md`
- `docs/rag-rebuild/bolttech_manual_review_checklist.md`

Note:
- Keep the files for future reuse, but do not treat Bolttech as part of the current active provider set, corpus QA status summary, or answer scope.
