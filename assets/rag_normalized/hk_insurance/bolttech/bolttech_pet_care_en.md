---
schema_version: rag-policy-v1
provider: bolttech
product: pet_care
policy_type: pet_insurance
language: en
region: hk
source_format: pdf
source_file: assets/rag/hk_insurance/bolttech/PetInsurance.pdf
source_page_reference_scheme: printed_label
source_version_label: "Pet Care (May 2024)"
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

# Bolttech Pet Care Insurance Policy

> Source: p2-p8
> Unit: policy

## Section 1 Medical Coverage

### A. Veterinary Consultation Fee

> Source: p3
> Clause: 1.A
> Unit: benefit

The Company covers veterinary consultation fees incurred at a licensed veterinary clinic.

- Maximum benefit: HK$250 per visit (Plan 2 and Plan 3)
- Not Applicable for Plan 1
- Maximum 20 visits per year (Plans 2 and 3)

### B. Prescribed Medication

> Source: p3
> Clause: 1.B
> Unit: benefit

The cost of any prescribed drugs, dressings, and injections dispensed by a licensed veterinary clinic.

- Maximum benefit: HK$250 per visit (Plan 2 and Plan 3)
- Not Applicable for Plan 1
- Maximum 20 visits per year (Plans 2 and 3)

### C. Room and Board

> Source: p3
> Clause: 1.C
> Unit: benefit

Confinement cost incurred in a veterinary clinic for a period of no less than 12 consecutive hours.

- Plan 2: HK$250 per day, maximum 12 days per year
- Plan 3: HK$250 per day, maximum 12 days per year
- Not Applicable for Plan 1

### D. Clinical and Surgical Expenses

> Source: p3-p4
> Clause: 1.D
> Unit: benefit

The following expenses incurred in a licensed veterinary clinic:

- Surgical Fee
- Operating Theatre Fee
- Anaesthetist Fee
- Euthanasia Fee
- X-ray and Laboratory Tests Fee
- Chemotherapy Treatment Expenses
- Post-Surgical Treatment Expenses (up to 90 days following the day of surgery)

- Plan 2: Maximum HK$60,000 per year
- Plan 3: Maximum HK$60,000 per year (base) + additional coverage under Section 1E optional top-up
- Not Applicable for Plan 1

### E. Top-up Medical Expenses (Optional Cover)

> Source: p5
> Clause: 1.E
> Unit: benefit

Additional coverage when Section 1D is exhausted. Applicable to Plan 2 and Plan 3 only.

- Option (a): Benefit limit HK$10,000, 15% loading on annual premium
- Option (b): Benefit limit HK$30,000, 25% loading on annual premium

#### Co-insurance

> Source: p3
> Clause: 1.E
> Unit: payout

20% co-insurance per claim for Insured Benefits Section 1. The portion of the claim amount that the policyholder needs to bear on each claim.

#### Waiting Period

> Source: p3
> Clause: 1
> Unit: waiting_period

A 30-day waiting period from the policy effective date is applied to claims for medical expenses resulting from illness.

## Section 2 Third Party Liability

> Source: p3-p4
> Clause: 2
> Unit: benefit

Legal liability to third parties caused by the insured pet.

- Plan 1: HK$600,000
- Plan 2: HK$600,000
- Plan 3: HK$1,000,000

#### Excess

> Source: p3-p4
> Clause: 2
> Unit: payout

The first HK$3,000 of each and every claim is borne by the policyholder.

## Section 3 Funeral Service Expenses

> Source: p3-p4
> Clause: 3
> Unit: benefit

Funeral service expenses arranged by a licensed veterinary clinic or funeral service provider.

- Plan 2: HK$1,000 per life
- Plan 3: HK$1,500 per life
- Not Applicable for Plan 1

## Section 4 Holiday Cancellation

> Source: p3-p4
> Clause: 4
> Unit: benefit

The non-recoverable prepaid holiday cancellation and curtailment costs of the insured if the insured pet requires emergency life-saving surgery.

- Plan 2: HK$3,000 per year
- Plan 3: HK$5,000 per year
- Not Applicable for Plan 1

## Section 5 Advertising Expenses

> Source: p3-p4
> Clause: 5
> Unit: benefit

The cost of advertisement if your pet is stolen or lost.

- Plan 2: HK$250 per year
- Plan 3: HK$400 per year
- Not Applicable for Plan 1

## Section 6 Overseas Cover

> Source: p3-p4
> Clause: 6
> Unit: benefit

Extended coverage to your pet for Sections 1, 2 and 3 whilst travelling or temporarily located outside Hong Kong up to a maximum of 90 days per trip.

- Plan 2: Per trip coverage
- Plan 3: Per trip coverage
- Not Applicable for Plan 1

## Section 7 Emergency Boarding Expenses

> Source: p3-p4
> Clause: 7
> Unit: benefit

Reimbursement of pet sitting expenses at a boarding kennel if the policyholder is hospitalized for more than 4 days.

- Plan 2: HK$200 per day, maximum 5 days per year
- Plan 3: HK$500 per day, maximum 5 days per year
- Not Applicable for Plan 1
- Co-insurance: 50% per claim

## General Exclusions

