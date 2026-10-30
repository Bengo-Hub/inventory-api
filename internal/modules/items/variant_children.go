package items

// Variants as child items (2026-10-28).
//
// A sellable variation (a T-shirt in Red / M) is its own Item with its own SKU, barcode, price,
// cost and stock, so every per-item flow (balances, receipts, transfers, counts, lots,
// reservations, pricing tiers, eTIMS, COGS) works for it unchanged. The link lives in metadata,
// with no schema change:
//
//   - child:  metadata.variant_of = "<parent item id>", metadata.variant_attributes = {"size":"M","colour":"Red"}
//   - parent: metadata.variant_axes = {"size":["S","M","L"],"colour":["Red","Blue"]}
//
// The parent is the display product and holds no stock of its own. Nothing blocks stock on it;
// the UI simply never receives or counts against it once it has variants.
//
// No index backs metadata->>'variant_of'. Lookups are always scoped by tenant_id (indexed) and a
// tenant's item count is in the thousands, so the JSON filter runs over an already narrow set.
// If that ever shows up in slow query logs, add an expression index through the Ent and Atlas
// versioned migration workflow.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	entdialect "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

// Metadata keys that link variant child items to their parent.
const (
	MetaVariantOf         = "variant_of"
	MetaVariantAttributes = "variant_attributes"
	MetaVariantAxes       = "variant_axes"
)

// includeVariantChildrenKey is a context flag set by the HTTP layer (?include_children=1). By
// default a catalog listing that asks for include=variants hides the child items, because they
// are shown inside their parent's variants[]; this flag lists them as rows too.
type includeVariantChildrenKey struct{}

// WithIncludeVariantChildren keeps variant child items in ListItems even when include=variants
// is requested.
func WithIncludeVariantChildren(ctx context.Context) context.Context {
	return context.WithValue(ctx, includeVariantChildrenKey{}, true)
}

func includeVariantChildren(ctx context.Context) bool {
	v, _ := ctx.Value(includeVariantChildrenKey{}).(bool)
	return v
}

// VariantRowInput is one cell of the variant matrix.
type VariantRowInput struct {
	Attributes map[string]string `json:"attributes"`
	SKU        string            `json:"sku,omitempty"`
	Barcode    string            `json:"barcode,omitempty"`
	Price      *float64          `json:"price,omitempty"`
	IsActive   *bool             `json:"is_active,omitempty"`
}

// SaveVariantsInput is the full variant matrix of a parent item: the axes and every row that
// should exist. Rows left out are deactivated.
type SaveVariantsInput struct {
	Axes map[string][]string `json:"axes"`
	Rows []VariantRowInput   `json:"rows"`
}

// VariantInputError is a validation failure in a SaveVariants request (HTTP 400).
type VariantInputError struct{ Msg string }

func (e *VariantInputError) Error() string { return e.Msg }

// ErrVariantParentNotFound is returned when the parent item is not in the tenant (HTTP 404).
var ErrVariantParentNotFound = errors.New("items: variant parent item not found")

// variantOfPredicate matches items whose metadata.variant_of is set.
func variantOfPredicate() predicate.Item {
	return predicate.Item(func(sel *entdialect.Selector) {
		sel.Where(sqljson.HasKey(item.FieldMetadata, sqljson.Path(MetaVariantOf), sqljson.Unquote(true)))
	})
}

// variantOfIn matches items whose metadata.variant_of is one of the given parent ids.
func variantOfIn(parentIDs []uuid.UUID) predicate.Item {
	args := make([]any, 0, len(parentIDs))
	for _, id := range parentIDs {
		args = append(args, id.String())
	}
	return predicate.Item(func(sel *entdialect.Selector) {
		sel.Where(sqljson.ValueIn(item.FieldMetadata, args, sqljson.Path(MetaVariantOf)))
	})
}

