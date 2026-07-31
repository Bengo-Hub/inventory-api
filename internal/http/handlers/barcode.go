package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entserial "github.com/bengobox/inventory-service/internal/ent/inventoryserial"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	"github.com/bengobox/inventory-service/internal/modules/barcode"
)

// registerBarcodeRoutes wires the barcode/label-printing endpoints. `perm` is the same
// per-route permission middleware factory used by RegisterRoutes.
func (h *InventoryHandler) registerBarcodeRoutes(inv chi.Router, perm func(string) func(http.Handler) http.Handler) {
	// Single-item barcode PNG (read). Lazily generates + stores an internal code if the item
	// has none, then renders. GET is exempt from group auth (consistent with other item reads).
	inv.Get("/items/{itemID}/barcode.png", h.GetItemBarcodePNG)
	// Single-item printable label (title/SKU/barcode/human-readable), same card layout the bulk
	// Avery sheet uses — the item detail page's "Barcode" action prints this instead of a bare
	// barcode-image-only preview with no item details.
	inv.Get("/items/{itemID}/label.pdf", h.GetItemLabelPDF)
	// Bulk label-print job: returns a PDF (Avery) or printer text (ZPL/Dymo). Gated on the
	// items "manage" permission (printing stock labels is a privileged item operation, and it
	// may persist generated internal barcodes).
	inv.With(perm("inventory.items.manage")).Post("/labels/print", h.PrintLabels)
}

// ─── Item barcode resolution ─────────────────────────────────────────────────

// resolveItemBarcode returns the (content, symbology, barcodeType-string) to use for an item.
// If the item already has a manufacturer/stored barcode it is used verbatim (never overwritten).
// Otherwise an internal EAN-13 (GS1 restricted range, prefix "20") is generated, persisted on the
// item, and returned. persist=false skips the write (used in bulk preview to avoid N writes when
// not desired — here we always persist so codes are stable + reusable).
func (h *InventoryHandler) resolveItemBarcode(ctx context.Context, it *ent.Item) (content string, sym barcode.Symbology, btype string, err error) {
	if strings.TrimSpace(it.Barcode) != "" {
		code := strings.TrimSpace(it.Barcode)
		s := barcode.SymCode128
		if it.BarcodeType != "" {
			switch strings.ToUpper(it.BarcodeType) {
			case "EAN13", "UPC", "UPCA":
				s = barcode.SymEAN13
			case "GS1-128", "GS1128":
				s = barcode.SymGS1128
			default:
				s = barcode.SymCode128
			}
		} else {
			s = barcode.ChooseSymbology(code)
		}
		return code, s, it.BarcodeType, nil
	}

	// No stored barcode → generate a stable internal EAN-13 from the item ID (deterministic seed).
	seed := seedFromUUID(it.ID)
	code, gerr := barcode.GenerateInternalEAN13("20", seed)
	if gerr != nil {
		return "", "", "", gerr
	}
	// Persist so the code is stable + reusable; never overwrite an existing GTIN (we only got
	// here because Barcode was empty).
	if _, uerr := h.orm.Item.UpdateOneID(it.ID).
		SetBarcode(code).SetBarcodeType("EAN13").Save(ctx); uerr != nil {
		h.log.Warn("barcode: failed to persist internal code", zap.Error(uerr), zap.String("item_id", it.ID.String()))
	}
	return code, barcode.SymEAN13, "EAN13", nil
}

// seedFromUUID derives a stable 10-digit-range seed from a UUID (low 8 bytes).
func seedFromUUID(id uuid.UUID) uint64 {
	var v uint64
	b := id[8:16]
	for _, x := range b {
		v = (v << 8) | uint64(x)
	}
	return v
}

// GetItemBarcodePNG handles GET /inventory/items/{itemID}/barcode.png
func (h *InventoryHandler) GetItemBarcodePNG(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item id")
		return
	}
	it, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.ID(itemID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}
	content, sym, _, err := h.resolveItemBarcode(r.Context(), it)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "BARCODE_FAILED", err.Error())
		return
	}
	png, err := barcode.RenderPNG(content, sym, 800, 260)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "BARCODE_FAILED", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.png"`, it.Sku))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// GetItemLabelPDF handles GET /inventory/items/{itemID}/label.pdf — a single-item printable
