package items

import (
	"context"
	"testing"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	"github.com/bengobox/inventory-service/internal/testutil"
)

func TestImageQueryAndTrigramSimilarity(t *testing.T) {
	require.Equal(t, "bread tins 600ml", imageQuery(`C:\photos\Bread_tins_600ml.JPG`))
	require.Equal(t, "zebra dumb cane plant", imageQuery("Zebra_-_Dumb  Cane Plant.png"))
	require.Equal(t, "ACM-00001", fileStem("ACM-00001.jpg"))

	require.InDelta(t, 1.0, trigramSimilarity("Stanley cup", "stanley_cup"), 1e-9)
	require.Zero(t, trigramSimilarity("", "cup"))
	// Values Postgres 17 pg_trgm returns for the same pairs.
	require.InDelta(t, 0.2857143, trigramSimilarity("cat", "cart"), 1e-6)
	require.InDelta(t, 0.5263158, trigramSimilarity("black pan", "Black non-stick Pan"), 1e-6)
	require.Greater(t, trigramSimilarity("black pan", "Black non-stick Pan"), trigramSimilarity("black pan", "Black jacket"))
}

// TestMatchItemImages: a file named after a SKU or barcode wins outright, an exact name is
// marked exact, a close name is offered as similar with alternatives, nothing from another
// tenant is ever suggested, and a name with no match says so.
func TestMatchItemImages(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_imgmatch_")
	ctx := context.Background()
	svc := NewService(client, zap.NewNop(), nil)

	tenantID, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tenantID, other} {
		_, err := client.Tenant.Create().SetID(id).SetName("Shop").SetSlug("shop-" + id.String()[:8]).Save(ctx)
		require.NoError(t, err)
	}
	mk := func(tid uuid.UUID, sku, name, barcode string) *ent.Item {
		c := client.Item.Create().SetTenantID(tid).SetSku(sku).SetName(name).SetType(item.TypeGOODS)
		if barcode != "" {
			c.SetBarcode(barcode)
		}
		it, err := c.Save(ctx)
		require.NoError(t, err)
		return it
	}
	pan := mk(tenantID, "ACM-00077", "Black non-stick Pan", "")
	jacket := mk(tenantID, "ACM-00496", "Black jacket", "")
	cup := mk(tenantID, "ACM-01536", "Stanley cup", "")
	coded := mk(tenantID, "ACM-00900", "Phone charm", "6001234567890")
	mk(other, "OTH-1", "Gold candle", "")

	res, err := svc.MatchItemImages(ctx, tenantID, []string{
		"ACM-00077.png", "6001234567890.jpg", "Stanley_cup.jpg", "Black pan.jpg", "Gold candle.jpg", "qqqq.png",
	})
	require.NoError(t, err)
	require.Len(t, res, 6)

	require.Equal(t, ImageMatchSKU, res[0].Kind)
	require.Equal(t, pan.ID, res[0].Match.ItemID)

	require.Equal(t, ImageMatchBarcode, res[1].Kind)
	require.Equal(t, coded.ID, res[1].Match.ItemID)

	require.Equal(t, ImageMatchExact, res[2].Kind)
	require.Equal(t, cup.ID, res[2].Match.ItemID)
	require.InDelta(t, 1.0, res[2].Match.Score, 1e-9)

	require.Equal(t, ImageMatchSimilar, res[3].Kind)
	require.Equal(t, pan.ID, res[3].Match.ItemID, "the closest name ranks first")
	alt := map[uuid.UUID]bool{}
	for _, a := range res[3].Alternatives {
		alt[a.ItemID] = true
	}
	require.True(t, alt[jacket.ID], "other similar names are offered as alternatives")

	require.Equal(t, ImageMatchNone, res[4].Kind, "another tenant's item is never suggested")
	require.Nil(t, res[4].Match)
	require.Equal(t, ImageMatchNone, res[5].Kind)

	_, err = svc.MatchItemImages(ctx, tenantID, make([]string, MaxImageMatchNames+1))
	require.Error(t, err)
}

// TestItemImageChangesPublishItemUpdated: adding the first image and deleting the primary both
// move items.image_url and write item.updated in the same transaction, so POS catalogue caches
// and terminals refresh the picture.
func TestItemImageChangesPublishItemUpdated(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_imgevent_")
	ctx := context.Background()
	objects, endpoint := fakeBucket(t)
	store, err := objectstore.New("inventory", objectstore.Config{
		Endpoint: endpoint, AccessKeyID: "AK", SecretAccessKey: "SK",
		PublicBucket: "pub", PrivateBucket: "priv", PublicBaseURL: "https://media.example.com",
	})
	require.NoError(t, err)
	svc := NewService(client, zap.NewNop(), store)

	tenantID := uuid.New()
	_, err = client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop-" + tenantID.String()[:8]).Save(ctx)
	require.NoError(t, err)
	it, err := client.Item.Create().SetTenantID(tenantID).SetSku("A-1").SetName("Pan").SetType(item.TypeGOODS).Save(ctx)
	require.NoError(t, err)

	events := func() []*ent.OutboxEvent {
		return client.OutboxEvent.Query().Where(outboxevent.EventType("item.updated")).AllX(ctx)
	}

	img, err := svc.AddItemImage(ctx, tenantID, it.ID, &objectstore.Upload{Data: []byte("jpeg"), ContentType: "image/jpeg", Ext: ".jpg"}, false)
	require.NoError(t, err)
	require.True(t, img.IsPrimary, "the first image is primary")
	key := client.Item.GetX(ctx, it.ID).ImageURL
	require.NotEmpty(t, key)
	require.Equal(t, "jpeg", string(objects["pub/"+key]))
	require.Len(t, events(), 1)
	require.Contains(t, string(events()[0].Payload), "https://media.example.com/"+key)

	require.NoError(t, svc.DeleteItemImage(ctx, tenantID, it.ID, img.ID))
	require.Empty(t, client.Item.GetX(ctx, it.ID).ImageURL, "no image left, the mirror is cleared")
	require.Len(t, events(), 2, "consumers hear that the picture is gone")
	_, stored := objects["pub/"+key]
	require.False(t, stored, "the unused file is deleted")
}
