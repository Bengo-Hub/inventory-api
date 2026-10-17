package items

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
)

// AssetTypeImage is the only asset_type the HTTP layer currently manages. VIDEO / DOCUMENT /
// 3D_MODEL are valid ItemAsset values reserved for future use but out of scope for now.
const AssetTypeImage = "IMAGE"

// ItemImageDTO is the catalog-facing projection of an IMAGE ItemAsset. URLs are resolved from the
// stored key at read time, exactly like ItemDTO.ImageURL.
type ItemImageDTO struct {
	ID           uuid.UUID `json:"id"`
	URL          string    `json:"url"`
	FileName     string    `json:"file_name,omitempty"`
	MimeType     string    `json:"mime_type,omitempty"`
	DisplayOrder int       `json:"display_order"`
	IsPrimary    bool      `json:"is_primary"`
	CreatedAt    time.Time `json:"created_at"`
}

// mapAssetToDTO converts an ItemAsset row to an ItemImageDTO with a resolved URL.
func (s *Service) mapAssetToDTO(a *ent.ItemAsset) ItemImageDTO {
	return ItemImageDTO{
		ID:           a.ID,
		URL:          s.mediaURL(a.URL),
		FileName:     a.FileName,
		MimeType:     a.MimeType,
		DisplayOrder: a.DisplayOrder,
		IsPrimary:    a.IsPrimary,
		CreatedAt:    a.CreatedAt,
	}
}

// resolveTenantItem verifies the item belongs to the tenant and returns it.
func (s *Service) resolveTenantItem(ctx context.Context, tenantID, itemID uuid.UUID) (*ent.Item, error) {
	itm, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.ID(itemID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: item not found")
		}
		return nil, fmt.Errorf("items: lookup item: %w", err)
	}
	return itm, nil
}

// CountItemImages returns the number of IMAGE assets attached to an item. Used by the HTTP
// layer to enforce the per-item image subscription cap before accepting an upload.
func (s *Service) CountItemImages(ctx context.Context, tenantID, itemID uuid.UUID) (int, error) {
	if _, err := s.resolveTenantItem(ctx, tenantID, itemID); err != nil {
		return 0, err
	}
	return s.client.ItemAsset.Query().
		Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage)).
		Count(ctx)
}

// ListItemImages returns an item's IMAGE assets ordered by is_primary (primary first) then
// display_order then created_at, with resolved URLs.
func (s *Service) ListItemImages(ctx context.Context, tenantID, itemID uuid.UUID) ([]ItemImageDTO, error) {
	if _, err := s.resolveTenantItem(ctx, tenantID, itemID); err != nil {
		return nil, err
	}
	assets, err := s.client.ItemAsset.Query().
		Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage)).
		Order(
			ent.Desc(itemasset.FieldIsPrimary),
			ent.Asc(itemasset.FieldDisplayOrder),
			ent.Asc(itemasset.FieldCreatedAt),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: list images: %w", err)
	}
	out := make([]ItemImageDTO, len(assets))
	for i, a := range assets {
		out[i] = s.mapAssetToDTO(a)
	}
	return out, nil
}

// AddItemImage stores an uploaded image and creates an IMAGE ItemAsset for the item. When the
// item has no images yet (or setPrimary is requested) the new asset becomes the primary and the
// item's legacy image_url mirror is updated, so existing single-image clients keep working.
// The per-item / feature subscription gating happens in the HTTP layer before this is called.
//
// up is an upload already validated and re-encoded by objectstore.ReadUpload (item-images type).
func (s *Service) AddItemImage(ctx context.Context, tenantID, itemID uuid.UUID, up *objectstore.Upload, setPrimary bool) (*ItemImageDTO, error) {
	itm, err := s.resolveTenantItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}

	existing, err := s.client.ItemAsset.Query().
		Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage)).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: count existing images: %w", err)
	}
	// The very first image is always primary regardless of the flag.
	makePrimary := setPrimary || existing == 0

	key, err := s.media.NewKey(objectstore.Owner{TenantID: tenantID.String()}, objectstore.TypeItemImages, up.Ext)
	if err != nil {
		return nil, fmt.Errorf("items: name image: %w", err)
	}
	if err = s.media.Put(ctx, key, up.Data, up.ContentType); err != nil {
		return nil, fmt.Errorf("items: store image: %w", err)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		s.dropImage(ctx, tenantID, key)
		return nil, fmt.Errorf("items: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			s.dropImage(ctx, tenantID, key) // keep storage and the gallery in step
		}
	}()

	if makePrimary {
		// Demote any current primary so there is exactly one primary image.
		if _, uerr := tx.ItemAsset.Update().
			Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage), itemasset.IsPrimary(true)).
			SetIsPrimary(false).Save(ctx); uerr != nil {
			err = fmt.Errorf("items: demote primary: %w", uerr)
			return nil, err
		}
	}

	asset, err := tx.ItemAsset.Create().
		SetItemID(itemID).
		SetAssetType(AssetTypeImage).
		SetURL(key).
		SetFileName(path.Base(key)).
		SetMimeType(up.ContentType).
		SetDisplayOrder(existing).
		SetIsPrimary(makePrimary).
		Save(ctx)
	if err != nil {
		err = fmt.Errorf("items: create asset: %w", err)
		return nil, err
	}

	// Keep the legacy image_url in sync with the primary asset for backward compatibility.
	if makePrimary {
		if _, uerr := tx.Item.UpdateOneID(itm.ID).
			Where(item.TenantID(tenantID)).
			SetImageURL(key).Save(ctx); uerr != nil {
			err = fmt.Errorf("items: sync image_url: %w", uerr)
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit: %w", err)
	}

	dto := s.mapAssetToDTO(asset)
	return &dto, nil
}

