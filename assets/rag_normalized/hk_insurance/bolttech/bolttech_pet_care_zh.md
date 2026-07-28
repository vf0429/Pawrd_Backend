---
schema_version: rag-policy-v1
provider: bolttech
product: pet_care
policy_type: pet_insurance
language: zh
region: hk
source_format: pdf
source_file: assets/rag/hk_insurance/bolttech/PetInsurance.pdf
source_page_reference_scheme: printed_label
source_version_label: "毛孩寵物保 (2024年5月)"
normalization_status: draft
normalization_method: manual_pdf_first
contains_bilingual_parallel_text: false
legacy_markdown_reference: []
excluded_content_types:
  - table_of_contents
  - marketing_intro
  - application_form
  - privacy_statement
  - premium_calculation_formula
  - premium_table
---

# Bolttech 毛孩寵物保險

> Source: p2-p9
> Unit: policy

## 項目1 醫療保障

### A. 獸醫診金

> Source: p4
> Clause: 1.A
> Unit: benefit

本公司支付受保寵物於持牌獸醫診所的診金。

- 計劃2：每隻寵物每次 HK$250
- 計劃3：每隻寵物每次 HK$250
- 計劃1：不適用
- 每年上限：各20次（計劃2及計劃3）

### B. 處方藥物

> Source: p4
> Clause: 1.B
> Unit: benefit

任何由持牌獸醫診所處方的藥物、敷料及注射費用。

- 計劃2：每次 HK$250
- 計劃3：每次 HK$250
- 計劃1：不適用
- 每年上限：各20次（計劃2及計劃3）

### C. 住房費用

> Source: p4
> Clause: 1.C
> Unit: benefit

於獸醫診所內接受治療，不少於連續12小時的住院費用。

- 計劃2：每日 HK$250，每年上限12日
- 計劃3：每日 HK$250，每年上限12日
- 計劃1：不適用

### D. 門診及手術費用

> Source: p4-p5
> Clause: 1.D
> Unit: benefit

於持牌獸醫診所產生的以下費用：

- 手術費用
- 手術室費用
- 麻醉師費用
- 人道毀滅費用
- X光檢查及化驗費用
- 化學治療費用
- 手術後治療費用（最長為手術後90日）

- 計劃2：每年上限 HK$60,000
- 計劃3：每年上限 HK$60,000（基本）+ 可選附加保障（項目1E）
- 計劃1：不適用

### E. 附加醫療費用保障（可選保障）

> Source: p5
> Clause: 1.E
> Unit: benefit

適用於項目1D保障限額用盡後。只適用於計劃2及計劃3。

- 選項(a)：最高保障金額 HK$10,000，年繳保費附加15%
- 選項(b)：最高保障金額 HK$30,000，年繳保費附加25%

#### 共同保險

> Source: p4
> Clause: 1.E
> Unit: payout

保障範圍內項目每宗索償之共同保險：20%。每次索償所需承擔之自付金額比率。

#### 等候期

> Source: p4
> Clause: 1
> Unit: waiting_period

項目1：因疾病引致的醫療費用索償，有由保單生效日起計30天的等候期。

## 項目2 第三方責任

> Source: p4-p5
> Clause: 2
> Unit: benefit

受保寵物引致對第三方之法律責任。

- 計劃1：HK$600,000
- 計劃2：HK$600,000
- 計劃3：HK$1,000,000

#### 自付額

> Source: p4-p5
> Clause: 2
> Unit: payout

每宗索償的首 HK$3,000 由保戶承擔。

## 項目3 殯葬服務費用

> Source: p4-p5
> Clause: 3
> Unit: benefit

由持牌獸醫或殯葬服務供應商安排的殯葬服務費用。

- 計劃2：每隻 HK$1,000
- 計劃3：每隻 HK$1,500
- 計劃1：不適用

## 項目4 假日行程取消津貼

> Source: p4-p5
> Clause: 4
> Unit: benefit

因受保寵物需要緊急救生手術而導致保戶損失已繳付及不能退回的行程費用。

- 計劃2：每年 HK$3,000
- 計劃3：每年 HK$5,000
- 計劃1：不適用

## 項目5 廣告費用

> Source: p4-p5
> Clause: 5
> Unit: benefit

受保寵物被盜或丟失之廣告費用。

- 計劃2：每年 HK$250
- 計劃3：每年 HK$400
- 計劃1：不適用

## 項目6 海外保障

> Source: p4-p5
> Clause: 6
> Unit: benefit

延長保障至海外，涵蓋項目1、2及3。受保寵物於香港境外旅遊或短暫逗留，每次旅程最長90天。

- 計劃2：每個旅程
- 計劃3：每個旅程
- 計劃1：不適用

## 項目7 緊急寄養費用

> Source: p4-p5
> Clause: 7
> Unit: benefit

保戶住院超過4天時，受保寵物於寄養場所的費用賠償。

- 計劃2：每日 HK$200，每年上限5日
- 計劃3：每日 HK$500，每年上限5日
- 計劃1：不適用
- 共同保險：每宗索償50%

## 不保事項

