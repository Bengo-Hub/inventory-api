package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/druginteractionrule"
)

// drugInteractionRuleDef is a curated, platform-wide (is_global=true) drug-drug-interaction
// pair for the pharmacy (DAWA use-case) clinical safety check. This is an in-house starter
// set of well-known, clinically significant interactions — not a licensed clinical database.
// Item.drug_class is the join key: any two dispensed items whose drug_class matches
// (ClassA, ClassB) here (in either order) trigger the interaction check.
type drugInteractionRuleDef struct {
	ClassA, ClassB         string
	Severity               string // minor | moderate | major | contraindicated
	Description            string
	ClinicalRecommendation string
}

var drugInteractionRuleDefs = []drugInteractionRuleDef{
	{"maoi", "ssri", "contraindicated", "Risk of serotonin syndrome.", "Avoid combination; allow washout period per product labeling before switching."},
	{"maoi", "snri", "contraindicated", "Risk of serotonin syndrome.", "Avoid combination; allow washout period before switching."},
	{"opioid", "benzodiazepine", "contraindicated", "Additive CNS and respiratory depression.", "Avoid combination unless alternatives are inadequate; use lowest effective doses and monitor closely."},
	{"ace_inhibitor", "potassium_sparing_diuretic", "major", "Increased risk of hyperkalemia.", "Monitor serum potassium and renal function."},
	{"warfarin", "nsaid", "major", "Increased bleeding risk (anticoagulant + antiplatelet/GI mucosal effect).", "Avoid if possible; monitor INR and for signs of bleeding."},
	{"warfarin", "aspirin", "major", "Increased bleeding risk.", "Avoid unless specifically indicated (e.g. mechanical valve); monitor INR closely."},
	{"antifungal_azole", "warfarin", "major", "Azole antifungals inhibit warfarin metabolism, increasing INR.", "Monitor INR closely; adjust warfarin dose."},
	{"antifungal_azole", "statin", "major", "CYP3A4 inhibition increases statin levels — rhabdomyolysis risk.", "Consider temporary statin suspension or a non-interacting statin."},
	{"macrolide", "statin", "major", "CYP3A4 inhibition increases statin levels — rhabdomyolysis risk.", "Consider temporary statin suspension or dose reduction."},
	{"digoxin", "macrolide", "major", "Macrolides increase digoxin absorption/levels — toxicity risk.", "Monitor digoxin levels; watch for toxicity symptoms."},
	{"amiodarone", "digoxin", "major", "Amiodarone increases digoxin levels — toxicity risk.", "Reduce digoxin dose and monitor levels."},
	{"digoxin", "diuretic_loop", "moderate", "Loop-diuretic-induced hypokalemia potentiates digoxin toxicity.", "Monitor potassium and digoxin levels."},
	{"ace_inhibitor", "arb", "moderate", "Dual RAAS blockade increases hyperkalemia and renal impairment risk.", "Avoid routine combination; monitor renal function and potassium if used."},
	{"beta_blocker", "calcium_channel_blocker", "major", "Additive bradycardia and AV-node suppression — risk of heart block.", "Use with caution; monitor heart rate and ECG."},
	{"ssri", "nsaid", "moderate", "Increased risk of GI bleeding.", "Consider gastroprotection (e.g. PPI) if combination is necessary."},
	{"ssri", "triptan", "major", "Risk of serotonin syndrome.", "Use with caution; educate patient on serotonin syndrome symptoms."},
	{"ssri", "warfarin", "moderate", "Increased bleeding risk (platelet function effect).", "Monitor INR and for bleeding."},
	{"muscle_relaxant", "opioid", "major", "Additive CNS and respiratory depression.", "Use lowest effective doses; monitor closely, especially in elderly patients."},
	{"nsaid", "sulfonylurea", "moderate", "NSAIDs can potentiate hypoglycemic effect.", "Monitor blood glucose."},
	{"ace_inhibitor", "lithium", "major", "Reduced lithium clearance — toxicity risk.", "Monitor lithium levels; adjust dose as needed."},
	{"lithium", "nsaid", "major", "Reduced lithium clearance — toxicity risk.", "Avoid if possible; monitor lithium levels."},
	{"diuretic_loop", "nsaid", "moderate", "NSAIDs blunt diuretic effect and increase renal impairment risk.", "Monitor renal function and blood pressure."},
	{"corticosteroid", "nsaid", "moderate", "Additive GI ulceration/bleeding risk.", "Consider gastroprotection if combination is necessary."},
	{"heparin", "nsaid", "major", "Increased bleeding risk.", "Avoid if possible; monitor for bleeding."},
	{"clopidogrel", "proton_pump_inhibitor", "moderate", "Some PPIs (e.g. omeprazole) reduce clopidogrel activation via CYP2C19 inhibition.", "Consider a PPI with less CYP2C19 interaction (e.g. pantoprazole) if acid suppression is needed."},
	{"macrolide", "theophylline", "major", "Macrolides inhibit theophylline metabolism — toxicity risk.", "Monitor theophylline levels; consider dose reduction."},
	{"fluoroquinolone", "theophylline", "major", "Fluoroquinolones inhibit theophylline metabolism — toxicity risk.", "Monitor theophylline levels; consider dose reduction."},
	{"antacid", "fluoroquinolone", "moderate", "Antacid cations chelate fluoroquinolones, reducing absorption.", "Separate dosing by at least 2 hours."},
	{"antacid", "tetracycline", "moderate", "Antacid cations chelate tetracyclines, reducing absorption.", "Separate dosing by at least 2 hours."},
	{"iron_supplement", "tetracycline", "moderate", "Iron chelates tetracyclines, reducing absorption of both.", "Separate dosing by at least 2-3 hours."},
	{"methotrexate", "nsaid", "major", "Reduced methotrexate clearance — toxicity risk.", "Avoid or use with close monitoring, especially at high-dose methotrexate."},
	{"methotrexate", "trimethoprim", "major", "Additive antifolate effect — bone marrow suppression risk.", "Avoid combination; monitor full blood count if unavoidable."},
	{"carbamazepine", "macrolide", "major", "Macrolides inhibit carbamazepine metabolism — toxicity risk.", "Monitor carbamazepine levels; consider an alternative antibiotic."},
	{"antifungal_azole", "phenytoin", "moderate", "Azoles can inhibit phenytoin metabolism — toxicity risk.", "Monitor phenytoin levels."},
	{"beta_blocker", "insulin", "moderate", "Beta-blockers can mask hypoglycemia symptoms (tachycardia, tremor).", "Counsel patient on alternative hypoglycemia symptoms (sweating); monitor glucose closely."},
	{"diuretic_thiazide", "lithium", "major", "Thiazides reduce lithium clearance — toxicity risk.", "Avoid if possible; monitor lithium levels closely."},
	{"antipsychotic", "fluoroquinolone", "major", "Additive QT-interval prolongation — torsades de pointes risk.", "Avoid combination if possible; obtain ECG monitoring if unavoidable."},
	{"ssri", "nsaid_aspirin_low_dose", "moderate", "Increased GI bleeding risk.", "Consider gastroprotection if combination is necessary."},
}