// VariantOfFromMetadata returns the parent id a child item points at.
func VariantOfFromMetadata(m map[string]any) (uuid.UUID, bool) {
	raw, ok := m[MetaVariantOf]
	if !ok {
		return uuid.Nil, false
	}
	s, ok := raw.(string)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// VariantAttributesFromMetadata reads metadata.variant_attributes as a string map. Values come
// back from the database as map[string]any, or as map[string]string when set in process.
func VariantAttributesFromMetadata(m map[string]any) map[string]string {
	switch v := m[MetaVariantAttributes].(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, val := range v {
			out[k] = fmt.Sprint(val)
		}
		return out
	}
	return nil
}

// VariantAxesFromMetadata reads metadata.variant_axes as axis name to ordered values.
func VariantAxesFromMetadata(m map[string]any) map[string][]string {
	switch v := m[MetaVariantAxes].(type) {
	case map[string][]string:
		return v
	case map[string]any:
		out := make(map[string][]string, len(v))
		for k, raw := range v {
			switch vals := raw.(type) {
			case []string:
				out[k] = vals
			case []any:
				for _, x := range vals {
					out[k] = append(out[k], fmt.Sprint(x))
				}
			}
		}
		return out
	}
	return nil
}

// sortedAxisKeys returns axis names in a stable order: alphabetical. Names and default SKUs list
// values in this order ("colour" before "size" gives "Red / M").
func sortedAxisKeys[T any](axes map[string]T) []string {
	keys := make([]string, 0, len(axes))
	for k := range axes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// comboKey identifies an attribute combination regardless of map order.
func comboKey(attrs map[string]string) string {
	keys := sortedAxisKeys(attrs)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strings.ToLower(k)+"="+strings.ToLower(attrs[k]))
	}
	return strings.Join(parts, "|")
}

// orderedValues lists the attribute values in axis key order.
func orderedValues(attrs map[string]string) []string {
	keys := sortedAxisKeys(attrs)
	vals := make([]string, 0, len(keys))
	for _, k := range keys {
		vals = append(vals, attrs[k])
	}
	return vals
}

// variantName builds "<parent name>, Red / M".
func variantName(parentName string, attrs map[string]string) string {
	return parentName + ", " + strings.Join(orderedValues(attrs), " / ")
}

// defaultVariantSKU builds "<parent sku>-RED-M" (whitespace dropped, upper case).
func defaultVariantSKU(parentSKU string, attrs map[string]string) string {
	parts := []string{parentSKU}
	for _, v := range orderedValues(attrs) {
		parts = append(parts, strings.ToUpper(strings.Join(strings.Fields(v), "")))
	}
	return strings.Join(parts, "-")
}

// normalizeVariantInput trims axis names (lower case) and values, and checks every row against
// the axes. It returns canonical rows whose attribute values use the axis spelling.
func normalizeVariantInput(in SaveVariantsInput) (map[string][]string, []VariantRowInput, error) {
	axes := make(map[string][]string, len(in.Axes))
	for rawKey, rawVals := range in.Axes {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if key == "" {
			return nil, nil, &VariantInputError{Msg: "axis names cannot be blank"}
		}
		if _, dup := axes[key]; dup {
			return nil, nil, &VariantInputError{Msg: fmt.Sprintf("axis %q is listed twice", key)}
		}
		seen := make(map[string]bool, len(rawVals))
		vals := make([]string, 0, len(rawVals))
		for _, v := range rawVals {
			v = strings.TrimSpace(v)
			if v == "" {
				return nil, nil, &VariantInputError{Msg: fmt.Sprintf("axis %q has a blank value", key)}
			}
			if seen[strings.ToLower(v)] {
				return nil, nil, &VariantInputError{Msg: fmt.Sprintf("axis %q lists %q twice", key, v)}
			}
			seen[strings.ToLower(v)] = true
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return nil, nil, &VariantInputError{Msg: fmt.Sprintf("axis %q has no values", key)}
		}
		axes[key] = vals
	}
	if len(in.Rows) > 0 && len(axes) == 0 {
		return nil, nil, &VariantInputError{Msg: "rows need at least one axis"}
	}

	rows := make([]VariantRowInput, 0, len(in.Rows))
	seenCombo := make(map[string]bool, len(in.Rows))
	seenSKU := make(map[string]bool, len(in.Rows))
	for i, r := range in.Rows {
		attrs := make(map[string]string, len(r.Attributes))
		for rawKey, rawVal := range r.Attributes {
			key := strings.ToLower(strings.TrimSpace(rawKey))
			allowed, ok := axes[key]
			if !ok {
				return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: %q is not one of the axes", i+1, rawKey)}
			}
			val := strings.TrimSpace(rawVal)
			canonical := ""
			for _, a := range allowed {
				if strings.EqualFold(a, val) {
					canonical = a
					break
				}
			}
			if canonical == "" {
				return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: %q is not an allowed %s", i+1, rawVal, key)}
			}
			attrs[key] = canonical
		}
		if len(attrs) != len(axes) {
			return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: needs a value for every axis (%s)", i+1, strings.Join(sortedAxisKeys(axes), ", "))}
		}
		ck := comboKey(attrs)
		if seenCombo[ck] {
			return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: the combination %s is listed twice", i+1, strings.Join(orderedValues(attrs), " / "))}
		}
		seenCombo[ck] = true
		r.Attributes = attrs
		r.SKU = strings.TrimSpace(r.SKU)
		r.Barcode = strings.TrimSpace(r.Barcode)
		if r.Price != nil && *r.Price < 0 {
			return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: price cannot be negative", i+1)}
		}
		if r.SKU != "" {
			if seenSKU[strings.ToLower(r.SKU)] {
				return nil, nil, &VariantInputError{Msg: fmt.Sprintf("row %d: SKU %q is used by another row", i+1, r.SKU)}
			}
			seenSKU[strings.ToLower(r.SKU)] = true
		}
		rows = append(rows, r)
	}
	return axes, rows, nil
}

