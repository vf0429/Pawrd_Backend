# OneDegree Manual Review Checklist

## Status

`assets/rag_normalized/hk_insurance/one_degree/` is now close to a manual-review state. The normalized English and Chinese markdown preserve the official clause tree and page anchors, but several areas remain sensitive because OneDegree benefit logic depends heavily on age, waiting-period timing, and plan-upgrade interactions.

## Items To Manually Review In PDF

1. Chronic-condition age split and upgrade interaction on `p34-p35` and `p6-p7`

Files:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

Reason:
- Clause `1.1` is structurally correct, but it contains the most retrieval-sensitive logic in the policy.
- Manual review should confirm the wording around age `<= 4`, age `>= 5`, waiting-period timing, and upgrade-date triggers is fully faithful to source wording.

2. Section `1.2` medical-expense boundaries on `p35-p37` and `p8`

Files:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

Reason:
- Surgery, overnight hospitalisation, imaging, laboratory testing, and consultation benefits are now separated into stable units.
- Manual review should confirm no reimbursement qualifier or exclusion boundary was shifted when those units were normalized.

3. Cancer and critical-illness cash-benefit wording on `p38-p39` and `p9`

Files:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

Reason:
- These sections mix trigger, payout, lifetime-limit, evidentiary, and exclusion logic.
- Manual review should confirm the “first time in lifetime” and 180-day waiting-period wording remains literal enough for quote-sensitive answers.

4. Definitions and cross-reference wording in later policy sections

Files:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_en_2025-12-31.md`
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

Reason:
- OneDegree relies heavily on cross references across coverage, exclusions, and upgrade/downgrade mechanics.
- Manual review should confirm those references were preserved verbatim enough for downstream retrieval and provider-aware chunking.

5. Chinese wording density in conditions / exclusion-heavy pages

File:
- `assets/rag_normalized/hk_insurance/one_degree/one_degree_pet_ceo_plan_zh_2025-12-31.md`

Reason:
- The Chinese source mirrors the English structure, but dense legal wording can still be over-smoothed during normalization.
- Final manual review should compare the most clause-dense Chinese sections against the PDF before this provider is treated as review-complete.

## Current Confidence

- Structure and clause mapping: high
- Benefits and waiting-period units: high
- Chronic-condition logic fidelity: medium-high
- Cash-benefit sections: medium-high
- Definitions / conditions legal fidelity: medium-high
