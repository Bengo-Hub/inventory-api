package items

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
)

// KRA eTIMS item classification sources reported on an item (ItemDTO.EtimsItemClsSource).
const (
	KRAClassFromItem     = "item"
	KRAClassFromCategory = "category"
)

// kraItemClsPattern is the shape of a KRA item classification code: a UNSPSC leaf of 8 to 10
// digits (KRA's own list uses 10, e.g. 1000000000 general goods).
var kraItemClsPattern = regexp.MustCompile(`^[0-9]{8,10}$`)

// NormalizeKRAItemClass trims a classification code and reports whether it has KRA's shape.
// An empty code is valid (it clears the setting).
func NormalizeKRAItemClass(code string) (string, bool) {
	code = strings.TrimSpace(code)
	return code, code == "" || kraItemClsPattern.MatchString(code)
}

// resolveCategoryKRAClasses returns, for each given category, the KRA classification its items
// register with when they carry none of their own: the category's own kra_item_cls_cd, else the
// nearest ancestor's (from the materialized path root/.../self). Ancestors not already loaded are
// fetched in one query, so any number of categories costs at most one extra round trip.
func (s *Service) resolveCategoryKRAClasses(ctx context.Context, cats []*ent.ItemCategory) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(cats))
	if len(cats) == 0 {
		return out
	}
	byID := make(map[uuid.UUID]*ent.ItemCategory, len(cats))
	for _, c := range cats {
		byID[c.ID] = c
	}
	var missing []uuid.UUID
	for _, c := range cats {
		if c.KraItemClsCd != "" {
			continue
		}
		for _, id := range categoryAncestorIDs(c) {
			if _, ok := byID[id]; !ok {
				missing = append(missing, id)
				byID[id] = nil // queued, not yet loaded
			}
		}
	}
	if len(missing) > 0 {
		if rows, err := s.client.ItemCategory.Query().Where(itemcategory.IDIn(missing...)).All(ctx); err == nil {
			for _, r := range rows {
				byID[r.ID] = r
			}
		}
	}
	for _, c := range cats {
		if c.KraItemClsCd != "" {
			out[c.ID] = c.KraItemClsCd
			continue
		}
		ancestors := categoryAncestorIDs(c)
		for i := len(ancestors) - 1; i >= 0; i-- { // nearest ancestor first
			if a := byID[ancestors[i]]; a != nil && a.KraItemClsCd != "" {
				out[c.ID] = a.KraItemClsCd
				break
			}
		}
	}
	return out
}

// categoryAncestorIDs reads a category's ancestors from its materialized path, root first,
// excluding the category itself. A legacy row with no path falls back to its direct parent.
func categoryAncestorIDs(c *ent.ItemCategory) []uuid.UUID {
	var ids []uuid.UUID
	if c.Path != "" {
		for _, part := range strings.Split(c.Path, "/") {
			if id, err := uuid.Parse(part); err == nil && id != c.ID {
				ids = append(ids, id)
			}
		}
		return ids
	}
	if c.ParentID != nil {
		ids = append(ids, *c.ParentID)
	}
	return ids
}

// categoryKRAClass is resolveCategoryKRAClasses for one category id ("" when unset or unknown).
func (s *Service) categoryKRAClass(ctx context.Context, categoryID *uuid.UUID) string {
	if categoryID == nil {
		return ""
	}
	c, err := s.client.ItemCategory.Get(ctx, *categoryID)
	if err != nil {
		return ""
	}
	return s.resolveCategoryKRAClasses(ctx, []*ent.ItemCategory{c})[c.ID]
}

// itemKRAClass is the classification an item registers with at KRA and where it came from: the
// item's own etims_item_cls_cd, else its category's. Empty when neither is set (treasury then uses
// the general goods default).
func itemKRAClass(i *ent.Item, categoryClass string) (code, source string) {
	if own := derefStr(i.EtimsItemClsCd); own != "" {
		return own, KRAClassFromItem
	}
	if categoryClass != "" {
		return categoryClass, KRAClassFromCategory
	}
	return "", ""
}