// label (title, SKU, barcode, human-readable text) using the same card layout as the bulk
// Avery sheet, so a quick single-item print always shows full item details next to the
// barcode rather than a bare barcode image.
func (h *InventoryHandler) GetItemLabelPDF(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid item id")
		return
	}
	it, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.ID(itemID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}
	content, sym, _, err := h.resolveItemBarcode(r.Context(), it)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "BARCODE_FAILED", err.Error())
		return
	}
	lbl := barcode.Label{Title: it.Name, Sku: it.Sku, Symbology: sym, Content: content}
	var companyName string
	if h.docSvc != nil {
		companyName = h.docSvc.GetBranding(r.Context(), tenantID).CompanyName
	}
	pdf, err := barcode.RenderSingleLabelPDF(lbl, companyName)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "BARCODE_FAILED", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s-label.pdf"`, it.Sku))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pdf)
}

// ─── Bulk label printing ─────────────────────────────────────────────────────

// PrintLabelsRequest is the body for POST /inventory/labels/print.
type PrintLabelsRequest struct {
	// Selection — exactly one of these drives which items are labelled.
	CategoryID      *uuid.UUID  `json:"category_id,omitempty"`
	SupplierID      *uuid.UUID  `json:"supplier_id,omitempty"`
	PurchaseOrderID *uuid.UUID  `json:"purchase_order_id,omitempty"`
	ItemIDs         []uuid.UUID `json:"item_ids,omitempty"`

	// Quantity of labels per selected item (default 1).
	QtyPerItem int `json:"qty_per_item,omitempty"`
	// Optional per-item overrides keyed by item-id string (wins over QtyPerItem).
	Quantities map[string]int `json:"quantities,omitempty"`

	// Output format: avery_a4 | thermal_zpl | dymo.
	Format string `json:"format"`
	// Sheet selects the Avery grid preset when Format == avery_a4 (e.g. "l7160" | "5160");
	// empty defaults to Avery L7160 (A4). ThermalSize selects the physical label size when
	// Format == thermal_zpl (e.g. "2x1" | "3x2" | "4x2" | "4x6" @203dpi); empty defaults to
	// 4x2in. See docs/barcode-labels.md for what real printer/paper stock each maps to.
	Sheet       string `json:"sheet,omitempty"`
	ThermalSize string `json:"thermal_size,omitempty"`

	// Lot/serial label mode. When IncludeLot is set, the engine renders one GS1-128 label per
	// active lot of each selected item (embedding (10) batch + (17) expiry). When IncludeSerial
	// is set, one GS1-128 label per available serial (embedding (21) serial). When neither is set,
	// a plain item barcode label is produced. IncludePrice embeds (392n) price for weighed/retail.
	IncludeLot    bool `json:"include_lot,omitempty"`
	IncludeSerial bool `json:"include_serial,omitempty"`
	IncludePrice  bool `json:"include_price,omitempty"`

	// PriceDecimals controls the implied decimals for the (392n) price AI / printed price (default 2).
	PriceDecimals int    `json:"price_decimals,omitempty"`
	Currency      string `json:"currency,omitempty"` // for the printed price string; default KES
}

