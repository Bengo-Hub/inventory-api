package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entunit "github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── Purchase Orders ──────────────────────────────────────────────────────────

type purchaseOrderDTO struct {
	ID                        uuid.UUID  `json:"id"`
	PONumber                  string     `json:"po_number"`
	SupplierName              string     `json:"supplier_name"`
	SupplierID                uuid.UUID  `json:"supplier_id"`
	WarehouseName             string     `json:"warehouse_name"`
	WarehouseID               uuid.UUID  `json:"warehouse_id"`
	Status                    string     `json:"status"`
	TotalAmount               float64    `json:"total_amount"`
	// OrderDate is the business date the order was raised — overrides CreatedAt for
	// display/reporting when set (nil means "use created_at", the pre-feature default).
	OrderDate                 *time.Time `json:"order_date"`
	ExpectedDate              *time.Time `json:"expected_date"`
	Notes                     string     `json:"notes"`
	PayTermDays               *int       `json:"pay_term_days"`
	AdditionalShippingCharges float64    `json:"additional_shipping_charges"`
	CreatedBy                 *uuid.UUID `json:"created_by,omitempty"`
	// CreatedByName is CreatedBy resolved to a display name — who raised this purchase order,
	// surfaced for the same "who did what" audit trail as adjustments/goods receipts.
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// effectivePODate returns the calendar day a purchase order counts toward on documents/reports:
// the staff-set order_date override when present, else created_at. Mirrors transfers'
// EffectiveTransferDate.
func effectivePODate(po *ent.PurchaseOrder) time.Time {
	if po.OrderDate != nil {
		return *po.OrderDate
	}
	return po.CreatedAt
}

// orderByEffectivePODate orders purchase orders by their effective date — order_date when set,
// else created_at — so a backdated PO groups with its real chronological neighbors instead of
// floating to the top of the list under today's date. Mirrors transfers.orderByEffectiveDate
// (ent's generated Asc/Desc only order by a single column, so a COALESCE-across-two-columns
// ORDER BY needs OrderExprFunc instead).
func orderByEffectivePODate(desc bool) entpurchaseorder.OrderOption {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return func(s *sql.Selector) {
		expr := "COALESCE(" + s.C(entpurchaseorder.FieldOrderDate) + ", " + s.C(entpurchaseorder.FieldCreatedAt) + ")"
		s.OrderExprFunc(func(b *sql.Builder) {
			b.WriteString(expr + " " + dir + ", " + s.C(entpurchaseorder.FieldCreatedAt) + " " + dir)
		})
	}
}

// ListPurchaseOrders handles GET /inventory/purchase-orders.
//
//	@Summary      List purchase orders
//	@Tags         Procurement
//	@Produce      json
//	@Param        search  query     string  false  "Filter by PO number or supplier name"
//	@Param        status  query     string  false  "Filter by status"
//	@Param        page    query     int     false  "Page number (1-based)"
//	@Param        limit   query     int     false  "Page size (max 100)"
//	@Success      200     {object}  map[string]interface{}
//	@Failure      400     {object}  map[string]string
//	@Failure      500     {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders [get]
func (h *InventoryExtrasHandler) ListPurchaseOrders(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)

	q := h.orm.PurchaseOrder.Query().Where(entpurchaseorder.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entpurchaseorder.StatusEQ(entpurchaseorder.Status(s)))
	}
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		q = q.Where(entpurchaseorder.Or(
			entpurchaseorder.PoNumberContainsFold(search),
			entpurchaseorder.HasSupplierWith(entsupplier.NameContainsFold(search)),
		))
	}
	if from, to, ok := parseCreatedAtRange(r); ok {
		// Match on the EFFECTIVE date (order_date when set, else created_at) — a backdated PO
		// filtered into "last month" must actually show up there, mirroring transfers'
		// EffectiveTransferDate filter treatment.
		q = q.Where(entpurchaseorder.Or(
			entpurchaseorder.And(entpurchaseorder.OrderDateNotNil(), entpurchaseorder.OrderDateGTE(from), entpurchaseorder.OrderDateLTE(to)),
			entpurchaseorder.And(entpurchaseorder.OrderDateIsNil(), entpurchaseorder.CreatedAtGTE(from), entpurchaseorder.CreatedAtLTE(to)),
		))
	}

	total, _ := q.Clone().Count(r.Context())
	orders, err := q.
		WithSupplier().
		WithWarehouse().
		Order(orderByEffectivePODate(true)).
		Limit(p.Limit).
		Offset(p.Offset).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list purchase orders")
		return
	}

	createdByIDs := make([]uuid.UUID, 0, len(orders))
	for _, o := range orders {
		if o.CreatedBy != nil {
			createdByIDs = append(createdByIDs, *o.CreatedBy)
		}
	}
	createdByNames := actorNamesByID(r.Context(), h.orm, tenantID, createdByIDs)

	result := make([]purchaseOrderDTO, 0, len(orders))
	for _, o := range orders {
		supplierName := ""
		if o.Edges.Supplier != nil {
			supplierName = o.Edges.Supplier.Name
		}
		warehouseName := ""
		if o.Edges.Warehouse != nil {
			warehouseName = o.Edges.Warehouse.Name
		}
		dto := purchaseOrderDTO{
			ID:                        o.ID,
			PONumber:                  o.PoNumber,
			SupplierName:              supplierName,
			SupplierID:                derefUUID(o.SupplierID),
			WarehouseName:             warehouseName,
			WarehouseID:               derefUUID(o.WarehouseID),
			Status:                    o.Status.String(),
			TotalAmount:               o.TotalAmount,
			Notes:                     o.Notes,
			PayTermDays:               o.PayTermDays,
			AdditionalShippingCharges: o.AdditionalShippingCharges,
			CreatedBy:                 o.CreatedBy,
			CreatedAt:                 o.CreatedAt,
		}
		if o.CreatedBy != nil {
			dto.CreatedByName = createdByNames[*o.CreatedBy]
		}
		if o.ExpectedDate != nil {
			dto.ExpectedDate = o.ExpectedDate
		}
		if o.OrderDate != nil {
			dto.OrderDate = o.OrderDate
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}

// GetPurchaseOrder handles GET /inventory/purchase-orders/{poID}.
//
//	@Summary      Get a purchase order with its line items
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  purchaseOrderDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID} [get]
func (h *InventoryExtrasHandler) GetPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		WithSupplier().
		WithWarehouse().
		WithLines().
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	supplierName := ""
	if po.Edges.Supplier != nil {
		supplierName = po.Edges.Supplier.Name
	}
	warehouseName := ""
	if po.Edges.Warehouse != nil {
		warehouseName = po.Edges.Warehouse.Name
	}

	// Fetch item names/SKUs for lines
	itemIDs := make([]uuid.UUID, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	itemNames := make(map[uuid.UUID]string)
	itemSKUs := make(map[uuid.UUID]string)
	itemCurrentPrice := make(map[uuid.UUID]*float64)
	if len(itemIDs) > 0 {
		items, _ := h.orm.Item.Query().
			Where(entitem.IDIn(itemIDs...)).
			All(r.Context())
		for _, it := range items {
			itemNames[it.ID] = it.Name
			itemSKUs[it.ID] = it.Sku
			itemCurrentPrice[it.ID] = it.MaxSellingPrice
		}
	}

	// Resolve unit abbreviations for lines that carry a unit_id, same lookup shape as
	// itemNames/itemSKUs above — PurchaseOrderLine has no Ent edge to Unit (mirrors item_id).
	unitIDs := make([]uuid.UUID, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		if l.UnitID != nil {
			unitIDs = append(unitIDs, *l.UnitID)
		}
	}
	unitAbbrs := make(map[uuid.UUID]string)
	if len(unitIDs) > 0 {
		units, _ := h.orm.Unit.Query().
			Where(entunit.IDIn(unitIDs...)).
			All(r.Context())
		for _, u := range units {
			unitAbbrs[u.ID] = u.Abbreviation
		}
	}

	type lineDTO struct {
		ID          uuid.UUID  `json:"id"`
		ItemID      uuid.UUID  `json:"item_id"`
		ItemName    string     `json:"item_name"`
		ItemSKU     string     `json:"item_sku"`
		Quantity    float64    `json:"quantity"`
		ReceivedQty float64    `json:"received_qty"`
		UnitCost    float64    `json:"unit_cost"`
		TotalCost   float64    `json:"total_cost"`
		UnitID      *uuid.UUID `json:"unit_id,omitempty"`
		Unit        string     `json:"unit,omitempty"`
		// Selling-price adjustment decided at order time — carried through and applied when this
		// line is actually received (see postGoodsReceiptCore). CurrentSellingPrice is context
		// only (the item's price as of now), so the PO form can show "currently X" next to the
		// input; it is never itself written anywhere.
		NewSellingPrice     *float64 `json:"new_selling_price,omitempty"`
		PriceScope          string   `json:"price_scope,omitempty"`
		CurrentSellingPrice *float64 `json:"current_selling_price,omitempty"`
		// Input VAT on the line (treasury tax code, the rate when ordered, price holds VAT).
		TaxCode          string  `json:"tax_code,omitempty"`
		TaxRate          float64 `json:"tax_rate,omitempty"`
		PriceIncludesTax bool    `json:"price_includes_tax,omitempty"`
	}

	lines := make([]lineDTO, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		var unitAbbr string
		if l.UnitID != nil {
			unitAbbr = unitAbbrs[*l.UnitID]
		}
		lines = append(lines, lineDTO{
			ID:                  l.ID,
			ItemID:              l.ItemID,
			ItemName:            itemNames[l.ItemID],
			ItemSKU:             itemSKUs[l.ItemID],
			Quantity:            l.QuantityOrdered,
			ReceivedQty:         l.QuantityReceived,
			UnitCost:            l.UnitPrice,
			TotalCost:           l.TotalPrice,
			UnitID:              l.UnitID,
			Unit:                unitAbbr,
			NewSellingPrice:     l.NewSellingPrice,
			PriceScope:          l.PriceScope,
			CurrentSellingPrice: itemCurrentPrice[l.ItemID],
			TaxCode:             l.TaxCode,
			TaxRate:             l.TaxRate,
			PriceIncludesTax:    l.PriceIncludesTax,
		})
	}

	type poDetailDTO struct {
		purchaseOrderDTO
		LineItems []lineDTO `json:"line_items"`
		// OpenReceipt is the order's unposted goods receipt, if any. While it is set the screen
		// shows "Awaiting receipt approval" (or offers to post it) instead of Amend/Cancel.
		OpenReceipt *openReceiptDTO `json:"open_receipt,omitempty"`
	}

	dto := purchaseOrderDTO{
		ID:                        po.ID,
		PONumber:                  po.PoNumber,
		SupplierName:              supplierName,
		SupplierID:                derefUUID(po.SupplierID),
		WarehouseName:             warehouseName,
		WarehouseID:               derefUUID(po.WarehouseID),
		Status:                    po.Status.String(),
		TotalAmount:               po.TotalAmount,
		Notes:                     po.Notes,
		PayTermDays:               po.PayTermDays,
		AdditionalShippingCharges: po.AdditionalShippingCharges,
		CreatedAt:                 po.CreatedAt,
	}
	if po.ExpectedDate != nil {
		dto.ExpectedDate = po.ExpectedDate
	}
	if po.OrderDate != nil {
		dto.OrderDate = po.OrderDate
	}

	writeJSON(w, http.StatusOK, poDetailDTO{purchaseOrderDTO: dto, LineItems: lines, OpenReceipt: h.openReceiptFor(r.Context(), tenantID, po.ID)})
}