// loadChildItems returns the child items of the given parents in one query per chunk of 1000
// parents, scoped to the tenant. activeOnly drops deactivated children.
func (s *Service) loadChildItems(ctx context.Context, client *ent.Client, tenantID uuid.UUID, parentIDs []uuid.UUID, activeOnly bool) ([]*ent.Item, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	var out []*ent.Item
	for i := 0; i < len(parentIDs); i += inChunk {
		end := min(i+inChunk, len(parentIDs))
		q := client.Item.Query().Where(item.TenantID(tenantID), variantOfIn(parentIDs[i:end]))
		if activeOnly {
			q = q.Where(item.IsActive(true))
		}
		rows, err := q.Order(ent.Asc(item.FieldName), ent.Asc(item.FieldID)).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("items: list variant children: %w", err)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// childVariantDTOs turns child items into VariantDTOs grouped by parent id, with the effective
// selling price (same enrichment as a list row) and on-hand/available summed over scope (nil
// scope means every warehouse of the tenant).
func (s *Service) childVariantDTOs(ctx context.Context, tenantID uuid.UUID, children []*ent.Item, cfg *ent.TenantInventoryConfig, scope map[uuid.UUID]struct{}) map[uuid.UUID][]VariantDTO {
	out := make(map[uuid.UUID][]VariantDTO)
	if len(children) == 0 {
		return out
	}
	dtos := make([]ItemDTO, len(children))
	ids := make([]uuid.UUID, len(children))
	for i, c := range children {
		dtos[i] = *s.mapToDTO(c)
		ids[i] = c.ID
	}
	s.enrichPrices(ctx, tenantID, cfg, dtos)

	type stock struct{ onHand, available float64 }
	bal := make(map[uuid.UUID]stock, len(ids))
	if rows, err := s.client.InventoryBalance.Query().
		Where(inventorybalance.TenantIDEQ(tenantID), inventorybalance.ItemIDIn(ids...)).
		Select(inventorybalance.FieldItemID, inventorybalance.FieldWarehouseID, inventorybalance.FieldOnHand, inventorybalance.FieldAvailable).
		All(ctx); err == nil {
		for _, b := range rows {
			if scope != nil {
				if _, ok := scope[b.WarehouseID]; !ok {
					continue
				}
			}
			cur := bal[b.ItemID]
			cur.onHand += b.OnHand
			cur.available += b.Available
			bal[b.ItemID] = cur
		}
	}

	for i, c := range children {
		parentID, ok := VariantOfFromMetadata(c.Metadata)
		if !ok {
			continue
		}
		d := dtos[i]
		v := VariantDTO{
			ID:         c.ID,
			SKU:        c.Sku,
			Name:       c.Name,
			Attributes: VariantAttributesFromMetadata(c.Metadata),
			Barcode:    c.Barcode,
			IsActive:   c.IsActive,
			ImageURL:   d.ImageURL,
		}
		if d.SellingPrice != nil {
			v.Price = *d.SellingPrice
		}
		if st, ok := bal[c.ID]; ok {
			onHand, avail := st.onHand, st.available
			v.OnHand, v.Available = &onHand, &avail
		}
		out[parentID] = append(out[parentID], v)
	}
	return out
}

// sortVariantsByAxes orders variants by the parent's axis value order (first axis in key order
// varies slowest). Variants with values outside the axes sort last, by name.
func sortVariantsByAxes(vs []VariantDTO, axes map[string][]string) {
	if len(axes) == 0 {
		return
	}
	keys := sortedAxisKeys(axes)
	rank := func(v VariantDTO, k string) int {
		for i, a := range axes[k] {
			if strings.EqualFold(a, v.Attributes[k]) {
				return i
			}
		}
		return len(axes[k])
	}
	sort.SliceStable(vs, func(i, j int) bool {
		for _, k := range keys {
			ri, rj := rank(vs[i], k), rank(vs[j], k)
			if ri != rj {
				return ri < rj
			}
		}
		return vs[i].Name < vs[j].Name
	})
}

// ListChildVariants returns the active variant children of each parent as VariantDTOs, in one
// query for all parents. Parents without children are absent from the map.
func (s *Service) ListChildVariants(ctx context.Context, tenantID uuid.UUID, parentIDs []uuid.UUID) (map[uuid.UUID][]VariantDTO, error) {
	children, err := s.loadChildItems(ctx, s.client, tenantID, parentIDs, true)
	if err != nil {
		return nil, err
	}
	cfg, _ := s.client.TenantInventoryConfig.Query().Where(tenantinventoryconfig.TenantID(tenantID)).Only(ctx)
	return s.childVariantDTOs(ctx, tenantID, children, cfg, nil), nil
}

// attachChildVariants fills Variants/HasVariants on the page's parent items (those carrying
// metadata.variant_axes) from their child items. A parent with no children keeps whatever the
// legacy ItemVariant eager load put there.
func (s *Service) attachChildVariants(ctx context.Context, tenantID uuid.UUID, dtos []ItemDTO, cfg *ent.TenantInventoryConfig, scope map[uuid.UUID]struct{}) {
	var parentIDs []uuid.UUID
	for i := range dtos {
		if _, ok := dtos[i].Metadata[MetaVariantAxes]; ok {
			parentIDs = append(parentIDs, dtos[i].ID)
		}
	}
	if len(parentIDs) == 0 {
		return
	}
	children, err := s.loadChildItems(ctx, s.rc(), tenantID, parentIDs, true)
	if err != nil {
		s.log.Warn("items: variant children unavailable for list page", zap.Error(err))
		return
	}
	byParent := s.childVariantDTOs(ctx, tenantID, children, cfg, scope)
	for i := range dtos {
		vs, ok := byParent[dtos[i].ID]
		if !ok || len(vs) == 0 {
			continue
		}
		sortVariantsByAxes(vs, VariantAxesFromMetadata(dtos[i].Metadata))
		dtos[i].Variants = vs
		dtos[i].HasVariants = true
	}
}

// childDTOFromParent copies the catalogue fields a variant inherits from its parent.
func childDTOFromParent(p *ItemDTO) ItemDTO {
	return ItemDTO{
		Type:                    p.Type,
		Description:             p.Description,
		CategoryID:              p.CategoryID,
		BrandID:                 p.BrandID,
		UnitID:                  p.UnitID,
		PreferredSupplierID:     p.PreferredSupplierID,
		ImageURL:                p.ImageURL,
		Tags:                    p.Tags,
		Manufacturer:            p.Manufacturer,
		Model:                   p.Model,
		Condition:               p.Condition,
		CountryOfOrigin:         p.CountryOfOrigin,
		HSCode:                  p.HSCode,
		IsReturnable:            p.IsReturnable,
		ReturnWindowDays:        p.ReturnWindowDays,
		NonBillable:             p.NonBillable,
		NotForSale:              p.NotForSale,
		RequiresAgeVerification: p.RequiresAgeVerification,
		IsPerishable:            p.IsPerishable,
		TrackLots:               p.TrackLots,
		TrackSerialNumbers:      p.TrackSerialNumbers,
		ShelfLifeDays:           p.ShelfLifeDays,
		WeightKg:                p.WeightKg,
		DimensionsCm:            p.DimensionsCm,
		CostPrice:               p.CostPrice,
		PurchasePrice:           p.PurchasePrice,
		PurchasePackSize:        p.PurchasePackSize,
		PurchaseUnit:            p.PurchaseUnit,
		YieldPct:                p.YieldPct,
		StockTrackingMode:       p.StockTrackingMode,
		MinSellingPrice:         p.MinSellingPrice,
		MaxSellingPrice:         p.MaxSellingPrice,
		TargetMarginPercent:     p.TargetMarginPercent,
		TaxCodeID:               p.TaxCodeID,
		TaxInclusive:            p.TaxInclusive,
		EtimsItemClsCd:          p.EtimsItemClsCd,
		EtimsPkgUnitCd:          p.EtimsPkgUnitCd,
		EtimsQtyUnitCd:          p.EtimsQtyUnitCd,
		UseCase:                 p.UseCase,
	}
}

// copyMetadata returns a shallow copy that can be changed without touching the source.
func copyMetadata(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func attrsToAny(attrs map[string]string) map[string]any {
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		out[k] = v
	}
	return out
}

// SaveVariants creates, updates and deactivates the variant child items of a parent so they
// match the input matrix, stores the axes on the parent and returns every child (active and
// inactive) in axis order.
//
// Child create and update go through CreateItem and UpdateItem, each with its own transaction
// and outbox event, so the whole call is not one transaction. It is idempotent instead: rows are
// matched to existing children by attribute combination, so a retry after a partial failure
// updates what was already created rather than duplicating it.
func (s *Service) SaveVariants(ctx context.Context, tenantID, parentID uuid.UUID, in SaveVariantsInput) ([]VariantDTO, error) {
	parent, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.ID(parentID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrVariantParentNotFound
		}
		return nil, fmt.Errorf("items: load variant parent: %w", err)
	}
	if _, isChild := VariantOfFromMetadata(parent.Metadata); isChild {
		return nil, &VariantInputError{Msg: "a variant cannot have variants of its own"}
	}
	axes, rows, err := normalizeVariantInput(in)
	if err != nil {
		return nil, err
	}

	existing, err := s.loadChildItems(ctx, s.client, tenantID, []uuid.UUID{parentID}, false)
	if err != nil {
		return nil, err
	}
	byCombo := make(map[string]*ent.Item, len(existing))
	for _, c := range existing {
		byCombo[comboKey(VariantAttributesFromMetadata(c.Metadata))] = c
	}

	// Resolve each row's SKU and make sure no other item of the tenant holds it.
	type plan struct {
		row   VariantRowInput
		sku   string
		match *ent.Item
	}
	plans := make([]plan, len(rows))
	skus := make([]string, 0, len(rows))
	seen := make(map[string]int, len(rows))
	for i, r := range rows {
		p := plan{row: r, match: byCombo[comboKey(r.Attributes)]}
		switch {
		case r.SKU != "":
			p.sku = r.SKU
		case p.match != nil:
			p.sku = p.match.Sku
		default:
			p.sku = defaultVariantSKU(parent.Sku, r.Attributes)
		}
		if j, dup := seen[strings.ToLower(p.sku)]; dup {
			return nil, &VariantInputError{Msg: fmt.Sprintf("rows %d and %d resolve to the same SKU %q", j+1, i+1, p.sku)}
		}
		seen[strings.ToLower(p.sku)] = i
		plans[i] = p
		skus = append(skus, p.sku)
	}
	if len(skus) > 0 {
		holders, err := s.client.Item.Query().
			Where(item.TenantID(tenantID), item.SkuIn(skus...)).
			Select(item.FieldID, item.FieldSku).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("items: check variant SKUs: %w", err)
		}
		for _, h := range holders {
			for _, p := range plans {
				if p.sku == h.Sku && (p.match == nil || p.match.ID != h.ID) {
					return nil, &DuplicateSKUError{SKU: h.Sku}
				}
			}
		}
	}

	parentDTO := s.mapToDTO(parent)
	kept := make(map[uuid.UUID]bool, len(plans))
	for _, p := range plans {
		active := p.row.IsActive == nil || *p.row.IsActive
		name := variantName(parent.Name, p.row.Attributes)
		if p.match == nil {
			dto := childDTOFromParent(parentDTO)
			dto.SKU = p.sku
			dto.Name = name
			dto.Barcode = p.row.Barcode
			dto.IsActive = active
			dto.Metadata = map[string]any{
				MetaVariantOf:         parentID.String(),
				MetaVariantAttributes: attrsToAny(p.row.Attributes),
				// The child copies the parent's tax code, and who chose it with it, so an
				// automatic parent code does not become a hand-set one on its variants.
				MetaTaxCodeSource: TaxCodeSourceAuto,
			}
			if TaxCodeSource(parentDTO.Metadata) == TaxCodeSourceManual {
				dto.Metadata[MetaTaxCodeSource] = TaxCodeSourceManual
			}
			if p.row.Price != nil && *p.row.Price > 0 {
				// The create path turns selling_price into the Retail/Max and Wholesale/Min
				// guardrails and their tier prices (applyRequestedSellingPrice).
				price := *p.row.Price
				dto.SellingPrice = &price
				dto.MaxSellingPrice, dto.MinSellingPrice = nil, nil
			}
			created, err := s.CreateItem(ctx, tenantID, dto)
			if err != nil {
				return nil, err
			}
			kept[created.ID] = true
			continue
		}

		child := p.match
		kept[child.ID] = true
		changed := false
		if child.Sku != p.sku {
			if _, err := s.client.Item.UpdateOneID(child.ID).Where(item.TenantID(tenantID)).SetSku(p.sku).Save(ctx); err != nil {
				if ent.IsConstraintError(err) {
					return nil, &DuplicateSKUError{SKU: p.sku}
				}
				return nil, fmt.Errorf("items: rename variant SKU: %w", err)
			}
			child.Sku = p.sku
			changed = true // UpdateItem below publishes the new SKU
		}
		dto := *s.mapToDTO(child)
		if dto.Name != name {
			dto.Name, changed = name, true
		}
		if p.row.Barcode != "" && dto.Barcode != p.row.Barcode {
			dto.Barcode, changed = p.row.Barcode, true
		}
		if dto.IsActive != active {
			dto.IsActive, changed = active, true
		}
		if p.row.Price != nil && *p.row.Price > 0 && (dto.MaxSellingPrice == nil || *dto.MaxSellingPrice != *p.row.Price) {
			price := *p.row.Price
			dto.MaxSellingPrice = &price
			if dto.MinSellingPrice == nil || *dto.MinSellingPrice > price {
				minP := price
				dto.MinSellingPrice = &minP
			}
			changed = true
		}
		meta := copyMetadata(child.Metadata)
		// fmt.Sprint prints map keys sorted, so this compares the stored spelling exactly.
		if fmt.Sprint(VariantAttributesFromMetadata(meta)) != fmt.Sprint(p.row.Attributes) {
			changed = true
		}
		meta[MetaVariantOf] = parentID.String()
		meta[MetaVariantAttributes] = attrsToAny(p.row.Attributes)
		dto.Metadata = meta
		if !changed {
			continue
		}
		if _, err := s.UpdateItem(ctx, tenantID, child.ID, dto); err != nil {
			return nil, err
		}
	}

	// Children no longer in the matrix are deactivated, never deleted: they may carry stock,
	// sales and eTIMS history.
	for _, c := range existing {
		if kept[c.ID] || !c.IsActive {
			continue
		}
		if err := s.setActiveAndPublish(ctx, tenantID, c.ID, false); err != nil {
			return nil, err
		}
	}

	if err := s.setVariantAxes(ctx, tenantID, parentID, axes); err != nil {
		return nil, err
	}

	all, err := s.loadChildItems(ctx, s.client, tenantID, []uuid.UUID{parentID}, false)
	if err != nil {
		return nil, err
	}
	cfg, _ := s.client.TenantInventoryConfig.Query().Where(tenantinventoryconfig.TenantID(tenantID)).Only(ctx)
	out := s.childVariantDTOs(ctx, tenantID, all, cfg, nil)[parentID]
	sortVariantsByAxes(out, axes)
	if out == nil {
		out = []VariantDTO{}
	}
	return out, nil
}

