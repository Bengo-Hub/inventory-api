package stock

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	entconsumptionline "github.com/bengobox/inventory-service/internal/ent/consumptionline"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
)

// How the stock history shows a sale that was later undone. The compensating ConsumptionLine is
// its own ledger row (Sale Voided, Sale Edited, Sale Reversal or Customer Return), and the
// original Sold row is marked with what happened to it (status voided, partially_returned...) so
// a voided receipt never reads as a plain sale. Voids, edits and reversals undo the sale, so they
// net out of Total Sold; only customer returns count as sell returns.

// reversalMeta is what a compensating Consumption row records about itself.
type reversalMeta struct {
	Kind   string
	Reason string
}

// reversalMetaByConsumption loads the kind and reason of these compensating Consumption rows in
// one query. Rows without stored metadata (written before it existed) resolve their kind from the
// idempotency key, which still tells a Delete Sale or a customer return apart. Best effort: a
// lookup failure leaves the map empty and every reversal reads as a plain Sale Reversal.
func (s *Service) reversalMetaByConsumption(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, into map[uuid.UUID]reversalMeta) {
	want := make([]uuid.UUID, 0, len(ids))
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if _, ok := into[id]; ok || seen[id] {
			continue
		}
		seen[id] = true
		want = append(want, id)
	}
	for start := 0; start < len(want); start += inChunk {
		end := min(start+inChunk, len(want))
		rows, err := s.client.Consumption.Query().
			Where(entconsumption.TenantID(tenantID), entconsumption.IDIn(want[start:end]...)).
			Select(entconsumption.FieldID, entconsumption.FieldIdempotencyKey, entconsumption.FieldMetadata).
			All(ctx)
		if err != nil {
			return
		}
		for _, r := range rows {
			key := ""
			if r.IdempotencyKey != nil {
				key = *r.IdempotencyKey
			}
			kind, _ := r.Metadata["kind"].(string)
			reason, _ := r.Metadata["reason"].(string)
			into[r.ID] = reversalMeta{Kind: ReversalKind(kind, key, reason), Reason: reason}
		}
	}
}

// inChunk bounds IN lists so a long-selling item never builds one oversized statement.
const inChunk = 1000

// reversalRowType maps a reversal kind to its ledger type and label. Pure for tests.
func reversalRowType(kind string) (mvType, label string) {
	switch kind {
	case ReversalKindReturn:
		return "sell_return", "Customer Return"
	case ReversalKindVoid:
		return "sale_reversal", "Sale Voided"
	case ReversalKindEdit:
		return "sale_reversal", "Sale Edited"
	}
	return "sale_reversal", "Sale Reversal"
}

// saleRef is one Sold ledger row waiting to learn what later happened to it.
type saleRef struct {
	idx int
	key string // order id + warehouse: a reversal puts stock back where the sale took it
	qty float64
	at  time.Time
}

// saleCorrection is how much of one Sold row was undone, and the status that describes it.
type saleCorrection struct {
	Quantity float64
	Status   string
}

// kindOrder is the order corrections are allocated in and the tie-break for the status.
var kindOrder = []string{ReversalKindVoid, ReversalKindEdit, ReversalKindReversal, ReversalKindReturn}

var kindStatus = map[string]string{
	ReversalKindVoid:     "voided",
	ReversalKindEdit:     "edited",
	ReversalKindReversal: "reversed",
	ReversalKindReturn:   "returned",
}

// allocateSaleCorrections spreads each order's reversed quantity (per kind) over its Sold rows,
// oldest first, and names each row's status after the kind that undid most of it: "voided" when
// fully undone by a void, "partially_returned" when a return took back part of it. A row with
// nothing undone gets no entry. Pure for tests.
func allocateSaleCorrections(sales []saleRef, reversed map[string]map[string]float64) map[int]saleCorrection {
	byKey := map[string][]saleRef{}
	for _, sr := range sales {
		byKey[sr.key] = append(byKey[sr.key], sr)
	}
	out := map[int]saleCorrection{}
	for key, refs := range byKey {
		left := map[string]float64{}
		for k, q := range reversed[key] {
			left[k] = q
		}
		if len(left) == 0 {
			continue
		}
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].at.Before(refs[j].at) })
		for _, sr := range refs {
			need := sr.qty
			share := map[string]float64{}
			for _, k := range kindOrder {
				if need <= qtyEpsilon {
					break
				}
				take := min(left[k], need)
				if take > 0 {
					share[k] += take
					left[k] -= take
					need -= take
				}
			}
			total := sr.qty - need
			if total <= qtyEpsilon {
				continue
			}
			dominant := ""
			for _, k := range kindOrder {
				if dominant == "" || share[k] > share[dominant]+qtyEpsilon {
					dominant = k
				}
			}
			status := kindStatus[dominant]
			if need > qtyEpsilon {
				status = "partially_" + status
			}
			out[sr.idx] = saleCorrection{Quantity: round4(total), Status: status}
		}
	}
	return out
}

