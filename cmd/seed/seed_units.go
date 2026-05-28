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
}

func unitUUID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:global:unit:"+name))
}

func seedUnits(ctx context.Context, client *ent.Client) error {
	for _, u := range unitDefs {
		id := unitUUID(u.Name)
		exists, err := client.Unit.Query().Where(entunit.IDEQ(id)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check unit %s: %w", u.Name, err)
		}
		if exists {
			if _, err := client.Unit.UpdateOneID(id).
				SetAbbreviation(u.Abbreviation).
				SetType(u.UnitType).
				Save(ctx); err != nil {
				return fmt.Errorf("update unit %s: %w", u.Name, err)
			}
			continue
		}
		if _, err := client.Unit.Create().
			SetID(id).
			SetName(u.Name).
			SetAbbreviation(u.Abbreviation).
			SetType(u.UnitType).
			SetIsActive(true).
			Save(ctx); err != nil {
			return fmt.Errorf("create unit %s: %w", u.Name, err)
		}
		log.Printf("unit created: %s (%s)", u.Name, u.UnitType)
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
