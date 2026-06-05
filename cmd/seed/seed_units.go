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
}

var unitDefs = []unitDef{
	{"PIECE", "pc", "count"},
	{"CUP", "cup", "volume"},
	{"SERVING", "srv", "count"},
	{"BOWL", "bowl", "count"},
	{"PLATE", "plate", "count"},
	{"SLICE", "slice", "count"},
	{"KG", "kg", "weight"},
	{"GRAM", "g", "weight"},
	{"LITRE", "L", "volume"},
	{"ML", "ml", "volume"},
	{"BOX", "box", "count"},
	{"BOTTLE", "btl", "count"},
	{"SHOT", "shot", "volume"},
	{"PACK", "pack", "count"},
	{"BAG", "bag", "count"},
	{"TICKET", "tkt", "count"},
	{"PORTION", "ptn", "count"},
	// Sellable / serving units (previously auto-created on import without a type).
	{"PAIR", "pair", "count"},
	{"POT", "pot", "count"},
	{"TIN", "tin", "count"},
	{"QTR", "qtr", "count"},
	{"PKT", "pkt", "count"},
	{"COMBO", "combo", "count"},
	{"FULL", "full", "count"},
	{"GLS", "gls", "volume"},
	{"TOT", "tot", "volume"},
	{"DAY", "day", "other"},
	{"HOUR", "hour", "other"},
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
			if _, err := client.Unit.UpdateOneID(existing.ID).
				SetAbbreviation(u.Abbreviation).
				SetType(u.UnitType).
				SetIsActive(true).
				Save(ctx); err != nil {
				return fmt.Errorf("update unit %s: %w", u.Name, err)
			}
			continue
		}
		if !ent.IsNotFound(err) {
			return fmt.Errorf("check unit %s: %w", u.Name, err)
		}
		if _, err := client.Unit.Create().
			SetID(unitUUID(u.Name)).
			SetName(u.Name).
			SetAbbreviation(u.Abbreviation).
			SetType(u.UnitType).
			SetIsActive(true).
			Save(ctx); err != nil {
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