func drugInteractionRuleUUID(classA, classB string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:drug-interaction-rule:"+classA+"|"+classB))
}

// seedDrugInteractionRules idempotently creates the platform-wide (tenant_id=uuid.Nil,
// is_global=true) curated drug-interaction pairs. Reconciles by deterministic ID so reseeding
// is safe; never deletes tenant-created override rows (additive-only, matching the platform's
// seed convention — see feedback_workflow_rules / RBAC-seed additive-only precedent).
func seedDrugInteractionRules(ctx context.Context, client *ent.Client) error {
	for _, def := range drugInteractionRuleDefs {
		classA, classB := def.ClassA, def.ClassB
		if strings.Compare(classA, classB) > 0 {
			classA, classB = classB, classA
		}
		id := drugInteractionRuleUUID(classA, classB)
		_, err := client.DrugInteractionRule.Get(ctx, id)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.DrugInteractionRule.Create().
				SetID(id).
				SetTenantID(uuid.Nil).
				SetIsGlobal(true).
				SetClassA(classA).
				SetClassB(classB).
				SetSeverity(druginteractionrule.Severity(def.Severity)).
				SetDescription(def.Description).
				SetClinicalRecommendation(def.ClinicalRecommendation).
				SetSource("in-house-v1").
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return fmt.Errorf("create drug interaction rule %s+%s: %w", classA, classB, createErr)
			}
		case err != nil:
			return fmt.Errorf("check drug interaction rule %s+%s: %w", classA, classB, err)
		default:
			if _, updateErr := client.DrugInteractionRule.UpdateOneID(id).
				SetSeverity(druginteractionrule.Severity(def.Severity)).
				SetDescription(def.Description).
				SetClinicalRecommendation(def.ClinicalRecommendation).
				SetSource("in-house-v1").
				SetIsActive(true).
				Save(ctx); updateErr != nil {
				return fmt.Errorf("update drug interaction rule %s+%s: %w", classA, classB, updateErr)
			}
		}
	}
	log.Printf("drug interaction rules seeded: %d pairs", len(drugInteractionRuleDefs))
	return nil
}