// ─── Purchase Order Writes ────────────────────────────────────────────────────

type createPOLineInput struct {
	ItemID   uuid.UUID  `json:"item_id"`
	Quantity float64    `json:"quantity"`
	UnitCost float64    `json:"unit_cost"`
	UnitID   *uuid.UUID `json:"unit_id"` // FK to Unit — unit of measure ordered in (e.g. kg, box); optional
	// Selling-price adjustment decided at order time. Applied only when this line is actually
	// received (see postGoodsReceiptCore) — nothing changes at PO creation/amend time.
	NewSellingPrice *float64 `json:"new_selling_price,omitempty"`
	PriceScope      string   `json:"price_scope,omitempty"`
	// Input VAT: a treasury tax code (empty = no VAT claimed) and whether unit_cost already holds
	// the VAT. The code's rate is looked up and stored with the line.
	TaxCode          string `json:"tax_code,omitempty"`
	PriceIncludesTax bool   `json:"price_includes_tax,omitempty"`
}

// poLineTax is a PO line's checked tax code and the rate in force when the order was raised.
type poLineTax struct {
	code string
	rate float64
}

// resolvePOLineTaxes checks every line's tax code against the tenant's treasury tax codes, before
// any transaction opens. An unknown code is the caller's mistake (400); lines without a code claim
// no input VAT.
func (h *InventoryExtrasHandler) resolvePOLineTaxes(ctx context.Context, tenantID uuid.UUID, lines []createPOLineInput) ([]poLineTax, error) {
	out := make([]poLineTax, len(lines))
	cache := map[string]poLineTax{}
	for i, l := range lines {
		code := strings.TrimSpace(l.TaxCode)
		if code == "" {
			continue
		}
		if t, ok := cache[strings.ToUpper(code)]; ok {
			out[i] = t
			continue
		}
		canonical, rate, err := h.itemsSvc.TaxCodeRate(ctx, tenantID, code)
		if err != nil {
			return nil, err
		}
		t := poLineTax{code: canonical, rate: rate}
		cache[strings.ToUpper(code)] = t
		out[i] = t
	}
	return out, nil
}

