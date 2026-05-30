package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entinventorylot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itemasset"
)

// itemDef describes a catalog item for seeding.
type itemDef struct {
	SKU          string
	Name         string
	Description  string
	CategorySlug string
	ItemType     entitem.Type
	UnitName     string
	ImageURL     string
	OnHand       int
	Tags         []string
	CostPrice    *float64
}

func ptr(f float64) *float64 { return &f }

// Media paths relative to the media server root.
const (
	mediaPlaceholder = "/media/images/outlets/menu/placeholder-food.svg"

	imgEspresso     = "/media/images/outlets/menu/espresso.jpg"
	imgCappuccino   = "/media/images/outlets/menu/cappuccino.jpg"
	imgHotCoffee    = "/media/images/outlets/menu/hot coffee.jpeg"
	imgIcedLatte    = "/media/images/outlets/menu/icedlatte.jpeg"
	imgCocktail     = "/media/images/outlets/menu/cocktail.jpeg"
	imgMilkshake    = "/media/images/outlets/menu/milkshake.jpeg"
	imgBurger       = "/media/images/outlets/menu/burger.jpg"
	imgPizza        = "/media/images/outlets/menu/margherita-pizza.jpg"
	imgChicken      = "/media/images/outlets/menu/chicken.jpeg"
	imgChickenUgali = "/media/images/outlets/menu/chicken_ugali.jpeg"
	imgPilau        = "/media/images/outlets/menu/pilau.jpeg"
	imgFish         = "/media/images/outlets/menu/fish.jpeg"
	imgSalad        = "/media/images/outlets/menu/salad.jpg"
	imgBreakfast    = "/media/images/outlets/menu/breakfast.jpg"
	imgOats         = "/media/images/outlets/menu/oats.jpeg"
	imgDessert      = "/media/images/outlets/menu/dessert.jpeg"
	imgLavaCake     = "/media/images/outlets/menu/chocolate-lava-cake.jpg"
	imgMain1        = "/media/images/outlets/menu/main-course-1.jpg"
	imgMain2        = "/media/images/outlets/menu/main-course-2.jpg"
)

// tenantItemGroups maps tenant slugs to the item group functions they should use.
// Add a slug here to scope which items a tenant receives during seeding.
var tenantItemGroups = map[string][]func() []itemDef{
	"codevertex-demo": {hospitalityItems, eventItems, pharmacyItems, retailItems, beautyServiceItems},
}

// itemDefsForSlug returns all item definitions for the given tenant slug.
// Falls back to hospitalityItems + eventItems if the slug is not in the map.
func itemDefsForSlug(slug string) []itemDef {
	groups, ok := tenantItemGroups[slug]
	if !ok {
		groups = []func() []itemDef{hospitalityItems, eventItems}
	}
	var all []itemDef
	for _, fn := range groups {
		all = append(all, fn()...)
	}
	return all
}

func itemUUID(tenantID uuid.UUID, sku string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:item:%s:%s", tenantID, sku)))
}

func itemAssetUUID(itemID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:itemasset:primary:%s", itemID)))
}

func seedItems(ctx context.Context, client *ent.Client, tenantID uuid.UUID, slug string, catIDs map[string]uuid.UUID, unitIDs map[string]uuid.UUID) error {
	defs := itemDefsForSlug(slug)

	seededSKUs := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		seededSKUs[def.SKU] = struct{}{}

		id := itemUUID(tenantID, def.SKU)

		catID, ok := catIDs[def.CategorySlug]
		if !ok {
			return fmt.Errorf("unknown category slug %q for SKU %s", def.CategorySlug, def.SKU)
		}

		unitID, ok := unitIDs[def.UnitName]
		if !ok {
			return fmt.Errorf("unknown unit %q for SKU %s — ensure seedUnits ran first", def.UnitName, def.SKU)
		}

		imgURL := def.ImageURL
		if imgURL == "" {
			imgURL = mediaPlaceholder
		}

		tags := def.Tags
		if tags == nil {
			tags = []string{}
		}

		_, err := client.Item.Get(ctx, id)
		switch {
		case ent.IsNotFound(err):
			create := client.Item.Create().
				SetID(id).
				SetTenantID(tenantID).
				SetSku(def.SKU).
				SetName(def.Name).
				SetDescription(def.Description).
				SetCategoryID(catID).
				SetUnitID(unitID).
				SetType(def.ItemType).
				SetImageURL(imgURL).
				SetTags(tags).
				SetIsActive(true).
				SetNillableCostPrice(def.CostPrice)
			if _, createErr := create.Save(ctx); createErr != nil {
				return fmt.Errorf("create item %s: %w", def.SKU, createErr)
			}
			log.Printf("item created: %s — %s", def.SKU, def.Name)
		case err != nil:
			return fmt.Errorf("check item %s: %w", def.SKU, err)
		default:
			update := client.Item.UpdateOneID(id).
				SetName(def.Name).
				SetDescription(def.Description).
				SetCategoryID(catID).
				SetUnitID(unitID).
				SetType(def.ItemType).
				SetImageURL(imgURL).
				SetTags(tags)
			if def.CostPrice != nil {
				update = update.SetCostPrice(*def.CostPrice)
			}
			if _, updateErr := update.Save(ctx); updateErr != nil {
				return fmt.Errorf("update item %s: %w", def.SKU, updateErr)
			}
		}

		if err := upsertPrimaryAsset(ctx, client, id, imgURL); err != nil {
			return fmt.Errorf("upsert asset for %s: %w", def.SKU, err)
		}
	}

	cleanupStaleItems(ctx, client, tenantID, seededSKUs)
	return nil
}

