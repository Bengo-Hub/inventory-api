package items

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// fakeBucket is a minimal S3 endpoint: PUT, GET, HEAD and DELETE of objects, HEAD of buckets.
func fakeBucket(t *testing.T) (map[string][]byte, string) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objects[p] = b
		case http.MethodHead, http.MethodGet:
			b, ok := objects[p]
			if !ok && strings.Contains(p, "/") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
		case http.MethodDelete:
			delete(objects, p)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return objects, srv.URL
}

// TestRelinkMedia moves every referenced old /media file to its tenant's keys and updates the
// rows, publishes item.updated for items, skips the shipped icons, lists missing files, and
// changes nothing on a second run.
func TestRelinkMedia(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_relink_")
	ctx := context.Background()
	objects, endpoint := fakeBucket(t)

	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("uploads/menu/photo.jpg", "jpeg-bytes")
	write("images/acme/brand.png", "png-bytes")
	write("icons/coffee-colored.svg", "<svg/>")
	write("uploads/menu/unused.jpg", "nobody uses this")

	store, err := objectstore.New("inventory", objectstore.Config{
		Endpoint: endpoint, AccessKeyID: "AK", SecretAccessKey: "SK",
		PublicBucket: "pub", PrivateBucket: "priv", PublicBaseURL: "https://media.example.com",
		LocalRoot: root, LocalURLBase: "https://inventoryapi.example.com",
	})
	require.NoError(t, err)
	svc := NewService(client, zap.NewNop(), store)

	tenantID := uuid.New()
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Acme").SetSlug("acme-" + tenantID.String()[:8]).Save(ctx)
	require.NoError(t, err)
	it, err := client.Item.Create().SetTenantID(tenantID).SetSku("A-1").SetName("Coffee").
		SetImageURL("/media/uploads/menu/photo.jpg").Save(ctx)
	require.NoError(t, err)
	asset, err := client.ItemAsset.Create().SetItemID(it.ID).SetAssetType(AssetTypeImage).
		SetURL("https://inventoryapi.example.com/media/uploads/menu/photo.jpg").SetIsPrimary(true).Save(ctx)
	require.NoError(t, err)
	brand, err := client.ItemBrand.Create().SetTenantID(tenantID).SetName("Brand").SetCode("BR").
		SetLogoURL("/media/images/acme/brand.png").Save(ctx)
	require.NoError(t, err)
	sup, err := client.Supplier.Create().SetTenantID(tenantID).SetName("Supplier").SetCode("SUP-1").
		SetLogoURL("/media/uploads/menu/gone.png").Save(ctx)
	require.NoError(t, err)
	cat, err := client.ItemCategory.Create().SetTenantID(tenantID).SetName("Drinks").SetCode("DR").
		SetIcon("/media/icons/coffee-colored.svg").Save(ctx)
	require.NoError(t, err)
	demo, err := client.Item.Create().SetTenantID(tenantID).SetSku("DEMO-1").SetName("Demo latte").
		SetImageURL("/media/images/outlets/menu/icedlatte.jpeg").Save(ctx)
	require.NoError(t, err)

	// A report changes nothing.
	rep, err := svc.RelinkMedia(ctx, false)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Found["items"])
	require.Equal(t, 1, rep.Found["item_assets"])
	require.Equal(t, 0, rep.Found["item_categories"], "shipped icons stay")
	require.Equal(t, 1, rep.Shipped["items"], "shipped demo pictures stay")
	require.Empty(t, objects)

	rep, err = svc.RelinkMedia(ctx, true)
	require.NoError(t, err)
	require.Equal(t, 2, rep.Moved, "the photo once, the brand logo once; unused files are not moved")
	require.Equal(t, 1, rep.Reused, "the gallery row reuses the item photo")
	require.Len(t, rep.Missing, 1)
	require.Contains(t, rep.Missing[0], "suppliers/")

	it = client.Item.GetX(ctx, it.ID)
	require.True(t, strings.HasPrefix(it.ImageURL, "inventory/"+tenantID.String()+"/item-images/"), it.ImageURL)
	require.Equal(t, it.ImageURL, client.ItemAsset.GetX(ctx, asset.ID).URL)
	require.Equal(t, "jpeg-bytes", string(objects["pub/"+it.ImageURL]))
	logo := client.ItemBrand.GetX(ctx, brand.ID).LogoURL
	require.True(t, strings.HasPrefix(logo, "inventory/"+tenantID.String()+"/logos/"), logo)
	require.Equal(t, "/media/uploads/menu/gone.png", client.Supplier.GetX(ctx, sup.ID).LogoURL, "missing file: row unchanged")
	require.Equal(t, "/media/icons/coffee-colored.svg", client.ItemCategory.GetX(ctx, cat.ID).Icon)
	require.Equal(t, "/media/images/outlets/menu/icedlatte.jpeg", client.Item.GetX(ctx, demo.ID).ImageURL)

	// The item's consumers hear about the new link.
	events := client.OutboxEvent.Query().Where(outboxevent.EventType("item.updated")).AllX(ctx)
	require.Len(t, events, 1)
	require.Contains(t, string(events[0].Payload), "https://media.example.com/"+it.ImageURL)

	// Reads give the public link.
	require.Equal(t, "https://media.example.com/"+it.ImageURL, svc.mediaURL(it.ImageURL))

	// A second run finds only the missing file again and uploads nothing.
	before := len(objects)
	rep, err = svc.RelinkMedia(ctx, true)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Moved)
	require.Equal(t, before, len(objects))
	require.Equal(t, 1, rep.Found["suppliers"])

	// Replacing an image frees the old file only when nothing of the tenant still shows it: the
	// gallery row still uses the photo, so it stays; once the gallery row goes too, it is deleted.
	photo := it.ImageURL
	client.Item.UpdateOneID(it.ID).SetImageURL("https://cdn.example.com/new.png").ExecX(ctx)
	svc.dropImage(ctx, tenantID, photo)
	require.Contains(t, objects, "pub/"+photo, "still used by the gallery row")
	client.ItemAsset.DeleteOneID(asset.ID).ExecX(ctx)
	svc.dropImage(ctx, tenantID, photo)
	require.NotContains(t, objects, "pub/"+photo, "unused file deleted")
	// Old volume files and external URLs are never deleted.
	svc.dropImage(ctx, tenantID, "/media/uploads/menu/photo.jpg")
	_, statErr := os.Stat(filepath.Join(root, "uploads", "menu", "photo.jpg"))
	require.NoError(t, statErr)
}