### 適用於項目1

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 投保前已存在之狀況
- 就等候期內所招致費用提出的索償
- 有關處置、火化或埋葬受保寵物的費用
- 治療或一般保健所需的營養膳食、特別膳食、日常膳食、維他命、礦物質補充劑、居所、寢具及沐浴用品之費用
- 與治療遺傳性、先天性畸形、先天性病症、行為問題、精神或情緒問題、隱睾症相關之治療或訓練費用
- 牙科治療費用（但因意外引起的牙科治療除外）；懷孕、分娩或繁殖及其任何併發症；器官移植；非必要醫療程序及整容手術有關之費用
- 例行身體檢查、X光、化驗及預防性治療、防疫針、絕育、結紮、例行移除狼爪、除蚤及防蚤、杜蟲、美容及修甲，或上述治療引起的任何併發症
- 獸醫就處理索償目的而收取的行政費用，包括但不限於填寫索償表及/或提供報告、證書、證明文件或資料之收費

### 適用於項目2

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 由閣下、家屬、任何與閣下同住或為閣下服務人士所擁有、託管、照顧或控制之財物之任何損失或損壞
- 閣下、家屬、任何與閣下同住或為閣下服務人士因意外而引致身體受傷或染病
- 罰款、附加費或逾期付款
- 懲罰性、加重性或懲戒性的損害賠償
- 由於或涉及受保寵物出現於禁止其進入的任何地方而引起之任何索償
- 與閣下之專業、職業或業務有關之事件引起之任何索償

### 適用於項目3

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 非由獸醫或殯葬服務供應商安排之交通運輸費用
- 安放受保寵物遺體之龕位或墓地費用

### 適用於項目4

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 非對受保寵物性命攸關之手術
- 任何出發前已存在或可預知的狀況或疾病
- 就取消行程而言，該行程是在預定出發日前15天內預訂的
- 與閣下一起旅遊人士之任何損失

### 適用於項目5

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 受保寵物被盜或丟失之日起超過30日所招致的任何費用

### 適用於項目6

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 受保寵物接受醫療或手術治療而作出旅程所招致的任何費用
- 有違獸醫建議的旅行所招致的任何費用

### 適用於項目7

> Source: p9
> Clause: Exclusions
> Unit: exclusion

- 任何於無牌寵物寄養狗/貓舍產生或在香港境外產生的寄養費用
- 寵物在寄養前並未接種預防常見疾病的疫苗
- 住院不是出於醫療需要
- 因寵物主人懷孕而產生的寄養需要
- 未能提供由寵物主人住院之香港醫院簽發的有效證明文件

## 一般條件

### 無索償折扣

> Source: p6-p8
> Clause: General Conditions
> Unit: renewal_rule

如果在上一個或之前保單年度沒有提出或產生任何索償，續保保費可享無索償折扣。

| 無索償年期 | 折扣 |
|---|---|
| 一年 | 5% |
| 連續兩年 | 10% |
| 連續三年或以上 | 15% |

如有任何應付或已支付的索償，無索償折扣將重置為0%。

### 數量折扣

> Source: p6
> Clause: General Conditions
> Unit: eligibility

同時投保多隻寵物予同一計劃可享以下折扣（只適用於計劃2及計劃3）。

| 投保寵物數量 | 折扣 |
|---|---|
| 2隻寵物 | 5% |
| 3隻寵物 | 7.5% |
| 4隻寵物 | 10% |
| 5隻或以上寵物 | 15% |

### 限制犬種

> Source: p5-p7
> Clause: General Conditions
> Unit: eligibility

以下犬種可能須額外徵收10%附加保費：

- 美國曲卡犬、克倫伯獵犬、山爹利犬
- 美國史特富郡爹利犬、斑點犬、紐芬蘭犬
- 貝生吉犬、獵鹿犬、古英國牧羊犬
- 巴吉度獵犬、都柏文犬、奧德獵犬
- 伯恩山犬、德國牧羊犬、法老王獵犬
- 拳師犬、大丹犬、洛威拿犬
- 老虎犬、靈提犬、聖伯納犬
- 鬥牛獒犬、愛爾蘭獵狼犬、斯塔福郡鬥牛㹴
- 中國沙皮犬、蘭伯格犬、軟毛麥色爹利犬
- 鬆獅犬、獒犬



下列危險狗隻品種將不獲授保：

- 比特鬥牛㹴、藏獒犬

- 日本土佐犬、公牛㹴、阿根廷杜告犬、巴西非拉犬

  以上所列犬種僅供參考，bolttech 保留最終決定狗隻品種是否符合承保資格的權利。

### 保單續保

> Source: p5-p8
> Clause: General Conditions
> Unit: renewal_rule

年繳保費會按受保寵物已達年齡每年釐定。

保費表以本公司在新投保或續保時所公布的最新版本毛孩寵物保或其替代產品為準。

bolttech 保留於續保時修訂條款及細則的權利，並會提前30天通知。

申請的保險保障僅於本公司接納申請及已繳付所需保費後生效。

取消保單收取的最低保費為每隻寵物 HK$500。

### 保險徵費

> Source: p5-p6
> Clause: General Conditions
> Unit: payout

保險業監管局徵費率為0.100%（由2021年4月1日起），上限為 HK$5,000。徵費不包含於所示保費金額內。
