package items

import (
	"context"
	"errors"
	"fmt"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
	"github.com/bengobox/inventory-service/internal/ent/itembrand"
	"github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/ent/itemvariant"
	"github.com/bengobox/inventory-service/internal/ent/supplier"
)

// relinkPage is the keyset page size of the media relink: small enough to keep each read short,
// large enough that a catalogue of a few hundred thousand rows takes minutes, not hours.
const relinkPage = 500

// shippedIconsPrefix holds the category icons the image ships (copied to MEDIA_ROOT/icons at
// start). They are platform assets, not tenant media, and stay served from the image.
const shippedIconsPrefix = "/media/icons/"

// MediaRelinkReport counts one relink run, per field.
type MediaRelinkReport struct {
	Apply    bool           `json:"apply"`
	Found    map[string]int `json:"found"`    // rows still on an old /media path
	Relinked map[string]int `json:"relinked"` // rows updated to an object key
	Moved    int            `json:"files_moved"`
	Reused   int            `json:"files_reused"`
	Missing  []string       `json:"missing"` // "<table>/<id>: <old path>" whose file is gone
}

// RelinkMedia moves every image a row still references by an old media volume path to object
// storage and points the row at the new key: item images (an item.updated event is published so
// catalogue consumers refresh their copy), gallery images, variant images, category icons (the
// shipped icon set excepted), brand and supplier logos. Without apply it only counts. Rows whose
// file is missing are listed and left unchanged. Safe to run again: moved rows are skipped and no
// file is uploaded twice.
func (s *Service) RelinkMedia(ctx context.Context, apply bool) (*MediaRelinkReport, error) {
	if !s.media.Remote() {
		return nil, fmt.Errorf("items: relinking needs object storage configured (OBJECT_STORAGE_*)")
	}
	rep := &MediaRelinkReport{Apply: apply, Found: map[string]int{}, Relinked: map[string]int{}}
	rl := s.media.NewRelinker()
	owner := func(t uuid.UUID) objectstore.Owner { return objectstore.Owner{TenantID: t.String()} }
	// relink returns the new value for one row's field, or "" when the row stays as it is.
	relink := func(table string, id uuid.UUID, ref string, t uuid.UUID, mediaType string) (string, error) {
		rep.Found[table]++
		if !apply {
			return "", nil
		}
		key, changed, err := rl.Relink(ctx, ref, owner(t), mediaType)
		if errors.Is(err, objectstore.ErrNotFound) {
			rep.Missing = append(rep.Missing, fmt.Sprintf("%s/%s: %s", table, id, ref))
			return "", nil
		}
		if err != nil || !changed {
			return "", err
		}
		rep.Relinked[table]++
		return key, nil
	}

	// Items: the primary image, mirrored to catalogue consumers through item.updated.
	for after := uuid.Nil; ; {
		rows, err := s.client.Item.Query().
			Where(item.IDGT(after), item.ImageURLContains(objectstore.LegacyPrefix)).
			Order(ent.Asc(item.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list items: %w", err)
		}
		for _, it := range rows {
			key, err := relink("items", it.ID, it.ImageURL, it.TenantID, objectstore.TypeItemImages)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.relinkItemImage(ctx, it, key); err != nil {
					return rep, err
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}

	// Gallery images and variant images carry no tenant; it comes from the item.
	for after := uuid.Nil; ; {
		rows, err := s.client.ItemAsset.Query().
			Where(itemasset.IDGT(after), itemasset.URLContains(objectstore.LegacyPrefix)).
			WithItem(func(q *ent.ItemQuery) { q.Select(item.FieldTenantID) }).
			Order(ent.Asc(itemasset.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list gallery images: %w", err)
		}
		for _, a := range rows {
			if a.Edges.Item == nil {
				continue
			}
			key, err := relink("item_assets", a.ID, a.URL, a.Edges.Item.TenantID, objectstore.TypeItemImages)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.client.ItemAsset.UpdateOneID(a.ID).SetURL(key).Exec(ctx); err != nil {
					return rep, fmt.Errorf("items: relink gallery image %s: %w", a.ID, err)
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}
	for after := uuid.Nil; ; {
		rows, err := s.client.ItemVariant.Query().
			Where(itemvariant.IDGT(after), itemvariant.ImageURLContains(objectstore.LegacyPrefix)).
			WithItem(func(q *ent.ItemQuery) { q.Select(item.FieldTenantID) }).
			Order(ent.Asc(itemvariant.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list variants: %w", err)
		}
		for _, v := range rows {
			if v.Edges.Item == nil {
				continue
			}
			key, err := relink("item_variants", v.ID, v.ImageURL, v.Edges.Item.TenantID, objectstore.TypeItemImages)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.client.ItemVariant.UpdateOneID(v.ID).SetImageURL(key).Exec(ctx); err != nil {
					return rep, fmt.Errorf("items: relink variant image %s: %w", v.ID, err)
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}

	// Category icons that are uploaded pictures (not an icon name, not the shipped icon set).
	for after := uuid.Nil; ; {
		rows, err := s.client.ItemCategory.Query().
			Where(itemcategory.IDGT(after), itemcategory.IconContains(objectstore.LegacyPrefix),
				itemcategory.Not(itemcategory.IconContains(shippedIconsPrefix))).
			Order(ent.Asc(itemcategory.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list categories: %w", err)
		}
		for _, c := range rows {
			if c.TenantID == uuid.Nil {
				continue // a global category has no tenant to own its picture
			}
			key, err := relink("item_categories", c.ID, c.Icon, c.TenantID, objectstore.TypeImages)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.client.ItemCategory.UpdateOneID(c.ID).SetIcon(key).Exec(ctx); err != nil {
					return rep, fmt.Errorf("items: relink category icon %s: %w", c.ID, err)
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}

	// Brand and supplier logos.
	for after := uuid.Nil; ; {
		rows, err := s.client.ItemBrand.Query().
			Where(itembrand.IDGT(after), itembrand.LogoURLContains(objectstore.LegacyPrefix)).
			Order(ent.Asc(itembrand.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list brands: %w", err)
		}
		for _, b := range rows {
			key, err := relink("item_brands", b.ID, b.LogoURL, b.TenantID, objectstore.TypeLogos)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.client.ItemBrand.UpdateOneID(b.ID).SetLogoURL(key).Exec(ctx); err != nil {
					return rep, fmt.Errorf("items: relink brand logo %s: %w", b.ID, err)
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}
	for after := uuid.Nil; ; {
		rows, err := s.client.Supplier.Query().
			Where(supplier.IDGT(after), supplier.LogoURLContains(objectstore.LegacyPrefix)).
			Order(ent.Asc(supplier.FieldID)).Limit(relinkPage).All(ctx)
		if err != nil {
			return rep, fmt.Errorf("items: list suppliers: %w", err)
		}
		for _, sp := range rows {
			key, err := relink("suppliers", sp.ID, sp.LogoURL, sp.TenantID, objectstore.TypeLogos)
			if err != nil {
				return rep, err
			}
			if key != "" {
				if err := s.client.Supplier.UpdateOneID(sp.ID).SetLogoURL(key).Exec(ctx); err != nil {
					return rep, fmt.Errorf("items: relink supplier logo %s: %w", sp.ID, err)
				}
			}
		}
		if len(rows) < relinkPage {
			break
		}
		after = rows[len(rows)-1].ID
	}

	rep.Moved, rep.Reused = rl.Moved, rl.Reused
	return rep, nil
}

// relinkItemImage points an item at its moved image and publishes item.updated in the same
// transaction (outbox), so catalogue consumers replace the old link they hold.
func (s *Service) relinkItemImage(ctx context.Context, it *ent.Item, key string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin tx: %w", err)
	}
	updated, err := tx.Item.UpdateOneID(it.ID).Where(item.TenantID(it.TenantID)).SetImageURL(key).Save(ctx)
	if err == nil {
		err = s.emitItemUpdatedEvent(ctx, tx, it.TenantID, updated)
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("items: relink item image %s: %w", it.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("items: relink item image %s: %w", it.ID, err)
	}
	return nil
}
