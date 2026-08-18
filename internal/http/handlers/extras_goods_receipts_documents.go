package handlers

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// GenerateGoodsReceiptPDF renders a branded GRN PDF for a PO receiving
// (GET /inventory/goods-receipts/{grnID}/pdf). Reuses the GRN's already-minted grn_number — the
// document number was issued at creation (documents.DocTypeGRN), so nothing is generated here.
//
//	@Summary      Generate a branded goods received note (GRN) PDF
//	@Tags         Procurement
//	@Produce      application/pdf
//	@Param        grnID  path      string  true  "GRN ID"
//	@Success      200    {file}    binary
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Failure      500    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/goods-receipts/{grnID}/pdf [get]
func (h *InventoryExtrasHandler) GenerateGoodsReceiptPDF(w http.ResponseWriter, r *http.Request) {
	if h.docSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "DOC_SVC_UNAVAILABLE", "Document service not configured")
		return
	}
	tenantID, g, ok := h.loadGRN(w, r)
	if !ok {
		return
	}
	format, ok := docFormatFromQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	lines, _ := h.orm.GoodsReceiptLine.Query().
		Where(entgrl.GoodsReceiptID(g.ID), entgrl.TenantID(tenantID)).All(ctx)

	// Originating PO: supplies the human PO number, the currency, and the per-line ORDERED
	// quantity the receiving clerk reconciles against.
	poNumber, currency := "", ""
	orderedByItem := map[uuid.UUID]float64{}
	orderedByLine := map[uuid.UUID]float64{}
	po, poErr := h.orm.PurchaseOrder.Query().
		Where(entpo.ID(g.PurchaseOrderID), entpo.TenantID(tenantID)).Only(ctx)
	if poErr == nil {
		poNumber, currency = po.PoNumber, po.Currency
		if poLines, e := h.orm.PurchaseOrderLine.Query().Where(entpoline.PoID(po.ID)).All(ctx); e == nil {
			for _, pl := range poLines {
				orderedByItem[pl.ItemID] = pl.QuantityOrdered
				orderedByLine[pl.ID] = pl.QuantityOrdered
			}
		}
	}

	supplierName := ""
	var supplierAddr []string
	supplierID := g.SupplierID
	if supplierID == nil && poErr == nil {
		supplierID = po.SupplierID
	}
	if supplierID != nil {
		if sup, e := h.orm.Supplier.Query().Where(entsupplier.ID(*supplierID)).Only(ctx); e == nil {
			supplierName = sup.Name
			if sup.Address != "" {
				supplierAddr = strings.Split(sup.Address, "\n")
			}
		}
	}

	warehouseName := ""
	warehouseID := g.WarehouseID
	if warehouseID == nil && poErr == nil {
		warehouseID = po.WarehouseID
	}
	if warehouseID != nil {
		if wh, e := h.orm.Warehouse.Query().Where(entwarehouse.ID(*warehouseID)).Only(ctx); e == nil {
			warehouseName = wh.Name
		}
	}

	// Item identifiers, so the document never prints raw item UUIDs.
	itemIDs := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	names, skus, units := map[uuid.UUID]string{}, map[uuid.UUID]string{}, map[uuid.UUID]string{}
	if items, e := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).WithUnits().All(ctx); e == nil {
		for _, it := range items {
			names[it.ID] = it.Name
			skus[it.ID] = it.Sku
			if it.Edges.Units != nil {
				units[it.ID] = it.Edges.Units.Abbreviation
			}
		}
	}

	items := make([]documents.GoodsReceiptDocLine, 0, len(lines))
	var total float64
	for _, l := range lines {
		amount := l.QuantityAccepted * l.UnitCost
		total += amount
		// SKU plus, when something was rejected, the reason — the GRN is the record of WHY.
		sub := skus[l.ItemID]
		if l.LotNumber != "" {
			sub = joinDetail(sub, "Lot "+l.LotNumber)
		}
		if l.QuantityRejected > 0 && l.RejectionReason != "" {
			sub = joinDetail(sub, "Rejected: "+l.RejectionReason)
		}
		ordered := ""
		if l.PurchaseOrderLineID != nil {
			if q, okq := orderedByLine[*l.PurchaseOrderLineID]; okq {
				ordered = formatQty(q)
			}
		}
		if ordered == "" {
			if q, okq := orderedByItem[l.ItemID]; okq {
				ordered = formatQty(q)
			}
		}
		items = append(items, documents.GoodsReceiptDocLine{
			Desc:        ifEmptyStr(names[l.ItemID], l.ItemID.String()),
			SubDesc:     sub,
			Unit:        units[l.ItemID],
			OrderedQty:  ordered,
			ReceivedQty: formatQty(l.QuantityReceived),
			AcceptedQty: formatQty(l.QuantityAccepted),
			RejectedQty: formatQty(l.QuantityRejected),
			UnitCost:    formatMoney(l.UnitCost),
			Amount:      formatMoney(amount),
		})
	}

	doc := documents.GoodsReceiptDoc{
		Branding:            h.docSvc.GetBranding(ctx, tenantID),
		GrnNumber:           g.GrnNumber,
		Date:                g.ReceivedDate.Format("02 January 2006"),
		Status:              string(g.Status),
		Currency:            currency,
		PurchaseOrderNumber: poNumber,
		SupplierName:        supplierName,
		SupplierAddr:        supplierAddr,
		WarehouseName:       warehouseName,
		Items:               items,
		Notes:               nonEmptyStrs(g.Notes),
	}
	// Only show a received value when the receipt actually captured costs — a zero-cost GRN
	// would otherwise print a misleading "0.00" grand total.
	if total > 0.0049 {
		doc.TotalReceivedValue = formatMoney(total)
	}
	doc.PreparedBy = h.resolveUserLabel(ctx, tenantID, g.ReceivedBy, "")

	var fileBytes []byte
	var err error
	switch format {
	case "csv":
		fileBytes, err = documents.RenderGoodsReceiptCSV(doc)
	case "xlsx":
		fileBytes, err = documents.RenderGoodsReceiptXLSX(doc)
	default:
		fileBytes, err = documents.RenderGoodsReceiptPDF(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOC_RENDER_FAILED", "Failed to render goods receipt document")
		return
	}
	writeDocFile(w, g.GrnNumber, format, fileBytes)
}

// joinDetail appends a detail fragment to a document sub-line, inserting the separator only
// when both sides are non-empty.
func joinDetail(base, extra string) string {
	base, extra = strings.TrimSpace(base), strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "  ·  " + extra
	}
}