// PrintLabels handles POST /inventory/labels/print.
func (h *InventoryHandler) PrintLabels(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req PrintLabelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	format, err := barcode.ParseLabelFormat(req.Format)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORMAT", err.Error())
		return
	}
	if req.QtyPerItem <= 0 {
		req.QtyPerItem = 1
	}
	if req.PriceDecimals <= 0 {
		req.PriceDecimals = 2
	}
	currency := req.Currency
	if currency == "" {
		currency = "KES"
	}

	items, err := h.resolveLabelItems(r, tenantID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SELECTION_FAILED", err.Error())
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "EMPTY_SELECTION", "No items matched the selection")
		return
	}

	labels, err := h.buildLabels(r, tenantID, items, req, currency)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "LABEL_BUILD_FAILED", err.Error())
		return
	}
	if len(labels) == 0 {
		writeError(w, http.StatusBadRequest, "NO_LABELS", "Selection produced no labels (no lots/serials?)")
		return
	}

	batch := barcode.Batch{
		Labels:      labels,
		Format:      format,
		Avery:       barcode.AverySpecByName(req.Sheet),
		Thermal:     barcode.ThermalSpecByName(req.ThermalSize),
		GeneratedAt: time.Now(),
	}
	if h.docSvc != nil {
		batch.CompanyName = h.docSvc.GetBranding(r.Context(), tenantID).CompanyName
	}

	out, err := batch.Render()
	if err != nil {
		h.log.Error("label render failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RENDER_FAILED", "Failed to render labels")
		return
	}

	ext := "pdf"
	if !format.IsPDF() {
		ext = "txt"
		if format == barcode.FormatThermalZPL {
			ext = "zpl"
		}
	}
	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="labels-%s.%s"`, time.Now().Format("20060102-150405"), ext))
	_, _ = w.Write(out)
}

// resolveLabelItems expands the request selection into a concrete item list.
func (h *InventoryHandler) resolveLabelItems(r *http.Request, tenantID uuid.UUID, req PrintLabelsRequest) ([]*ent.Item, error) {
	ctx := r.Context()
	q := h.orm.Item.Query().Where(entitem.TenantID(tenantID))

	switch {
	case len(req.ItemIDs) > 0:
		q = q.Where(entitem.IDIn(req.ItemIDs...))
	case req.CategoryID != nil:
		q = q.Where(entitem.CategoryID(*req.CategoryID))
	case req.PurchaseOrderID != nil:
		lines, err := h.orm.PurchaseOrderLine.Query().
			Where(entpoline.PoID(*req.PurchaseOrderID)).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load purchase-order lines: %w", err)
		}
		ids := make([]uuid.UUID, 0, len(lines))
		for _, l := range lines {
			ids = append(ids, l.ItemID)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		q = q.Where(entitem.IDIn(ids...))
	case req.SupplierID != nil:
		// Items the supplier has been ordered from (via this supplier's purchase orders).
		pos, err := h.orm.PurchaseOrder.Query().
			Where(entpo.TenantID(tenantID), entpo.SupplierID(*req.SupplierID)).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load supplier purchase orders: %w", err)
		}
		poIDs := make([]uuid.UUID, 0, len(pos))
		for _, p := range pos {
			poIDs = append(poIDs, p.ID)
		}
		if len(poIDs) == 0 {
			return nil, nil
		}
		lines, err := h.orm.PurchaseOrderLine.Query().
			Where(entpoline.PoIDIn(poIDs...)).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load supplier po lines: %w", err)
		}
		seen := map[uuid.UUID]bool{}
		ids := make([]uuid.UUID, 0, len(lines))
		for _, l := range lines {
			if !seen[l.ItemID] {
				seen[l.ItemID] = true
				ids = append(ids, l.ItemID)
			}
		}
		if len(ids) == 0 {
			return nil, nil
		}
		q = q.Where(entitem.IDIn(ids...))
	default:
		return nil, fmt.Errorf("a selection is required (item_ids, category_id, supplier_id, or purchase_order_id)")
	}

	return q.Order(ent.Asc(entitem.FieldSku)).All(ctx)
}

// buildLabels expands items × quantity into labels, branching on lot/serial mode.
func (h *InventoryHandler) buildLabels(r *http.Request, tenantID uuid.UUID, items []*ent.Item, req PrintLabelsRequest, currency string) ([]barcode.Label, error) {
	ctx := r.Context()
	var labels []barcode.Label

	qtyFor := func(id uuid.UUID) int {
		if req.Quantities != nil {
			if q, ok := req.Quantities[id.String()]; ok && q > 0 {
				return q
			}
		}
		return req.QtyPerItem
	}

	for _, it := range items {
		content, sym, _, err := h.resolveItemBarcode(ctx, it)
		if err != nil {
			h.log.Warn("barcode: skip item, resolve failed", zap.String("sku", it.Sku), zap.Error(err))
			continue
		}
		priceStr := h.itemPriceString(it, currency, req.PriceDecimals)
		gtin, _ := barcode.NormalizeEAN13(content) // 13-digit GTIN for GS1 (empty if not numeric EAN)

		switch {
		case req.IncludeSerial:
			serials, _ := h.orm.InventorySerial.Query().
				Where(entserial.TenantID(tenantID), entserial.ItemID(it.ID),
					entserial.StatusEQ(entserial.Status("available"))).All(ctx)
			for _, s := range serials {
				g := barcode.NewGS1()
				if gtin != "" {
					g.GTIN(gtin)
				}
				g.Serial(s.SerialNumber)
				if req.IncludePrice {
					addPriceAI(g, it, req.PriceDecimals)
				}
				lbl := serialLabel(it, s.SerialNumber, g, priceStr)
				labels = appendN(labels, lbl, qtyFor(it.ID))
			}
		case req.IncludeLot:
			lots, _ := h.orm.InventoryLot.Query().
				Where(entlot.TenantID(tenantID), entlot.ItemID(it.ID),
					entlot.StatusEQ(entlot.Status("active"))).All(ctx)
			for _, lot := range lots {
				g := barcode.NewGS1()
				if gtin != "" {
					g.GTIN(gtin)
				}
				g.Batch(lot.LotNumber)
				if lot.ExpiryDate != nil {
					g.Expiry(*lot.ExpiryDate)
				}
				if req.IncludePrice {
					addPriceAI(g, it, req.PriceDecimals)
				}
				lbl := lotLabel(it, lot, g, priceStr)
				labels = appendN(labels, lbl, qtyFor(it.ID))
			}
		default:
			lbl := barcode.Label{
				Title:     it.Name,
				Sku:       it.Sku,
				Symbology: sym,
				Content:   content,
			}
			if req.IncludePrice {
				lbl.Price = priceStr
			}
			labels = appendN(labels, lbl, qtyFor(it.ID))
		}
	}
	return labels, nil
}

// serialLabel builds a GS1-128 serial label. SKU and serial render as distinct rows on the
// card (see drawLabelCell) rather than one crammed string.
func serialLabel(it *ent.Item, serial string, g *barcode.GS1Builder, price string) barcode.Label {
	return barcode.Label{
		Title:      it.Name,
		Sku:        it.Sku,
		DetailLine: "S/N " + serial,
		Symbology:  barcode.SymGS1128,
		Content:    g.Barcode(),
		HumanText:  g.HumanReadable(),
		Price:      price,
	}
}

// lotLabel builds a GS1-128 lot/batch label. SKU and lot/expiry render as distinct rows on
// the card rather than one crammed string.
func lotLabel(it *ent.Item, lot *ent.InventoryLot, g *barcode.GS1Builder, price string) barcode.Label {
	detail := "LOT " + lot.LotNumber
	if lot.ExpiryDate != nil {
		detail += "  EXP " + lot.ExpiryDate.Format("2006-01-02")
	}
	return barcode.Label{
		Title:      it.Name,
		Sku:        it.Sku,
		DetailLine: detail,
		Symbology:  barcode.SymGS1128,
		Content:    g.Barcode(),
		HumanText:  g.HumanReadable(),
		Price:      price,
	}
}

// addPriceAI adds the (392n) price AI from the item's effective price.
func addPriceAI(g *barcode.GS1Builder, it *ent.Item, decimals int) {
	price := itemEffectivePrice(it)
	if price <= 0 {
		return
	}
	scale := 1.0
	for i := 0; i < decimals; i++ {
		scale *= 10
	}
	g.PriceMinor(int64(price*scale+0.5), decimals)
}

// itemEffectivePrice picks the best stored price for label purposes (min selling price → cost).
func itemEffectivePrice(it *ent.Item) float64 {
	if it.MinSellingPrice != nil && *it.MinSellingPrice > 0 {
		return *it.MinSellingPrice
	}
	if it.CostPrice != nil && *it.CostPrice > 0 {
		return *it.CostPrice
	}
	return 0
}

// itemPriceString formats the printed price line (empty when no price known).
func (h *InventoryHandler) itemPriceString(it *ent.Item, currency string, decimals int) string {
	p := itemEffectivePrice(it)
	if p <= 0 {
		return ""
	}
	return fmt.Sprintf("%s %.*f", currency, decimals, p)
}

// appendN appends `lbl` n times (n>=1).
func appendN(dst []barcode.Label, lbl barcode.Label, n int) []barcode.Label {
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		dst = append(dst, lbl)
	}
	return dst
}