type createPOInput struct {
	SupplierID                uuid.UUID           `json:"supplier_id"`
	WarehouseID               uuid.UUID           `json:"warehouse_id"`
	ExpectedDate              *string             `json:"expected_date"` // accepts "YYYY-MM-DD" or RFC3339
	// OrderDate lets staff record the order under a business date other than today — e.g. a PO
	// placed with the supplier by phone days ago, only entered into the system now. Accepts
	// "YYYY-MM-DD" or RFC3339, same as ExpectedDate/PurchaseReturn's DateReturned. Blank/omitted
	// leaves it unset — the PO reports under created_at, the pre-feature default.
	OrderDate                 *string             `json:"order_date"`
	Notes                     string              `json:"notes"`
	PayTermDays               *int                `json:"pay_term_days"`
	AdditionalShippingCharges float64             `json:"additional_shipping_charges"`
	LineItems                 []createPOLineInput `json:"line_items"`
}

// CreatePurchaseOrder handles POST /inventory/purchase-orders.
//
//	@Summary      Create a purchase order with line items
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      createPOInput  true  "Purchase order payload"
//	@Success      201   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders [post]
func (h *InventoryExtrasHandler) CreatePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req createPOInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.SupplierID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_SUPPLIER", "supplier_id is required")
		return
	}
	if req.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id is required")
		return
	}
	if len(req.LineItems) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_LINES", "at least one line item is required")
		return
	}
	lineTaxes, err := h.resolvePOLineTaxes(r.Context(), tenantID, req.LineItems)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TAX_CODE", err.Error())
		return
	}

	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to begin transaction")
		return
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var total float64
	for _, l := range req.LineItems {
		total += roundDecimal(l.Quantity * l.UnitCost)
	}
	total = roundDecimal(total)

	// Configurable per-tenant document number (e.g. PO-260625-000001). Falls back to the legacy
	// timestamp form only if the sequence service is unavailable, so creation never fails on it.
	poNumber := "PO-" + strings.ToUpper(tenantID.String()[:8]) + "-" + time.Now().Format("20060102150405")
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypePurchaseOrder); derr == nil && n != "" {
			poNumber = n
		} else if derr != nil {
			h.log.Warn("PO number sequence failed, using fallback", zap.Error(derr))
		}
	}
	poCreate := tx.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(req.SupplierID).
		SetWarehouseID(req.WarehouseID).
		SetPoNumber(poNumber).
		SetTotalAmount(total).
		SetNotes(req.Notes).
		SetNillablePayTermDays(req.PayTermDays).
		SetAdditionalShippingCharges(req.AdditionalShippingCharges)
	// Stamp the creator (the "Prepared By" on the PO document) from the authenticated user.
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok && claims.Subject != "" {
		if uid, perr := uuid.Parse(claims.Subject); perr == nil {
			poCreate.SetCreatedBy(uid)
		}
	}
	if req.ExpectedDate != nil && *req.ExpectedDate != "" {
		var expDate time.Time
		// Accept both "YYYY-MM-DD" (from date inputs) and RFC3339
		if t, parseErr := time.Parse("2006-01-02", *req.ExpectedDate); parseErr == nil {
			expDate = t
		} else if t, parseErr := time.Parse(time.RFC3339, *req.ExpectedDate); parseErr == nil {
			expDate = t
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "expected_date must be YYYY-MM-DD or RFC3339")
			return
		}
		poCreate = poCreate.SetExpectedDate(expDate)
	}
	if req.OrderDate != nil && strings.TrimSpace(*req.OrderDate) != "" {
		orderDate, ok := parseFlexibleDate(strings.TrimSpace(*req.OrderDate))
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "order_date must be YYYY-MM-DD or RFC3339")
			return
		}
		poCreate = poCreate.SetOrderDate(orderDate)
	}
	po, err := poCreate.Save(r.Context())
	if err != nil {
		h.log.Error("create purchase order failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order")
		return
	}

	for i, l := range req.LineItems {
		lc := tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).
			SetItemID(l.ItemID).
			SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).
			SetTotalPrice(roundDecimal(l.Quantity * l.UnitCost)).
			SetNillableUnitID(l.UnitID).
			SetTaxCode(lineTaxes[i].code).
			SetTaxRate(lineTaxes[i].rate).
			SetPriceIncludesTax(lineTaxes[i].code != "" && l.PriceIncludesTax)
		if l.NewSellingPrice != nil && *l.NewSellingPrice > 0 {
			lc = lc.SetNewSellingPrice(*l.NewSellingPrice)
		}
		if scope := strings.TrimSpace(l.PriceScope); scope != "" {
			lc = lc.SetPriceScope(scope)
		}
		_, err = lc.Save(r.Context())
		if err != nil {
			h.log.Error("create PO line failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order line")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit purchase order")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           po.ID,
		"po_number":    po.PoNumber,
		"status":       po.Status.String(),
		"total_amount": po.TotalAmount,
	})
}

// AmendPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/amend.
// Replaces the whole order (lines, supplier, warehouse, terms, shipping, notes, dates) and
// recomputes the total. When an order can be amended:
//   - draft: always;
//   - sent: only while nothing has been received and no goods receipt is open. If the total or
//     the supplier changes, the order goes back to draft so its approval and budget check run
//     again when it is re-sent;
//   - partially received, received or cancelled: never (use a purchase return or a new order).
//
// An unused purchase_order approval is closed when the amount or the supplier changes.
//
//	@Summary      Amend a draft/sent purchase order's lines
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        poID  path      string        true  "Purchase order ID"
//	@Param        body  body      createPOInput  true  "Amended lines + notes/expected_date"
//	@Success      200   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/amend [put]
func (h *InventoryExtrasHandler) AmendPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).WithLines().Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	st := po.Status.String()
	if st != "draft" && st != "sent" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft or sent purchase orders can be amended")
		return
	}
	for _, l := range po.Edges.Lines {
		if l.QuantityReceived > 0 {
			writeError(w, http.StatusConflict, "ALREADY_RECEIVING", "Goods have already been received on this order, so it can no longer be amended")
			return
		}
	}
	// An open receipt (draft or awaiting approval) was built from the current lines; amending
	// them underneath it would let the approver sign off quantities that no longer match.
	if live, lerr := h.liveOpenGoodsReceipts(r.Context(), tenantID, poID); lerr == nil && len(live) > 0 {
		writeError(w, http.StatusConflict, "RECEIPT_OPEN",
			"Goods receipt "+live[0].GrnNumber+" is open for this order. Post or cancel it before amending.")
		return
	}
	var req createPOInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if len(req.LineItems) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_LINES", "at least one line item is required")
		return
	}
	lineTaxes, err := h.resolvePOLineTaxes(r.Context(), tenantID, req.LineItems)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TAX_CODE", err.Error())
		return
	}

	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to begin transaction")
		return
	}
	if _, err = tx.PurchaseOrderLine.Delete().Where(entpoline.PoID(po.ID)).Exec(r.Context()); err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to clear existing lines")
		return
	}
	var total float64
	for i, l := range req.LineItems {
		lineTotal := roundDecimal(l.Quantity * l.UnitCost)
		total += lineTotal
		lc := tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).SetItemID(l.ItemID).SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).SetTotalPrice(lineTotal).SetNillableUnitID(l.UnitID).
			SetTaxCode(lineTaxes[i].code).SetTaxRate(lineTaxes[i].rate).
			SetPriceIncludesTax(lineTaxes[i].code != "" && l.PriceIncludesTax)
		if l.NewSellingPrice != nil && *l.NewSellingPrice > 0 {
			lc = lc.SetNewSellingPrice(*l.NewSellingPrice)
		}
		if scope := strings.TrimSpace(l.PriceScope); scope != "" {
			lc = lc.SetPriceScope(scope)
		}
		if _, err = lc.Save(r.Context()); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to write amended line")
			return
		}
	}
	total = roundDecimal(total)
	// Amend replaces the whole order as the form shows it, header included: supplier, warehouse,
	// payment terms and shipping are applied too (they used to be silently ignored).
	newStatus, returnedToDraft := amendedPOStatus(st, po.TotalAmount, total, po.SupplierID, req.SupplierID)
	upd := tx.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(total).SetNotes(req.Notes).
		SetAdditionalShippingCharges(req.AdditionalShippingCharges).
		SetStatus(entpurchaseorder.Status(newStatus))
	if req.SupplierID != uuid.Nil {
		upd = upd.SetSupplierID(req.SupplierID)
	}
	if req.WarehouseID != uuid.Nil {
		upd = upd.SetWarehouseID(req.WarehouseID)
	}
	if req.PayTermDays != nil {
		upd = upd.SetPayTermDays(*req.PayTermDays)
	} else {
		upd = upd.ClearPayTermDays()
	}
	if req.ExpectedDate != nil && *req.ExpectedDate != "" {
		if t, e := time.Parse("2006-01-02", *req.ExpectedDate); e == nil {
			upd = upd.SetExpectedDate(t)
		} else if t, e := time.Parse(time.RFC3339, *req.ExpectedDate); e == nil {
			upd = upd.SetExpectedDate(t)
		}
	}
	if req.OrderDate != nil && strings.TrimSpace(*req.OrderDate) != "" {
		if orderDate, ok := parseFlexibleDate(strings.TrimSpace(*req.OrderDate)); ok {
			upd = upd.SetOrderDate(orderDate)
		} else {
			_ = tx.Rollback()
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "order_date must be YYYY-MM-DD or RFC3339")
			return
		}
	}
	updated, err := upd.Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update purchase order")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit amendment")
		return
	}
	// A purchase_order approval was given for the old amount. When the amount changes, close it
	// so the changed order has to be approved again before it can be sent. An executed approval
	// (the order was sent on it) is history and stays approved; Send never reuses it anyway. A
	// supplier change withdraws it too: the approver signed off this spend with that supplier.
	supplierChanged := req.SupplierID != uuid.Nil && (po.SupplierID == nil || *po.SupplierID != req.SupplierID)
	if total != po.TotalAmount || supplierChanged {
		_, _ = h.orm.ApprovalRequest.Update().
			Where(areq.TenantID(tenantID), areq.ObjectID(po.ID), areq.ModuleEQ(areq.ModulePurchaseOrder),
				areq.Or(areq.StatusEQ(areq.StatusPending), areq.And(areq.StatusEQ(areq.StatusApproved), areq.ExecutedAtIsNil()))).
			SetStatus(areq.StatusCancelled).SetDecidedAt(time.Now()).
			Save(r.Context())
	}
	// Treasury keeps the budget commitment in step: status "sent" refreshes it to the new total,
	// any other status (an order sent back to draft) releases it until the order is sent again.
	payload := poBudgetPayload(tenantID, updated)
	payload["id"] = po.ID
	payload["status"] = newStatus
	payload["previous_status"] = st
	payload["previous_total_amount"] = po.TotalAmount
	payload["returned_to_draft"] = returnedToDraft
	h.publishOutbox(r.Context(), tenantID, "purchase_order", po.ID, "inventory.purchase_order.amended", payload)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": po.ID, "po_number": po.PoNumber, "total_amount": total, "status": newStatus, "returned_to_draft": returnedToDraft,
	})
}

