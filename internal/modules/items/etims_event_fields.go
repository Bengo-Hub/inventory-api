package items

import "github.com/bengobox/inventory-service/internal/ent"

// derefStr returns the value of an optional string field, or "" when unset.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// mergeEtimsEventFields adds the KRA eTIMS catalog fields to an item event payload.
// Inventory is the item source of truth: treasury's eTIMS item-master sync
// (inventory.item.created/updated → saveItemInfo) reads these instead of hardcoding
// classification defaults.
//
// etims_qty_unit_cd resolution order: the item's own override, else the unit's KRA
// mapping (Unit.kra_qty_unit_cd), else empty — treasury applies its own default ("U")
// so an unmapped unit never blocks registration.
func mergeEtimsEventFields(payload map[string]any, i *ent.Item, unitAbbrev, unitKraQty string) {
	qtyUnit := derefStr(i.EtimsQtyUnitCd)
	if qtyUnit == "" {
		qtyUnit = unitKraQty
	}
	payload["country_of_origin"] = i.CountryOfOrigin
	payload["hs_code"] = i.HsCode
	payload["unit_abbreviation"] = unitAbbrev
	payload["etims_item_cls_cd"] = derefStr(i.EtimsItemClsCd)
	payload["etims_pkg_unit_cd"] = derefStr(i.EtimsPkgUnitCd)
	payload["etims_qty_unit_cd"] = qtyUnit
}