// UpdateItemImageInput carries the mutable fields of an image PATCH. Nil fields are unchanged.
type UpdateItemImageInput struct {
	DisplayOrder *int  `json:"display_order,omitempty"`
	IsPrimary    *bool `json:"is_primary,omitempty"`
}

// UpdateItemImage reorders an image and/or sets it as the primary (unsetting others). When an
// image becomes primary the item's legacy image_url mirror is updated too.
func (s *Service) UpdateItemImage(ctx context.Context, tenantID, itemID, imageID uuid.UUID, in UpdateItemImageInput) (*ItemImageDTO, error) {
	itm, err := s.resolveTenantItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	// Ensure the asset belongs to this item.
	asset, err := s.client.ItemAsset.Query().
		Where(itemasset.ID(imageID), itemasset.ItemID(itemID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("items: image not found")
		}
		return nil, fmt.Errorf("items: lookup image: %w", err)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	upd := tx.ItemAsset.UpdateOneID(asset.ID)
	if in.DisplayOrder != nil {
		upd = upd.SetDisplayOrder(*in.DisplayOrder)
	}
	makePrimary := in.IsPrimary != nil && *in.IsPrimary
	if makePrimary {
		if _, uerr := tx.ItemAsset.Update().
			Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage), itemasset.IDNEQ(asset.ID), itemasset.IsPrimary(true)).
			SetIsPrimary(false).Save(ctx); uerr != nil {
			err = fmt.Errorf("items: demote primary: %w", uerr)
			return nil, err
		}
		upd = upd.SetIsPrimary(true)
	} else if in.IsPrimary != nil {
		upd = upd.SetIsPrimary(false)
	}

	updated, err := upd.Save(ctx)
	if err != nil {
		err = fmt.Errorf("items: update image: %w", err)
		return nil, err
	}

	if makePrimary {
		if _, uerr := tx.Item.UpdateOneID(itm.ID).
			Where(item.TenantID(tenantID)).
			SetImageURL(updated.URL).Save(ctx); uerr != nil {
			err = fmt.Errorf("items: sync image_url: %w", uerr)
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: commit: %w", err)
	}

	dto := s.mapAssetToDTO(updated)
	return &dto, nil
}

// DeleteItemImage removes an IMAGE asset. If the deleted image was primary, the next image
// (by display_order) is promoted to primary and the item's legacy image_url mirror is updated
// (cleared when no images remain).
func (s *Service) DeleteItemImage(ctx context.Context, tenantID, itemID, imageID uuid.UUID) error {
	itm, err := s.resolveTenantItem(ctx, tenantID, itemID)
	if err != nil {
		return err
	}
	asset, err := s.client.ItemAsset.Query().
		Where(itemasset.ID(imageID), itemasset.ItemID(itemID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("items: image not found")
		}
		return fmt.Errorf("items: lookup image: %w", err)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("items: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = tx.ItemAsset.DeleteOneID(asset.ID).Exec(ctx); err != nil {
		err = fmt.Errorf("items: delete image: %w", err)
		return err
	}

	if asset.IsPrimary {
		// Promote the next image (lowest display_order) to primary, if any.
		next, nerr := tx.ItemAsset.Query().
			Where(itemasset.ItemID(itemID), itemasset.AssetType(AssetTypeImage)).
			Order(ent.Asc(itemasset.FieldDisplayOrder), ent.Asc(itemasset.FieldCreatedAt)).
			First(ctx)
		newPrimaryURL := ""
		if nerr == nil {
			if _, uerr := tx.ItemAsset.UpdateOneID(next.ID).SetIsPrimary(true).Save(ctx); uerr != nil {
				err = fmt.Errorf("items: promote primary: %w", uerr)
				return err
			}
			newPrimaryURL = next.URL
		}
		if _, uerr := tx.Item.UpdateOneID(itm.ID).
			Where(item.TenantID(tenantID)).
			SetImageURL(newPrimaryURL).Save(ctx); uerr != nil {
			err = fmt.Errorf("items: sync image_url: %w", uerr)
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("items: commit: %w", err)
	}
	s.dropImage(ctx, tenantID, asset.URL)
	return nil
}

// dropImage deletes a stored image once nothing references it any more: only this service's own
// keys (an old /media file or an external URL may be shared and is left alone), and only when no
// item or gallery row still points at it. Best effort: a failure leaves an orphan, never a broken
// image.
//
// A key carries its tenant, so only that tenant's rows can reference it; both checks are scoped to
// the tenant (tenant_id index) and stop at the first match.
func (s *Service) dropImage(ctx context.Context, tenantID uuid.UUID, ref string) {
	if !s.media.Owned(ref) {
		return
	}
	key := s.media.Normalize(ref)
	if used, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.ImageURL(key)).Exist(ctx); err != nil || used {
		return
	}
	if used, err := s.client.ItemAsset.Query().
		Where(itemasset.URL(key), itemasset.HasItemWith(item.TenantID(tenantID))).Exist(ctx); err != nil || used {
		return
	}
	if err := s.media.Delete(ctx, key); err != nil {
		s.log.Warn("item image not deleted from storage", zap.String("key", key), zap.Error(err))
	}
}