// amendedPOStatus decides an amended order's status. A draft stays a draft. A sent order stays
// sent when the change is cosmetic (notes, dates, warehouse, terms, or the same total), but goes
// back to draft when what was promised to the supplier changes (the total or the supplier): the
// approval and treasury budget check that ran on send must run again for the new figures, so the
// order has to be sent again rather than silently changing a committed amount.
func amendedPOStatus(current string, oldTotal, newTotal float64, oldSupplier *uuid.UUID, newSupplier uuid.UUID) (status string, returnedToDraft bool) {
	if current != "sent" {
		return current, false
	}
	supplierChanged := newSupplier != uuid.Nil && (oldSupplier == nil || *oldSupplier != newSupplier)
	// Totals are money: a change below one cent is rounding noise, not a new amount.
	totalChanged := oldTotal-newTotal >= 0.005 || newTotal-oldTotal >= 0.005
	if totalChanged || supplierChanged {
		return "draft", true
	}
	return current, false
}

// SendPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/send.
// Transitions a draft PO to sent status.
//
//	@Summary      Send a draft purchase order
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/send [put]
func (h *InventoryExtrasHandler) SendPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() != "draft" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft orders can be sent")
		return
	}
	// Approval-matrix gate: if a rule matches this PO's amount, an approved request for this
	// amount that has not been used yet is required before sending. No matching rule, no gate.
	// Sending is not automatic on approval: it runs the treasury budget check with the sender's
	// override rights, so staff press Send ("Approved, ready to send" in the app). A rejected
	// order stays a draft; it is amended and submitted again (a new request).
	gate, gerr := h.approvals().Check(r.Context(), tenantID, "purchase_order", po.ID, po.TotalAmount, nil)
	if gerr != nil {
		// Fail closed: an order that may need approval is never sent on a database fault.
		h.log.Error("PO approval check failed; send refused", zap.Error(gerr))
		writeApprovalUnavailable(w)
		return
	} else if !gate.Proceed() {
		writeApprovalGate(w, "purchase_order", gate)
		return
	}
	// Budget control: sending promises the spend to the supplier, so it is checked against the
	// tenant's treasury budgets first (stop answers 409 OVER_BUDGET unless overridden).
	budgetRes, ok := h.checkPOBudget(w, r, tenantID, po)
	if !ok {
		return
	}
	// The approval is spent by this send: claim it first so two sends cannot both use it.
	var approved *ent.ApprovalRequest
	if gate.State == approvals.StateApproved {
		approved = gate.Request
		won, cerr := h.approvals().ClaimExecution(r.Context(), tenantID, approved.ID)
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "CLAIM_FAILED", "Could not record the approval as used. Try again.")
			return
		}
		if !won {
			writeError(w, http.StatusConflict, "ALREADY_SENT", "This order is already being sent on its approval")
			return
		}
	}
	// Draft to sent only once, whatever the race.
	n, err := h.orm.PurchaseOrder.Update().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID), entpurchaseorder.StatusEQ(entpurchaseorder.StatusDraft)).
		SetStatus(entpurchaseorder.StatusSent).
		Save(r.Context())
	if err != nil || n == 0 {
		if approved != nil {
			_ = h.approvals().ReleaseExecution(r.Context(), tenantID, approved.ID)
		}
		if err != nil {
			h.log.Error("send PO failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "SEND_FAILED", "Failed to send purchase order")
			return
		}
		writeError(w, http.StatusConflict, "ALREADY_SENT", "This order was sent or changed in the meantime")
		return
	}

	// Publish event for notifications service
	go func() {
		ctx := context.Background()
		evt := &events.Event{
			ID:            uuid.New(),
			TenantID:      tenantID,
			AggregateType: "inventory",
			AggregateID:   po.ID,
			EventType:     "purchase_order.sent",
			// Notifications reads the ids; treasury commits the amount against the budget.
			Payload:   poBudgetPayload(tenantID, po),
			Timestamp: time.Now().UTC(),
		}
		payload, marshalErr := evt.ToJSON()
		if marshalErr != nil {
			h.log.Warn("PO sent event: marshal failed", zap.Error(marshalErr))
			return
		}
		_, writeErr := h.orm.OutboxEvent.Create().
			SetID(evt.ID).
			SetTenantID(tenantID).
			SetAggregateType(evt.AggregateType).
			SetAggregateID(evt.AggregateID.String()).
			SetEventType(evt.EventType).
			SetPayload(json.RawMessage(payload)).
			SetStatus("PENDING").
			SetCreatedAt(evt.Timestamp).
			Save(ctx)
		if writeErr != nil {
			h.log.Warn("PO sent event: outbox write failed", zap.Error(writeErr))
		}
	}()

	resp := map[string]any{"status": "sent"}
	if budgetRes != nil && budgetRes.Action != "ok" {
		resp["budget"] = budgetRes
	}
	writeJSON(w, http.StatusOK, resp)
}