// setActiveAndPublish flips is_active on one item and emits item.updated in the same transaction.
func (s *Service) setActiveAndPublish(ctx context.Context, tenantID, itemID uuid.UUID, active bool) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin tx: %w", err)
	}
	updated, err := tx.Item.UpdateOneID(itemID).Where(item.TenantID(tenantID)).SetIsActive(active).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: set variant active=%v: %w", active, err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// setVariantAxes merges variant_axes into the parent's metadata (other keys kept; an empty axes
// map removes the key) and emits item.updated for the parent. A no-op when nothing changed.
func (s *Service) setVariantAxes(ctx context.Context, tenantID, parentID uuid.UUID, axes map[string][]string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin tx: %w", err)
	}
	parent, err := tx.Item.Query().Where(item.TenantID(tenantID), item.ID(parentID)).Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: load variant parent: %w", err)
	}
	prev := VariantAxesFromMetadata(parent.Metadata)
	if fmt.Sprint(prev) == fmt.Sprint(axes) && (len(axes) > 0) == (parent.Metadata[MetaVariantAxes] != nil) {
		_ = tx.Rollback()
		return nil
	}
	meta := copyMetadata(parent.Metadata)
	if len(axes) == 0 {
		delete(meta, MetaVariantAxes)
	} else {
		stored := make(map[string]any, len(axes))
		for k, vals := range axes {
			stored[k] = vals
		}
		meta[MetaVariantAxes] = stored
	}
	updated, err := tx.Item.UpdateOneID(parentID).SetMetadata(meta).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: save variant axes: %w", err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// RefreshVariantAxes rebuilds each parent's variant_axes from its active children's attributes.
