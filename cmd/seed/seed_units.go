package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entunit "github.com/bengobox/inventory-service/internal/ent/unit"
)

type unitDef struct {
	Name         string
	Abbreviation string
	UnitType     string
	// Outlet use_cases the unit is relevant to (matches Unit.use_cases semantics:
	// empty = universal, shown for every vertical). Vertical-specific serving /
	// dispensing units are tagged so a retail outlet's unit picker isn't cluttered
	// with TOT/POT and a café's isn't cluttered with TABLET/STRIP.
	UseCases []string
}

var hospitalityServing = []string{"hospitality", "quick_service"}
var pharmacyDispensing = []string{"pharmacy"}
var serviceOnly = []string{"services"}
var serviceAndHospitality = []string{"services", "hospitality"} // bookable/billable time & tickets

var unitDefs = []unitDef{
	{"PIECE", "pc", "count", nil},
	{"CUP", "cup", "volume", hospitalityServing},
	{"SERVING", "srv", "count", hospitalityServing},
	{"BOWL", "bowl", "count", hospitalityServing},
	{"PLATE", "plate", "count", hospitalityServing},
	{"SLICE", "slice", "count", hospitalityServing},
	{"KG", "kg", "weight", nil},
	{"GRAM", "g", "weight", nil},
	{"LITRE", "L", "volume", nil},
	{"ML", "ml", "volume", nil},
	{"BOX", "box", "count", nil},
	{"BOTTLE", "btl", "count", nil},
	{"SHOT", "shot", "volume", hospitalityServing},
	{"PACK", "pack", "count", nil},
	{"BAG", "bag", "count", nil},
	{"TICKET", "tkt", "count", serviceAndHospitality},
	{"PORTION", "ptn", "count", hospitalityServing},
	// Sellable / serving units (previously auto-created on import without a type).
	{"PAIR", "pair", "count", nil},
	{"POT", "pot", "count", hospitalityServing},
	{"TIN", "tin", "count", nil},
	{"QTR", "qtr", "count", hospitalityServing},
	{"PKT", "pkt", "count", nil},
	{"COMBO", "combo", "count", hospitalityServing},
	{"FULL", "full", "count", hospitalityServing},
	{"GLS", "gls", "volume", hospitalityServing},
	{"TOT", "tot", "volume", hospitalityServing},
	// Pharmacy dispensing units.
	{"TABLET", "tab", "count", pharmacyDispensing},
	{"CAPSULE", "cap", "count", pharmacyDispensing},
	{"STRIP", "strip", "count", pharmacyDispensing},
	{"SACHET", "sct", "count", pharmacyDispensing},
	{"VIAL", "vial", "count", pharmacyDispensing},
	// Service / time-based units. Type "service" (percentage/effort-based) and "time"
	// (duration-based) let the document item form show service-appropriate units for
	// SERVICE items — distinct from the measurement units used for GOODS. See treasury-ui
	// unit-kind convention (lib/api/inventory unitKind). Tags mirror the live prod
	// data: time units are bookable in services + hospitality (room nights, venue
	// hours); pure effort/billing units are services-only. Treasury document pickers
	// pass no use_case so they always see everything regardless of tags.
	{"DAY", "day", "time", serviceAndHospitality},
	{"HOUR", "hour", "time", serviceAndHospitality},
	{"WEEK", "week", "time", serviceAndHospitality},
	{"MONTH", "month", "time", serviceAndHospitality},
	{"YEAR", "year", "time", serviceAndHospitality},
	{"PERCENT", "%", "service", serviceOnly},
	{"PROJECT", "project", "service", serviceOnly},
	{"MILESTONE", "milestone", "service", serviceOnly},
	{"SESSION", "session", "service", serviceOnly},
	{"VISIT", "visit", "service", serviceOnly},
	{"UNIT", "unit", "service", serviceOnly},
	{"LUMPSUM", "lumpsum", "service", serviceOnly},
}

func unitUUID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:global:unit:"+name))
}

func seedUnits(ctx context.Context, client *ent.Client) error {
	for _, u := range unitDefs {
		// Reconcile by NAME (not the deterministic ID) so units auto-created on import — which
		// have random IDs and an empty/"-" type — are fixed in place instead of duplicated.
		existing, err := client.Unit.Query().Where(entunit.NameEQ(u.Name)).First(ctx)
		if err == nil {
			upd := client.Unit.UpdateOneID(existing.ID).
				SetAbbreviation(u.Abbreviation).
				SetType(u.UnitType).
				SetIsActive(true)
			if len(u.UseCases) > 0 {
				upd = upd.SetUseCases(u.UseCases)
			} else {
				upd = upd.ClearUseCases()
			}
			if _, err := upd.Save(ctx); err != nil {
				return fmt.Errorf("update unit %s: %w", u.Name, err)
			}
			continue
		}
		if !ent.IsNotFound(err) {
			return fmt.Errorf("check unit %s: %w", u.Name, err)
		}
		create := client.Unit.Create().
			SetID(unitUUID(u.Name)).
			SetName(u.Name).
			SetAbbreviation(u.Abbreviation).
			SetType(u.UnitType).
			SetIsActive(true)
		if len(u.UseCases) > 0 {
			create = create.SetUseCases(u.UseCases)
		}
		if _, err := create.Save(ctx); err != nil {
			return fmt.Errorf("create unit %s: %w", u.Name, err)
		}
		log.Printf("unit created: %s (%s)", u.Name, u.UnitType)
	}
	// Retire the legacy placeholder "-" unit so it stops appearing in unit pickers.
	if n, err := client.Unit.Update().Where(entunit.NameEQ("-")).SetIsActive(false).Save(ctx); err != nil {
		log.Printf("[WARN] deactivate placeholder unit: %v", err)
	} else if n > 0 {
		log.Printf("deactivated %d placeholder \"-\" unit(s)", n)
	}
	return nil
}

func resolveUnitIDs(ctx context.Context, client *ent.Client) (map[string]uuid.UUID, error) {
	units, err := client.Unit.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query units: %w", err)
	}
	m := make(map[string]uuid.UUID, len(units))
	for _, u := range units {
		m[u.Name] = u.ID
	}
	return m, nil
}