// ReceivePurchaseOrder handles PUT /inventory/purchase-orders/{poID}/receive.
// Marks the PO as received and increments on_hand stock for each line.
//
//	@Summary      Receive a purchase order into stock
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/receive [put]
//
// ReceivePurchaseOrder is the quick-action equivalent of receiving everything still
// outstanding on the PO right now: it auto-generates a goods receipt covering every line's
// remaining quantity (fully accepted, unit cost from the PO line), then posts it through
// EXACTLY the same path as the manual multi-line GRN flow (postGoodsReceiptCore) — stock-in,
// lot/serial capture, PO-line quantity_received tracking, approval gating, and the
// goods_receipt.posted / purchase_order.received ledger events. There is no longer a
// separate, ad-hoc stock-bump that skips the GRN paper trail.
func (h *InventoryExtrasHandler) ReceivePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}

	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		WithLines().
		WithSupplier().
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	status := po.Status.String()
	if status != "draft" && status != "sent" && status != "partially_received" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft, sent, or partially-received orders can be received")
		return
	}
	if po.WarehouseID == nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "Assign a destination warehouse before receiving this purchase order")
		return
	}

	// One open receipt per order. If a draft receipt already exists (typically one waiting for
	// approval) this click works on THAT receipt: still pending answers "awaiting approval",
	// approved posts it. Creating a fresh receipt on every click is what left PO 000021 with four
	// identical drafts, each with its own approval request, none of which ever posted.
	live, err := h.liveOpenGoodsReceipts(r.Context(), tenantID, poID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to check open goods receipts")
		return
	}
	if len(live) > 1 {
		numbers := make([]string, 0, len(live))
		for _, g := range live {
			numbers = append(numbers, g.GrnNumber)
		}
		writeError(w, http.StatusConflict, "MULTIPLE_OPEN_RECEIPTS",
			"This order has several unposted goods receipts ("+strings.Join(numbers, ", ")+"). Cancel the duplicates on the Goods Receipts page and keep one.")
		return
	}
	var g *ent.GoodsReceipt
	if len(live) == 1 {
		g = live[0]
	} else {
		g, err = h.createReceiveAllReceipt(r.Context(), tenantID, po)
		if err != nil {
			if err == errNothingToReceive {
				writeError(w, http.StatusBadRequest, "NOTHING_TO_RECEIVE", "All lines are already fully received")
				return
			}
			h.log.Error("auto-create GRN on mark-received failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to record goods receipt")
			return
		}
	}

	if !h.gateApproval(w, r, tenantID, "goods_receipt", g.ID, g.GrnNumber, po.TotalAmount) {
		return // GRN stays in draft, awaiting/rejected sign-off; PO status is untouched.
	}

	if err = h.postGoodsReceiptOnce(r.Context(), tenantID, g, po); err != nil {
		h.log.Error("post goods receipt on mark-received failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to post goods receipt")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "received", "goods_receipt_id": g.ID, "grn_number": g.GrnNumber})
}