// The bulk import uses it after rows have set variant_of/variant_attributes directly. Values keep
// the parent's existing order where known; new values are appended in the order first seen.
func (s *Service) RefreshVariantAxes(ctx context.Context, tenantID uuid.UUID, parentIDs []uuid.UUID) error {
	if len(parentIDs) == 0 {
		return nil
	}
	children, err := s.loadChildItems(ctx, s.client, tenantID, parentIDs, true)
	if err != nil {
		return err
	}
	found := make(map[uuid.UUID]map[string][]string, len(parentIDs))
	for _, c := range children {
		pid, ok := VariantOfFromMetadata(c.Metadata)
		if !ok {
			continue
		}
		axes := found[pid]
		if axes == nil {
			axes = map[string][]string{}
			found[pid] = axes
		}
		for k, v := range VariantAttributesFromMetadata(c.Metadata) {
			if !containsFold(axes[k], v) {
				axes[k] = append(axes[k], v)
			}
		}
	}
	parents, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.IDIn(parentIDs...)).All(ctx)
	if err != nil {
		return fmt.Errorf("items: load variant parents: %w", err)
	}
	var errs []error
	for _, p := range parents {
		fresh := found[p.ID]
		merged := map[string][]string{}
		for k, vals := range VariantAxesFromMetadata(p.Metadata) {
			if _, still := fresh[k]; !still {
				continue
			}
			for _, v := range vals {
				if containsFold(fresh[k], v) {
					merged[k] = append(merged[k], v)
				}
			}
		}
		for _, k := range sortedAxisKeys(fresh) {
			for _, v := range fresh[k] {
				if !containsFold(merged[k], v) {
					merged[k] = append(merged[k], v)
				}
			}
		}
		if err := s.setVariantAxes(ctx, tenantID, p.ID, merged); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
