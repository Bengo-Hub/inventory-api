package items

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
)

func strPtr(s string) *string { return &s }

// The eTIMS enrichment is what treasury's item-master sync registers items from — every
// field must land in the event payload, and the quantity-unit resolution order is:
// item override > unit's KRA mapping > empty (treasury applies its own default).
func TestMergeEtimsEventFields(t *testing.T) {
	tests := []struct {
		name       string
		item       *ent.Item
		unitAbbrev string
		unitKraQty string
		wantQty    string
		wantCls    string
		wantPkg    string
	}{
		{
			name: "item override wins over unit mapping",
			item: &ent.Item{
				CountryOfOrigin: "KE", HsCode: "0902",
				EtimsItemClsCd: strPtr("5020230602"),
				EtimsPkgUnitCd: strPtr("CT"),
				EtimsQtyUnitCd: strPtr("KG"),
			},
			unitAbbrev: "kg", unitKraQty: "GRM",
			wantQty: "KG", wantCls: "5020230602", wantPkg: "CT",
		},
		{
			name:       "unit KRA mapping fills when item has no override",
			item:       &ent.Item{},
			unitAbbrev: "ltr", unitKraQty: "LTR",
			wantQty: "LTR", wantCls: "", wantPkg: "",
		},
		{
			name:    "everything unset stays empty (treasury defaults downstream)",
			item:    &ent.Item{},
			wantQty: "", wantCls: "", wantPkg: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{}
			mergeEtimsEventFields(payload, tt.item, tt.unitAbbrev, tt.unitKraQty)

			if got := payload["etims_qty_unit_cd"]; got != tt.wantQty {
				t.Errorf("etims_qty_unit_cd = %v, want %q", got, tt.wantQty)
			}
			if got := payload["etims_item_cls_cd"]; got != tt.wantCls {
				t.Errorf("etims_item_cls_cd = %v, want %q", got, tt.wantCls)
			}
			if got := payload["etims_pkg_unit_cd"]; got != tt.wantPkg {
				t.Errorf("etims_pkg_unit_cd = %v, want %q", got, tt.wantPkg)
			}
			if got := payload["country_of_origin"]; got != tt.item.CountryOfOrigin {
				t.Errorf("country_of_origin = %v, want %q", got, tt.item.CountryOfOrigin)
			}
			if got := payload["hs_code"]; got != tt.item.HsCode {
				t.Errorf("hs_code = %v, want %q", got, tt.item.HsCode)
			}
			if got := payload["unit_abbreviation"]; got != tt.unitAbbrev {
				t.Errorf("unit_abbreviation = %v, want %q", got, tt.unitAbbrev)
			}
		})
	}
}