// errNothingToReceive means every line of the order is already fully received.
var errNothingToReceive = errors.New("nothing left to receive")

// createReceiveAllReceipt opens the "receive everything still outstanding" draft receipt for
// Mark Received. After creating it, it re-reads the order's drafts: if a concurrent click opened
// one first, this one is deleted and the earlier one is returned, so a double click still ends
// with a single receipt.
func (h *InventoryExtrasHandler) createReceiveAllReceipt(ctx context.Context, tenantID uuid.UUID, po *ent.PurchaseOrder) (*ent.GoodsReceipt, error) {
	var remaining []*ent.PurchaseOrderLine
	for _, line := range po.Edges.Lines {
		if line.QuantityOrdered > line.QuantityReceived {
			remaining = append(remaining, line)
		}
	}
	if len(remaining) == 0 {
		return nil, errNothingToReceive
	}

	grnNumber := "GRN-" + strings.ToUpper(uuid.New().String()[:8])
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(ctx, tenantID, documents.DocTypeGRN); derr == nil && n != "" {
			grnNumber = n
		}
	}
	g, err := h.orm.GoodsReceipt.Create().
		SetTenantID(tenantID).SetGrnNumber(grnNumber).SetPurchaseOrderID(po.ID).
		SetNillableSupplierID(po.SupplierID).SetNillableWarehouseID(po.WarehouseID).
		SetNotes("Auto-generated via Mark Received").
		Save(ctx)
	if err != nil {
		return nil, err
	}
	for _, line := range remaining {
		qty := line.QuantityOrdered - line.QuantityReceived
		lineID := line.ID
		lc := h.orm.GoodsReceiptLine.Create().
			SetTenantID(tenantID).SetGoodsReceiptID(g.ID).SetItemID(line.ItemID).
			SetPurchaseOrderLineID(lineID).
			SetQuantityReceived(qty).SetQuantityAccepted(qty).SetQuantityRejected(0).
			SetUnitCost(line.UnitPrice)
		// Carry the selling price decided on the PO line, as the Goods Receipt dialog does.
		// Without it, Mark Received posted the new cost but silently dropped the new price.
		if line.NewSellingPrice != nil && *line.NewSellingPrice > 0 {
			lc = lc.SetNewSellingPrice(*line.NewSellingPrice)
			if scope := strings.TrimSpace(line.PriceScope); scope != "" {
				lc = lc.SetPriceScope(scope)
			}
		}
		if _, lerr := lc.Save(ctx); lerr != nil {
			h.log.Warn("auto GRN line create failed", zap.Error(lerr))
		}
	}
	// Double-click guard: keep only the oldest open draft for this order.
	if open, oerr := h.openGoodsReceipts(ctx, tenantID, po.ID); oerr == nil && len(open) > 0 && open[0].ID != g.ID {
		h.deleteGoodsReceipt(ctx, tenantID, g.ID)
		return open[0], nil
	}
	h.publishOutbox(ctx, tenantID, "inventory", g.ID, "goods_receipt.created", map[string]any{
		"id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": po.ID,
	})
	return g, nil
}

// CancelPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/cancel.
//
//	@Summary      Cancel a purchase order
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/cancel [put]
func (h *InventoryExtrasHandler) CancelPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() == "received" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Received orders cannot be cancelled")
		return
	}
	// An open receipt means goods are being booked in against this order right now; cancelling
	// underneath it would leave a receipt (possibly already approved) for a dead order.
	if live, lerr := h.liveOpenGoodsReceipts(r.Context(), tenantID, poID); lerr == nil && len(live) > 0 {
		writeError(w, http.StatusConflict, "RECEIPT_OPEN",
			"Goods receipt "+live[0].GrnNumber+" is open for this order. Post or cancel it before cancelling the order.")
		return
	}
	if _, err := h.orm.PurchaseOrder.UpdateOneID(poID).SetStatus("cancelled").Save(r.Context()); err != nil {
		h.log.Error("cancel PO failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CANCEL_FAILED", "Failed to cancel purchase order")
		return
	}
	// Treasury releases whatever budget the order still had committed.
	h.publishOutbox(r.Context(), tenantID, "purchase_order", po.ID, "inventory.purchase_order.cancelled", poBudgetPayload(tenantID, po))
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
