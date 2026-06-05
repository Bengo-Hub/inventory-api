package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
	entqc "github.com/bengobox/inventory-service/internal/ent/qualitycheck"
)

func batchUUID(tenantID uuid.UUID, batchNumber string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:batch:%s:%s", tenantID, batchNumber)))
}

// seedProductionBatches creates a couple of detergent production batches so the
// manufacturing UI has realistic data: one completed (with a passing QC + costed
// raw-material rows) and one in-progress.
func seedProductionBatches(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	now := time.Now().UTC()
	actual148 := 148.0
	actual118 := 118.0

	seeds := []struct {
		BatchNumber  string
		RecipeSKU    string
		Planned      float64
		Actual       *float64
		Status       entpb.Status
		LaborCost    float64
		OverheadCost float64
		QCPass       bool
	}{
		{"BATCH-DET-0001", "FIN-DISH-001", 150, &actual148, entpb.StatusCompleted, 3000, 1200, true},
		{"BATCH-DET-0002", "FIN-HAND-001", 200, nil, entpb.StatusInProgress, 2500, 1000, false},
		{"BATCH-DET-0003", "FIN-MULT-001", 120, nil, entpb.StatusPlanned, 2000, 900, false},
		{"BATCH-DET-0004", "FIN-BLCH-001", 150, &actual118, entpb.StatusCompleted, 2200, 800, true},
	}

	for _, bs := range seeds {
		batchID := batchUUID(tenantID, bs.BatchNumber)
		recipeID := recipeUUID(tenantID, bs.RecipeSKU)
		if _, err := client.ProductionBatch.Get(ctx, batchID); err == nil {
			continue // idempotent
		}

		// locate the recipe def to explode its BOM into batch materials
		var rd *recipeDef
		for i := range recipeDefs {
			if recipeDefs[i].SKU == bs.RecipeSKU {
				rd = &recipeDefs[i]
				break
			}
		}

		create := client.ProductionBatch.Create().
			SetID(batchID).SetTenantID(tenantID).SetBatchNumber(bs.BatchNumber).
			SetRecipeID(recipeID).SetScheduledDate(now.AddDate(0, 0, -7)).
			SetPlannedQuantity(bs.Planned).SetLaborCost(bs.LaborCost).SetOverheadCost(bs.OverheadCost).
			SetStatus(bs.Status)
		switch bs.Status {
		case entpb.StatusInProgress:
			create = create.SetStartDate(now.AddDate(0, 0, -1))
		case entpb.StatusCompleted:
			create = create.SetStartDate(now.AddDate(0, 0, -2)).SetEndDate(now.AddDate(0, 0, -1))
		}
		if bs.Actual != nil {
			create = create.SetActualQuantity(*bs.Actual)
		}
		batch, err := create.Save(ctx)
		if err != nil {
			return fmt.Errorf("create batch %s: %w", bs.BatchNumber, err)
		}

		// Explode BOM → BatchRawMaterial rows with cost (item.cost_price × qty).
		// Planned batches have not consumed materials yet, so skip the explosion.
		ratio := 1.0
		if rd != nil && rd.OutputQty > 0 {
			ratio = bs.Planned / rd.OutputQty
		}
		var materialCost float64
		if rd != nil && bs.Status != entpb.StatusPlanned {
			for _, ing := range rd.Ingredients {
				rawItemID := itemUUID(tenantID, ing.RawSKU)
				qty := ing.Qty * ratio
				cost := 0.0
				if it, e := client.Item.Get(ctx, rawItemID); e == nil && it.CostPrice != nil {
					cost = *it.CostPrice * qty
				}
				materialCost += cost
				unitID := unitUUID(ing.UOM)
				if _, e := client.BatchRawMaterial.Create().
					SetTenantID(tenantID).SetProductionBatchID(batch.ID).SetItemID(rawItemID).
					SetQuantity(qty).SetCost(cost).SetNillableUnitID(&unitID).
					Save(ctx); e != nil {
					log.Printf("[WARN] batch material %s→%s: %v", bs.BatchNumber, ing.RawSKU, e)
				}
			}
		}

		// Completed batch: roll up unit cost.
		if bs.Status == entpb.StatusCompleted && bs.Actual != nil && *bs.Actual > 0 {
			uc := (materialCost + bs.LaborCost + bs.OverheadCost) / *bs.Actual
			_, _ = client.ProductionBatch.UpdateOneID(batch.ID).SetUnitCost(uc).Save(ctx)
		}

		// Passing QC for the completed batch.
		if bs.QCPass {
			qcID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:qc:"+batchID.String()))
			if _, e := client.QualityCheck.Create().
				SetID(qcID).SetTenantID(tenantID).SetProductionBatchID(batch.ID).
				SetResult(entqc.ResultPass).SetNotes("Passed standard QC (pH, viscosity, appearance)").
				SetCheckDate(now.AddDate(0, 0, -1)).
				Save(ctx); e != nil {
				log.Printf("[WARN] batch QC %s: %v", bs.BatchNumber, e)
			}
		}

		log.Printf("production batch seeded: %s (%s)", bs.BatchNumber, bs.Status)
	}
	return nil
}