// cleanupStaleItems removes items (and their dependent balance/lot records) for this
// tenant whose SKU is not in the seeded set. This keeps demo data clean after
// tenant use-case groups change.
func cleanupStaleItems(ctx context.Context, client *ent.Client, tenantID uuid.UUID, seededSKUs map[string]struct{}) {
	existing, err := client.Item.Query().
		Where(entitem.TenantID(tenantID)).
		All(ctx)
	if err != nil {
		log.Printf("[WARN] cleanup: query items: %v", err)
		return
	}

	var staleIDs []uuid.UUID
	for _, it := range existing {
		if _, ok := seededSKUs[it.Sku]; !ok {
			staleIDs = append(staleIDs, it.ID)
		}
	}
	if len(staleIDs) == 0 {
		return
	}

	if _, err := client.InventoryBalance.Delete().
		Where(entinvbal.ItemIDIn(staleIDs...)).
		Exec(ctx); err != nil {
		log.Printf("[WARN] cleanup: delete balances: %v", err)
	}
	if _, err := client.InventoryLot.Delete().
		Where(entinventorylot.ItemIDIn(staleIDs...)).
		Exec(ctx); err != nil {
		log.Printf("[WARN] cleanup: delete lots: %v", err)
	}
	if deleted, err := client.Item.Delete().
		Where(entitem.IDIn(staleIDs...)).
		Exec(ctx); err != nil {
		log.Printf("[WARN] cleanup: delete items: %v", err)
	} else if deleted > 0 {
		log.Printf("cleanup: removed %d stale items for tenant %s", deleted, tenantID)
	}
}

func upsertPrimaryAsset(ctx context.Context, client *ent.Client, itemID uuid.UUID, imgURL string) error {
	assetID := itemAssetUUID(itemID)

	existing, err := client.ItemAsset.Query().
		Where(itemasset.ItemID(itemID), itemasset.IsPrimary(true)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query primary asset: %w", err)
	}

	if existing != nil {
		if existing.URL != imgURL {
			if _, err := client.ItemAsset.UpdateOneID(existing.ID).SetURL(imgURL).Save(ctx); err != nil {
				return fmt.Errorf("update asset URL: %w", err)
			}
		}
		return nil
	}

	if _, err := client.ItemAsset.Create().
		SetID(assetID).
		SetItemID(itemID).
		SetAssetType("IMAGE").
		SetURL(imgURL).
		SetIsPrimary(true).
		SetDisplayOrder(0).
		SetMimeType(mimeFromURL(imgURL)).
		Save(ctx); err != nil {
		return fmt.Errorf("create primary asset: %w", err)
	}
	return nil
}

func mimeFromURL(url string) string {
	switch {
	case len(url) >= 4 && url[len(url)-4:] == ".jpg":
		return "image/jpeg"
	case len(url) >= 5 && url[len(url)-5:] == ".jpeg":
		return "image/jpeg"
	case len(url) >= 4 && url[len(url)-4:] == ".png":
		return "image/png"
	case len(url) >= 4 && url[len(url)-4:] == ".svg":
		return "image/svg+xml"
	case len(url) >= 5 && url[len(url)-5:] == ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
