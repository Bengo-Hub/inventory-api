package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

type eventCapacityDef struct {
	SKU           string
	TotalCapacity int
	EventStartAt  time.Time
	EventEndAt    time.Time
	EventVenue    string
}

// eventCapacityDefs sets capacity and schedule on the SERVICE-type event items in catalogItemDefs.
// Dates are fixed in the near future relative to 2026-05-28 for demo repeatability.
var eventCapacityDefs = []eventCapacityDef{
	{
		"EVT-JAZ-001",
		80,
		time.Date(2026, 6, 7, 19, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 7, 23, 0, 0, 0, time.UTC),
		"Urban Loft Rooftop, Westlands, Nairobi",
	},
	{
		"EVT-BAR-001",
		16,
		time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 14, 13, 0, 0, 0, time.UTC),
		"Urban Loft Roastery Corner, Ground Floor",
	},
	{
		"EVT-WIN-001",
		40,
		time.Date(2026, 6, 20, 18, 30, 0, 0, time.UTC),
		time.Date(2026, 6, 20, 21, 30, 0, 0, time.UTC),
		"Urban Loft Private Dining Room",
	},
	{
		"EVT-BRN-001",
		60,
		time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 21, 14, 0, 0, 0, time.UTC),
		"Urban Loft Main Hall & Terrace",
	},
	{
		"EVT-MIX-001",
		20,
		time.Date(2026, 6, 27, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 27, 18, 0, 0, 0, time.UTC),
		"Urban Loft Bar, First Floor",
	},
	{
		"EVT-QUZ-001",
		100,
		time.Date(2026, 7, 3, 18, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 3, 22, 0, 0, 0, time.UTC),
		"Urban Loft Main Hall",
	},
}

// seedEventCapacity sets total_capacity, event_start_at, event_end_at, event_venue on
// the SERVICE-type event items that were created by seedItems.
func seedEventCapacity(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, def := range eventCapacityDefs {
		itemID := itemUUID(tenantID, def.SKU)
		venue := def.EventVenue

		if _, err := client.Item.UpdateOneID(itemID).
			SetNillableTotalCapacity(&def.TotalCapacity).
			SetNillableBookedCapacity(intPtr(0)).
			SetNillableEventStartAt(&def.EventStartAt).
			SetNillableEventEndAt(&def.EventEndAt).
			SetNillableEventVenue(&venue).
			Save(ctx); err != nil {
			return fmt.Errorf("set event capacity for %s: %w", def.SKU, err)
		}
		log.Printf("event capacity set: %s — %d seats @ %s", def.SKU, def.TotalCapacity, def.EventVenue)
	}
	return nil
}

func intPtr(i int) *int { return &i }
