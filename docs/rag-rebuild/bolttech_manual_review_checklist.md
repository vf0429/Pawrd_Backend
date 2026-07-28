# Bolttech Manual Review Checklist

## Status

`assets/rag_normalized/hk_insurance/bolttech/` is now in a strong manual-review state.

Completed in the latest pass:
- remapped source anchors to visible brochure page labels
- rechecked benefits, waiting period, co-insurance, discount tables, levy, and exclusion blocks against the PDF
- removed summary-like renewal wording that was not well supported by the brochure text

## Remaining Manual Review Items

1. Restricted dog breed Chinese names on `p5`

Files:
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_zh.md`
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_en.md`

Reason:
- This breed table is still the weakest OCR zone in the brochure.
- English breed names are relatively stable, but several Chinese names may differ from the printed wording.

Highest-risk names:
- `山爹利犬`
- `奧德獵犬`
- `老虎犬`
- `蘭伯格犬`
- `軟毛麥色爹利犬`
- `巴西非拉犬`

2. Chinese major exclusions block on `p9`

File:
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_zh.md`

Reason:
- The Chinese exclusion page is OCR-damaged in the source PDF.
- Current wording is semantically aligned and structurally correct, but a visual spot-check against the brochure is still recommended.

Highest-risk lines:
- `有關處置、火化或埋葬受保寵物的費用`
- `居所、寢具及沐浴用品`
- `非必要醫療程序及整容手術`
- `任何於無牌寵物寄養狗/貓舍產生或在香港境外產生的寄養費用`
- `寵物在寄養前並未接種預防常見疾病的疫苗`

3. Renewal wording source support on `p6-p8`

Files:
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_en.md`
- `assets/rag_normalized/hk_insurance/bolttech/bolttech_pet_care_zh.md`

Reason:
- The brochure contains premium / renewal notes spread across examples and footnotes rather than a clean policy clause.
- The normalized wording is conservative and source-backed, but this section should still be treated as brochure-level operational rules rather than full policy wording.

## Current Confidence

- Structure and printed-page anchoring: high
- Benefits and limits: high
- Discounts / levy / waiting period: high
- English exclusions: high
- Chinese exclusions: medium-high
- Chinese breed-name table: medium
