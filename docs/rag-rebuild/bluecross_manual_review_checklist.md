# Blue Cross Manual Review Checklist

## Status

`assets/rag_normalized/hk_insurance/bluecross/` is now close to a manual-review state. Definitions, benefit sections, major exclusions, renewal rules, and claims provisions have been rebuilt against the PDF and re-expanded where earlier normalization had over-compressed the legal text.

## Items To Manually Review In PDF

1. Chinese wording in Section 4 Emergency Boarding on `p4-p5`

File:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`

Lines to inspect:
- `如保單持有人於保單生效期間住院多於連續4天，本公司將支付在保單持有人住院期間其受保寵物於寄養設施所產生必要的寵物托管費用，惟寄養必須於保單持有人入院當日或之後開始。`
- `如寵物托管橫跨兩個受保期，應付的保障將按實際寵物托管費用招致日期分攤至相應的受保期。`

Reason:
- Current wording is semantically aligned to the English text.
- The Chinese brochure wording uses `寄養所` / `寵物託管費用`; current normalized text uses slightly cleaner modern phrasing.

2. Chinese wording in Section 5 Overseas Cover on `p5`

File:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`

Lines to inspect:
- `由離港日起計每次旅程包括檢疫期在內不超過90天`
- `本公司於本部分下的最高責任不得超過保單資料頁中第一、二及三部分所列的相關限額。`

Reason:
- Meaning is correct, but the original Chinese source uses a more clause-dense sentence structure.

3. Chinese Section 1 exclusion terminology on `p3-p4`

File:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`

Lines to inspect:
- `居所、寢具及沐浴用品`
- `與行為問題有關的治療、訓練或治療課程費用`
- `非必要就醫及整容手術`

Reason:
- These are materially aligned to the English side.
- Final legal-style wording could be visually calibrated against the Chinese print.

4. Chinese sanction clause compression on `p7`

File:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`

Lines to inspect:
- `### 26. 制裁限制及不保條款`

Reason:
- This clause is long and jurisdiction-heavy in the source.
- The normalized version preserves the operative meaning but compresses legal boilerplate.

5. Claims Provisions wording density on `p8`

Files:
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_en_pcp-05-2024.md`
- `assets/rag_normalized/hk_insurance/bluecross/bluecross_lovepet_zh_pcp-05-2024.md`

Reason:
- Structure is now correct.
- Manual review should confirm whether any clause needs to be made more literal for evidentiary or quote-heavy RAG use.

## Current Confidence

- Structure and clause mapping: high
- Definitions: high
- Benefits Sections 1-5: high
- General Exclusions / No Claim Discount: high
- General Conditions: medium-high
- Claims Provisions: medium-high
