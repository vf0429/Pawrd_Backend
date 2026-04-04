package main

import (
	"log"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// SeedMedicalServices inserts demo content for all placeholder medical services.
// Idempotent — skips rows whose category already exists.
func SeedMedicalServices(db *gorm.DB) {
	services := []models.MedicalService{
		{
			Category:    "deworming",
			Name:        "Deworming",
			NameZh:      "驅蟲",
			Icon:        "pills.fill",
			ColorHex:    "#34C759",
			Description: "Regular deworming protects your pet from intestinal parasites and heartworm. Recommended every 1–3 months depending on lifestyle.",
			DescZh:      "定期驅蟲可保護寵物免受腸道寄生蟲及心絲蟲侵害，建議每1-3個月進行一次。",
			ContentJSON: `{
  "overview": "Deworming is a core preventive care routine for dogs and cats. Internal parasites such as roundworms, hookworms, and tapeworms can cause weight loss, vomiting, and serious organ damage if left untreated.",
  "frequency": "Every 1–3 months for outdoor pets; every 3–6 months for indoor-only pets.",
  "products": [
    {"name": "Milbemax (Dog)", "dose": "1 tablet / 10 kg", "price_hkd": 95, "covers": "Roundworm, Hookworm, Tapeworm"},
    {"name": "Milbemax (Cat)", "dose": "1 tablet / 4 kg", "price_hkd": 85, "covers": "Roundworm, Hookworm, Tapeworm"},
    {"name": "Revolution Plus (Cat)", "dose": "Topical — monthly", "price_hkd": 135, "covers": "Fleas, Ticks, Roundworm, Heartworm"},
    {"name": "NexGard Spectra (Dog)", "dose": "1 chew / month by weight", "price_hkd": 165, "covers": "Fleas, Ticks, Roundworm, Heartworm"}
  ],
  "steps": [
    "Weigh your pet before purchasing — dose is weight-based.",
    "Administer with food to minimise stomach upset.",
    "Record the date and product used in the Pawrd Health Records.",
    "Repeat on schedule — set a reminder in the app."
  ],
  "notes": "Puppies and kittens should be dewormed from 2 weeks of age and more frequently in the first 6 months."
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 1,
		},
		{
			Category:    "dental",
			Name:        "Dental Care",
			NameZh:      "口腔護理",
			Icon:        "mouth.fill",
			ColorHex:    "#FF9500",
			Description: "Dental disease affects over 80% of pets by age 3. Scaling, polishing, and daily brushing prevent painful tooth loss and systemic infections.",
			DescZh:      "超過80%的寵物在3歲前患有牙周病。洗牙、拋光及每日刷牙可預防牙齒脫落及全身性感染。",
			ContentJSON: `{
  "overview": "Periodontal disease is the most common health condition in pets. Bacteria from the mouth can travel to the heart, kidneys, and liver. A professional dental scale and polish under anaesthesia is the gold standard for deep cleaning.",
  "services": [
    {"name": "Dental Scale & Polish", "duration": "45–90 min (GA)", "price_hkd": "1800–3500", "note": "Requires pre-anaesthetic blood test"},
    {"name": "Tooth Extraction", "duration": "Additional time", "price_hkd": "500–800 per tooth", "note": "If tooth is non-viable"},
    {"name": "Dental X-Ray", "duration": "15 min (included in scale)", "price_hkd": "Included", "note": "Checks below gum line"}
  ],
  "home_care": [
    "Brush teeth daily with pet-safe toothpaste (never human toothpaste).",
    "Use dental chews approved by VOHC (Veterinary Oral Health Council).",
    "Offer dental water additives as a supplement — not a replacement for brushing.",
    "Annual professional check-up is recommended for all adult pets."
  ],
  "signs_of_dental_disease": ["Bad breath", "Yellow/brown tartar on teeth", "Red or bleeding gums", "Difficulty chewing", "Pawing at mouth"],
  "frequency": "Professional cleaning every 1–2 years; home care daily."
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 2,
		},
		{
			Category:    "lab_tests",
			Name:        "Lab Tests",
			NameZh:      "化驗檢查",
			Icon:        "testtube.2",
			ColorHex:    "#5856D6",
			Description: "Blood panels, urinalysis, and pathology tests to detect hidden illness early. Essential before anaesthesia and for senior pets annually.",
			DescZh:      "血液檢查、尿液分析及病理化驗，可早期發現隱性疾病。麻醉前及年老寵物每年必做。",
			ContentJSON: `{
  "overview": "Laboratory diagnostics help vets detect conditions that are invisible to the naked eye — from kidney disease and diabetes to anaemia and infections. Early detection dramatically improves outcomes and reduces treatment costs.",
  "panels": [
    {
      "name": "Pre-Anaesthetic Panel",
      "tests": ["CBC", "Liver enzymes (ALT, ALP)", "BUN / Creatinine", "Blood glucose"],
      "price_hkd": "680–950",
      "turnaround": "Same day (in-clinic)"
    },
    {
      "name": "Senior Wellness Panel",
      "tests": ["Full CBC", "Comprehensive metabolic", "Thyroid (T4)", "Urinalysis"],
      "price_hkd": "1200–1800",
      "turnaround": "Same day / Next day"
    },
    {
      "name": "Infectious Disease Screen",
      "tests": ["FeLV/FIV (cats)", "Heartworm antigen (dogs)", "Tick-borne diseases"],
      "price_hkd": "380–620",
      "turnaround": "15–30 min (rapid test)"
    },
    {
      "name": "Urinalysis",
      "tests": ["Dipstick", "Specific gravity", "Sediment microscopy"],
      "price_hkd": "280–420",
      "turnaround": "Same day"
    }
  ],
  "when_to_test": [
    "Before any surgery or procedure requiring anaesthesia",
    "Annually for pets over 7 years old",
    "When your pet shows signs of illness (lethargy, vomiting, increased thirst)",
    "As part of a new-pet baseline health check"
  ]
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 3,
		},
		{
			Category:    "microchip",
			Name:        "Microchip",
			NameZh:      "晶片植入",
			Icon:        "wave.3.right",
			ColorHex:    "#00BCD4",
			Description: "A permanent 15-digit ISO microchip implanted under the skin. Legally required for dogs in Hong Kong. Reunites lost pets with their owners.",
			DescZh:      "植入皮下的永久ISO 15位晶片，香港法律規定狗隻必須植入。幫助走失寵物找回主人。",
			ContentJSON: `{
  "overview": "A microchip is a tiny transponder (about the size of a grain of rice) implanted under the skin between the shoulder blades. It stores a unique 15-digit ISO number that links to your contact information in a national registry.",
  "legal_requirements_hk": {
    "dogs": "Mandatory under the Dogs and Cats Ordinance. Must be registered with AFCD within 30 days of implantation.",
    "cats": "Not legally required but strongly recommended.",
    "penalty": "Failure to microchip and register a dog in HK: up to HK$10,000 fine."
  },
  "procedure": {
    "duration": "Under 5 minutes",
    "anaesthesia": "Not required — quick injection under the skin",
    "price_hkd": "250–450 (implant + registration)",
    "pain_level": "Similar to a routine vaccination"
  },
  "steps": [
    "Book an appointment at any licensed veterinary clinic.",
    "The vet implants the chip and scans to confirm it reads correctly.",
    "Complete the AFCD registration form (dogs) or private registry (cats).",
    "Update your contact details if you move or change phone number."
  ],
  "important": "Always update the registry if your contact details change — the chip is only useful if the registry is current."
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 4,
		},
		{
			Category:    "nutrition",
			Name:        "Nutrition",
			NameZh:      "營養諮詢",
			Icon:        "leaf.fill",
			ColorHex:    "#4CAF50",
			Description: "Personalised diet planning for your pet's age, breed, weight, and health conditions. Includes raw, home-cooked, and premium commercial food guidance.",
			DescZh:      "根據寵物年齡、品種、體重及健康狀況制定個人化飲食計劃，包括生食、自煮及高端商業糧建議。",
			ContentJSON: `{
  "overview": "Proper nutrition is the foundation of long-term health. Obesity, food allergies, kidney disease, and joint problems are all closely linked to diet. A certified veterinary nutritionist can design a meal plan tailored to your pet's unique needs.",
  "consultation_types": [
    {
      "name": "Basic Nutrition Review",
      "format": "30-min online consultation",
      "price_hkd": 480,
      "includes": ["Review of current diet", "Feeding amount calculation", "Brand recommendations"]
    },
    {
      "name": "Custom Diet Plan",
      "format": "60-min in-clinic or online",
      "price_hkd": 980,
      "includes": ["Full health history review", "Custom recipe (home-cooked or raw)", "Supplement guide", "30-day follow-up"]
    },
    {
      "name": "Weight Management Programme",
      "format": "3-month programme",
      "price_hkd": 1800,
      "includes": ["Monthly weigh-ins", "Progressive meal plan", "Progress tracking in Pawrd app"]
    }
  ],
  "common_concerns": [
    {"issue": "Obesity", "tip": "Feed measured portions — free-feeding causes overweight in 60% of pets."},
    {"issue": "Food Allergies", "tip": "Novel protein elimination diet is the gold standard for diagnosis."},
    {"issue": "Kidney Disease", "tip": "Phosphorus restriction and moisture-rich diets slow progression significantly."},
    {"issue": "Puppy/Kitten Growth", "tip": "Large-breed puppies need controlled calcium — not just 'more food'."}
  ]
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 5,
		},
		{
			Category:    "surgery_care",
			Name:        "Surgery Care",
			NameZh:      "手術護理",
			Icon:        "cross.vial.fill",
			ColorHex:    "#E91E63",
			Description: "Pre-operative preparation, post-operative monitoring, and wound care guidance. Covers spay/neuter, orthopaedic, soft tissue, and emergency procedures.",
			DescZh:      "術前準備、術後監護及傷口護理指引，涵蓋絕育、骨科、軟組織及緊急手術。",
			ContentJSON: `{
  "overview": "Surgical procedures require careful preparation and attentive aftercare. Pawrd's surgery care guide walks you through every stage — from pre-op blood tests to wound healing milestones — so you know exactly what to expect.",
  "pre_op_checklist": [
    "Fasting: No food 8–12 hours before surgery (water is usually fine until 2–4 hours before).",
    "Pre-anaesthetic blood test recommended for all pets (mandatory for seniors).",
    "Inform the vet of all medications and supplements your pet is taking.",
    "Arrange transport — your pet will be groggy after anaesthesia.",
    "Prepare a warm, quiet recovery space at home."
  ],
  "common_surgeries": [
    {"name": "Spay (Female)", "duration": "45–90 min", "recovery": "10–14 days", "price_hkd": "2500–4500"},
    {"name": "Neuter (Male)", "duration": "20–40 min", "recovery": "7–10 days", "price_hkd": "1500–3000"},
    {"name": "Orthopaedic (TPLO)", "duration": "90–120 min", "recovery": "8–12 weeks", "price_hkd": "15000–25000"},
    {"name": "Soft Tissue (Tumour removal)", "duration": "Varies", "recovery": "2–3 weeks", "price_hkd": "3000–8000"},
    {"name": "Foreign Body Removal", "duration": "60–90 min", "recovery": "1–2 weeks", "price_hkd": "5000–12000"}
  ],
  "post_op_care": [
    "Keep the incision site clean and dry for at least 10 days.",
    "Use an E-collar (cone) to prevent licking — licking delays healing and causes infection.",
    "Restrict activity: no running, jumping, or stairs until cleared by the vet.",
    "Monitor for redness, swelling, discharge, or odour at the wound site.",
    "Return for suture removal as scheduled (usually 10–14 days post-op)."
  ],
  "when_to_call_the_vet": ["Bleeding that does not stop", "Vomiting more than 3 times post-op", "Extreme lethargy beyond 24 hours", "Wound opens or shows pus", "Pet refuses to eat for more than 48 hours"]
}`,
			Provider:  "Pawrd Health",
			Contact:   "",
			IsActive:  true,
			SortOrder: 6,
		},
	}

	for _, svc := range services {
		var existing models.MedicalService
		if err := db.Where("category = ?", svc.Category).First(&existing).Error; err == nil {
			continue // already seeded
		}
		if err := db.Create(&svc).Error; err != nil {
			log.Printf("Failed to seed medical service %s: %v", svc.Category, err)
		} else {
			log.Printf("Seeded medical service: %s", svc.Category)
		}
	}
}