### Applicable to Section 1 (Medical Coverage)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Pre-existing conditions
- Claims for expenses incurred during the waiting period
- Charges in respect of disposal, cremation, or burial of the insured pet
- Diet foods, special diet, pet foods, vitamins, mineral supplements, housing, bedding, and bathing needs for the treatment or general well-being of the insured pet
- Fees for treatment relating to hereditary, congenital abnormality, or congenital illness declared or judged by a vet; training or therapy for behavioral problems, mental or emotional disorder, cryptorchidism
- Costs of any treatment related to dentistry (except dental treatment due to an accident); pregnancy, birth, or breeding and any complications thereof; organ transplantation; elective procedures and cosmetic surgeries
- Costs of any routine physical examinations, X-ray, laboratory tests, preventative treatments, preventative vaccinations, spaying, neutering, castration, grooming, routine removal of dew claws, killing and controlling fleas, treating round worms and tapeworms, grooming and nail clipping, or any complications arising from these treatments
- Administrative fees charged by the vet for the purposes of processing your claim, including but not limited to any charges for completing the claim forms and/or providing reports, certificates, supporting documents, or other information

### Applicable to Section 2 (Third Party Liability)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Loss or damage to property in the ownership, custody, care, or control of yourself, the family, or any person residing with or in the service of you
- Accidental injury to or illness contracted by you, the family, or any person living with or in the service of you
- Fines, penalty, surcharge, or late payment
- Punitive, aggravated, or exemplary damages
- Any claim arising from or involving the insured pet being at any place for which it is prohibited
- Any claim arising from an occurrence in connection with your profession, occupation, or business

### Applicable to Section 3 (Funeral Service Expenses)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Transportation fee not arranged by the vet or funeral service provider
- The cost of the niche or burial ground of the remains of the insured pet

### Applicable to Section 4 (Holiday Cancellation)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Non life-saving surgery of the insured pet
- Any pre-existing or foreseeable condition or disease prior to departure
- Any cancelled holiday booked less than 15 days prior to the scheduled departure date
- Any loss of other persons who will be on holiday with you

### Applicable to Section 5 (Advertising Expenses)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Any expenses incurred more than 30 days from the date on which the insured pet is stolen or lost

### Applicable to Section 6 (Overseas Cover)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Any expenses incurred during the trip which is intentionally arranged for medical or surgical treatment for the insured pet
- Any expenses incurred during a trip which is undertaken against the vet's recommendation

### Applicable to Section 7 (Emergency Boarding Expenses)

> Source: p8
> Clause: Exclusions
> Unit: exclusion

- Any boarding at an unlicensed boarding kennel or outside Hong Kong
- Your pet has not received vaccination against common disease before boarding
- Hospitalization is not due to medical necessity
- Boarding required due to pregnancy of the pet owner
- Failure to provide valid documentary proof issued by a hospital in Hong Kong where the pet owner is hospitalized

## General Conditions

### No Claim Discount

> Source: p6-p8
> Clause: General Conditions
> Unit: renewal_rule

If there is no claim made or arising in previous policy year(s), the policyholder enjoys a no claim discount on renewal premium for the next policy year.

| No claim period | Discount |
|---|---|
| One year | 5% |
| Two consecutive years | 10% |
| Three or more consecutive years | 15% |

If a claim is payable, the no claim discount resets to 0%.

### Volume Discount

> Source: p6
> Clause: General Conditions
> Unit: eligibility

The following discount is applied according to the number of dogs or cats being insured at the same time under the same insurance plan (applicable to Plan 2 and Plan 3 only).

| Number of insured pets | Discount |
|---|---|
| 2 pets | 5% |
| 3 pets | 7.5% |
| 4 pets | 10% |
| 5 pets or more | 15% |

### Restricted Dog Breeds

> Source: p5-p7
> Clause: General Conditions
> Unit: eligibility

The following dog breeds may attract an additional 10% premium loading:

- American Cocker Spaniel, Clumber Spaniel, Mountain Terrier
- American Staffordshire Terrier, Dalmatian, Newfoundland
- Basenji, Deerhound, Old English Sheepdog
- Basset Hound, Doberman Pinscher, Otterhound
- Bernese Mountain Dog, German Shepherd, Pharaoh Hound
- Boxer, Great Dane, Rottweiler
- Bulldog, Greyhound, Saint Bernard
- Bullmastiff, Irish Wolfhound, Staffordshire Bull Terrier
- Chinese Shar-Pei, Leonberger, Wheaten Terrier
- Chow Chow, Mastiff



The following dangerous dogs are excluded from the Plan

- Pit Bull Terrier, Tibetan Mastiff
- Japanese Tosa, Bull Terrier, Dogo Argentino, Fila Brasileiro

  Bolttech Insurance (Hong Kong) Company Limited reserves the right to make the final decision on whether a dog breed is eligible for insurance.

### Policy Renewal

> Source: p5-p8
> Clause: General Conditions
> Unit: renewal_rule

Annual premium is determined annually based on the attained age of the insured pet.

The premium table is subject to the latest version of Pet Care, or any substitute product, published by the Company at the time of new application or renewal.

Bolttech Insurance reserves the right to revise the terms and conditions upon renewal by giving 30 days' advance notice.

The insurance coverage applied for shall only take effect when the application has been accepted by the Company and the required premium has been paid.

The minimum premium charge for policy cancellation is HK$500 per pet.

### Insurance Levy

> Source: p5-p6
> Clause: General Conditions
> Unit: payout

The insurance levy rate is 0.100% (from 1 Apr 2021 onwards), with a cap of HK$5,000. The levy is collected by the Insurance Authority and is not included in the premium amounts shown.
