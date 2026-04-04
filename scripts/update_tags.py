import sqlite3
import os

# Connect to the SQLite database
# Using absolute path to ensure correct file is accessed
db_path = '/Users/vfzzz/Downloads/Pawrd_Backend_Repo/pet_insurance.db'

if not os.path.exists(db_path):
    print(f"Error: Database file not found at {db_path}")
    exit(1)

conn = sqlite3.connect(db_path)
cursor = conn.cursor()

# Mapping of insurance plan names to their descriptive tags
tag_mapping = {
    # bolttech Plans
    'Pet Care - Plan 1': '#BudgetStarter #FixedPremium',
    'Pet Care - Plan 2': '#MidTierBalanced #SurgicalProtection',
    'Pet Care - Plan 3': '#MaxPetCare #ComprehensiveBasic',
    
    # MSIG Plans
    'HappyTail - Dog Standard Plan': '#SurgicalSpecialist #EarlyEnrollmentReward',
    'HappyTail - Dog Premier Plan': '#HereditarySupport #MidTierSurgery',
    'HappyTail - Dog Ultimate Plan': '#HighLimitSurgical #LifetimeProtection',
    'HappyTail - Cat Plan': '#FelineFocus #NoSubLimitSurgery',
    
    # OneDegree Plans
    'Essential Plan': '#NoSubLimitEntry #HospitalizationFocus',
    'Plus Plan': '#ValueChoice #ConsultationIncluded',
    'Ultra Plan': '#HKHighestLimit #FlexibleMedical',
    'Prestige Plan': '#AdvancedDiagnostics #MRICover',
    
    # Blue Cross Plans
    'Love Pet - Type C': '#OverseasLiability #NoMicrochipForCats',
    'Love Pet - Type B': '#EmergencyBoarding #FuneralSupport',
    'Love Pet - Type A': '#BehavioralTherapy #MaximumMedical',
    'Love Pet Outpatient - Sharing Plan': '#MultiPetSharing #VetVisitFocus',
    'Love Pet Outpatient - Basic Plan': '#VetVisitFocus #OutpatientFocus',
    
    # Prudential Plans
    'PRUChoice Furkid Care - A': '#HighLiability #TravelDelaySupport',
    'PRUChoice Furkid Care - B': '#AdvancedImaging #WaitingPeriodWaiver'
}

print("Starting database update...")

# Iterate through the mapping and update the database
updated_count = 0
for plan_name, tags in tag_mapping.items():
    cursor.execute(
        "UPDATE product SET tag = ? WHERE insurance_name = ?", 
        (tags, plan_name)
    )
    
    if cursor.rowcount > 0:
        updated_count += 1
        # print(f"Updated '{plan_name}'")
    else:
        print(f"Warning: Product '{plan_name}' not found.")

# Save (commit) the changes and close the connection
conn.commit()
conn.close()

print(f"Database updated successfully. Updated {updated_count} products.")