// qtyEpsilon absorbs float noise from ratio-scaled partial reversals.
const qtyEpsilon = 1e-6

// annotateSoldRows marks every Sold row with what later happened to it. The reversals are read
// for the orders on the page regardless of the date window, so a sale in range still shows as
// voided when the void fell after the range ends. One query per chunk of orders, served by the
// (tenant_id, order_id) index.
func (s *Service) annotateSoldRows(ctx context.Context, tenantID, itemID uuid.UUID, warehouseID *uuid.UUID, rows []MovementRow, saleOrders map[int]uuid.UUID, metas map[uuid.UUID]reversalMeta) {
	if len(saleOrders) == 0 {
		return
	}
	orderIDs := make([]uuid.UUID, 0, len(saleOrders))
	seen := map[uuid.UUID]bool{}
	for _, oid := range saleOrders {
		if !seen[oid] {
			seen[oid] = true
			orderIDs = append(orderIDs, oid)
		}
	}

	type revLine struct {
		orderID, consumptionID uuid.UUID
		warehouse              string
		qty                    float64
	}
	var lines []revLine
	for start := 0; start < len(orderIDs); start += inChunk {
		end := min(start+inChunk, len(orderIDs))
		preds := []predicate.ConsumptionLine{
			entconsumptionline.TenantID(tenantID),
			entconsumptionline.OrderIDIn(orderIDs[start:end]...),
			entconsumptionline.IngredientItemID(itemID),
			entconsumptionline.Theoretical(false),
			entconsumptionline.Or(entconsumptionline.Reason(reversalReason), entconsumptionline.QuantityLT(0)),
		}
		if warehouseID != nil {
			preds = append(preds, entconsumptionline.WarehouseID(*warehouseID))
		}
		cls, err := s.client.ConsumptionLine.Query().Where(preds...).
			Select(entconsumptionline.FieldOrderID, entconsumptionline.FieldConsumptionID,
				entconsumptionline.FieldWarehouseID, entconsumptionline.FieldQuantity).
			All(ctx)
		if err != nil {
			return
		}
		for _, c := range cls {
			lines = append(lines, revLine{orderID: c.OrderID, consumptionID: c.ConsumptionID, warehouse: warehouseKey(c.WarehouseID), qty: c.Quantity})
		}
	}
	if len(lines) == 0 {
		return
	}

	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.consumptionID)
	}
	s.reversalMetaByConsumption(ctx, tenantID, ids, metas)

	reversed := map[string]map[string]float64{}
	for _, l := range lines {
		kind := ReversalKindReversal
		if m, ok := metas[l.consumptionID]; ok {
			kind = m.Kind
		}
		key := l.orderID.String() + "|" + l.warehouse
		if reversed[key] == nil {
			reversed[key] = map[string]float64{}
		}
		q := l.qty
		if q < 0 {
			q = -q
		}
		reversed[key][kind] += q
	}

	sales := make([]saleRef, 0, len(saleOrders))
	for idx, oid := range saleOrders {
		sales = append(sales, saleRef{
			idx: idx, key: oid.String() + "|" + warehouseKey(rows[idx].WarehouseID),
			qty: -rows[idx].QuantityChange, at: rows[idx].OccurredAt,
		})
	}
	for idx, c := range allocateSaleCorrections(sales, reversed) {
		rows[idx].Status = c.Status
		rows[idx].ReversedQuantity = c.Quantity
	}
}

// statusLabels names each sale status for people.
var statusLabels = map[string]string{
	"voided": "Voided", "partially_voided": "Partly voided",
	"edited": "Edited", "partially_edited": "Partly edited",
	"reversed": "Reversed", "partially_reversed": "Partly reversed",
	"returned": "Returned", "partially_returned": "Partly returned",
}

// DisplayLabel is the row's label with what happened to a sale, e.g. "Sold (Voided)" or
// "Sold (Partly returned 1)". Exports use it so a voided receipt never prints as a plain sale.
func (m MovementRow) DisplayLabel() string {
	sl, ok := statusLabels[m.Status]
	if !ok {
		return m.Label
	}
	if strings.HasPrefix(m.Status, "partially_") {
		return fmt.Sprintf("%s (%s %s)", m.Label, sl, strconv.FormatFloat(m.ReversedQuantity, 'f', -1, 64))
	}
	return m.Label + " (" + sl + ")"
}

func warehouseKey(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
