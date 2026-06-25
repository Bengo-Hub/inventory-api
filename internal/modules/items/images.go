package items

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
)

// AssetTypeImage is the only asset_type the HTTP layer currently manages. VIDEO / DOCUMENT /
// 3D_MODEL are valid ItemAsset values reserved for future use but out of scope for now.
const AssetTypeImage = "IMAGE"

// maxImageUploadBytes caps a single image upload (mirrors media.go's 2MB limit).
const maxImageUploadBytes = 2 * 1024 * 1024

// ItemImageDTO is the catalog-facing projection of an IMAGE ItemAsset. URLs are resolved
// (MEDIA_URL_BASE-prefixed) at read time, exactly like ItemDTO.ImageURL.
type ItemImageDTO struct {
	ID           uuid.UUID `json:"id"`
	URL          string    `json:"url"`
	FileName     string    `json:"file_name,omitempty"`
	MimeType     string    `json:"mime_type,omitempty"`
	DisplayOrder int       `json:"display_order"`
	IsPrimary    bool      `json:"is_primary"`
	CreatedAt    time.Time `json:"created_at"`
}

// SetMediaRoot wires the media root directory used to persist uploaded item images
// (mirrors the MediaHandler storage path: {root}/uploads/menu/...).
func (s *Service) SetMediaRoot(root string) {
	s.mediaRoot = root
}

// mapAssetToDTO converts an ItemAsset row to an ItemImageDTO with a resolved URL.
func (s *Service) mapAssetToDTO(a *ent.ItemAsset) ItemImageDTO {
	return ItemImageDTO{
		ID:           a.ID,
		URL:          s.resolveMediaURL(a.URL),
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

// storeImageFile re-encodes (stripping EXIF) and persists an uploaded image to
// {mediaRoot}/uploads/menu/{uuid}_{ts}.{ext}, returning the relative /media path, the
// detected mime type, and the generated filename. Logic mirrors handlers/media.go so item
// images share the same storage location/sanitisation as the legacy single-image uploader.
func (s *Service) storeImageFile(file multipart.File, header *multipart.FileHeader) (relPath, mimeType, filename string, err error) {
	// Detect content type from the leading bytes.
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	contentType := http.DetectContentType(buffer[:n])
	if _, serr := file.Seek(0, 0); serr != nil {
		return "", "", "", fmt.Errorf("items: seek upload: %w", serr)
	}

	allowed := map[string]bool{"image/jpeg": true, "image/jpg": true, "image/png": true}
	if !allowed[contentType] {
		ext := strings.ToLower(filepath.Ext(header.Filename))
		headerCT := header.Header.Get("Content-Type")
		switch {
		case ext == ".jpg" || ext == ".jpeg" || headerCT == "image/jpeg":
			contentType = "image/jpeg"
		case ext == ".png" || headerCT == "image/png":
			contentType = "image/png"
		}
	}
	if !allowed[contentType] {
		return "", "", "", fmt.Errorf("items: only JPEG, JPG and PNG images are allowed")
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		if contentType == "image/png" {
			ext = ".png"
		} else {
			ext = ".jpg"
		}
	}
	filename = fmt.Sprintf("%s_%d%s", uuid.New().String(), time.Now().Unix(), ext)

	root := s.mediaRoot
	if root == "" {
		root = "./media"
	}
	dir := filepath.Join(root, "uploads", "menu")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return "", "", "", fmt.Errorf("items: create upload dir: %w", mkErr)
	}
	dstPath := filepath.Join(dir, filename)
	dst, cErr := os.Create(dstPath)
	if cErr != nil {
		return "", "", "", fmt.Errorf("items: create file: %w", cErr)
	}
	defer dst.Close()

	// Re-encode to strip EXIF / neutralise image-based exploits; fall back to raw copy if decode fails.
	written := false
	if img, _, decErr := image.Decode(file); decErr == nil {
		var encErr error
		if contentType == "image/png" {
			encErr = png.Encode(dst, img)
		} else {
			encErr = jpeg.Encode(dst, img, &jpeg.Options{Quality: 85})
		}
		if encErr == nil {
			written = true
		} else {
			if _, sErr := file.Seek(0, 0); sErr != nil {
				return "", "", "", fmt.Errorf("items: seek for fallback copy: %w", sErr)
			}
			_ = dst.Truncate(0)
			_, _ = dst.Seek(0, 0)
		}
	}
	if !written {
		if _, copyErr := io.Copy(dst, file); copyErr != nil {
			return "", "", "", fmt.Errorf("items: write file: %w", copyErr)
		}
	}

	return fmt.Sprintf("/media/uploads/menu/%s", filename), contentType, filename, nil
}

// AddItemImage stores an uploaded image and creates an IMAGE ItemAsset for the item. When the
// item has no images yet (or setPrimary is requested) the new asset becomes the primary and the
// item's legacy image_url mirror is updated, so existing single-image clients keep working.
// The per-item / feature subscription gating happens in the HTTP layer before this is called.
func (s *Service) AddItemImage(ctx context.Context, tenantID, itemID uuid.UUID, file multipart.File, header *multipart.FileHeader, setPrimary bool) (*ItemImageDTO, error) {
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

	relPath, mimeType, filename, err := s.storeImageFile(file, header)
	if err != nil {
		return nil, err
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
		SetURL(relPath).
		SetFileName(filename).
		SetMimeType(mimeType).
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
			SetImageURL(relPath).Save(ctx); uerr != nil {
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
	return nil
}
