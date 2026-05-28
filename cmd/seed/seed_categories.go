package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
)

type categoryDef struct {
	Slug        string
	Name        string
	Code        string
	Description string
	Icon        string
}

var categoryDefs = []categoryDef{
	{"hot-beverages", "Hot Beverages", "BEV", "Espresso drinks, teas, and other hot beverages", "/media/icons/coffee-colored.svg"},
	{"cold-beverages", "Cold Beverages", "CBV", "Iced coffees, frappes, smoothies, and fresh juices", "/media/icons/juice-colored.svg"},
	{"pastries", "Pastries & Bakery", "PST", "Croissants, muffins, cakes, and baked goods", "/media/icons/cake-colored.svg"},
	{"sandwiches", "Sandwiches & Wraps", "SND", "Paninis, wraps, and classic sandwiches", "/media/icons/sandwich-colored.svg"},
	{"salads", "Salads", "SAL", "Fresh salads and greens", "/media/icons/fresh-colored.svg"},
	{"main-courses", "Main Courses", "MIN", "Grills, curries, rice dishes, and hearty mains", "/media/icons/burger-colored.svg"},
	{"light-bites", "Light Bites", "BTE", "Samosas, spring rolls, and quick snacks", "/media/icons/snack-colored.svg"},
	{"breakfast", "Breakfast", "BRK", "Full breakfasts, pancakes, oats, and morning meals", "/media/icons/breakfast-colored.svg"},
	{"pizza", "Pizza", "PIZ", "Artisanal and classic pizzas", "/media/icons/pizza-colored.svg"},
	{"chicken", "Chicken", "CHK", "Fried and grilled chicken specialties", "/media/icons/drumstick-colored.svg"},
	{"sushi", "Sushi", "SHI", "Fresh sushi and Japanese delicacies", "/media/icons/sushi-colored.svg"},
	{"grocery", "Grocery", "GRC", "Fresh produce and household essentials", "/media/icons/grocery-colored.svg"},
	{"pharmacy", "Pharmacy", "PHR", "Medication and health services", "/media/icons/medicine-colored.svg"},
	{"gifts", "Gifts", "GFT", "Special gifts and hampers", "/media/icons/gift-colored.svg"},
	{"flowers", "Flowers", "FLW", "Fresh flower bouquets and arrangements", "/media/icons/flower-colored.svg"},
	{"alcohol", "Alcohol", "ALC", "Wines, spirits, and beers", "/media/icons/liquor-colored.svg"},
	{"chinese", "Chinese", "CHN", "Authentic Chinese cuisine", "/media/icons/chinese-colored.svg"},
	{"indian", "Indian", "IND", "Flavorful Indian curries and specialities", "/media/icons/curry-colored.svg"},
	{"desserts", "Desserts", "DST", "Sweet treats and delights", "/media/icons/dessert-colored.svg"},
	{"retail", "Retail", "RTL", "Shopping and fashion goods", "/media/icons/retail-colored.svg"},
	{"electronics", "Electronics", "ELC", "Devices and accessories", "/media/icons/electronics-colored.svg"},
	{"fashion", "Fashion", "FSH", "Clothing and apparel", "/media/icons/fashion-colored.svg"},
	{"fresh", "Fresh", "FRS", "Fresh fruits, vegetables, and produce", "/media/icons/fresh-colored.svg"},
	{"juice", "Juice", "JCE", "Fresh juices and smoothies", "/media/icons/juice-colored.svg"},
	{"events", "Events & Experiences", "EVT", "Live events, workshops, and curated dining experiences", "/media/icons/events-colored.svg"},
	{"beauty-services", "Beauty & Spa", "BEA", "Hair, nail, skin, and wellness services", "/media/icons/beauty-colored.svg"},
}

func categoryUUID(tenantID uuid.UUID, slug string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:category:%s:%s", tenantID, slug)))
}

func seedItemCategories(ctx context.Context, client *ent.Client, tenantID uuid.UUID) (map[string]uuid.UUID, error) {
	catIDs := make(map[string]uuid.UUID, len(categoryDefs))
	for _, cat := range categoryDefs {
		id := categoryUUID(tenantID, cat.Slug)
		catIDs[cat.Slug] = id

		_, err := client.ItemCategory.Get(ctx, id)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.ItemCategory.Create().
				SetID(id).
				SetTenantID(tenantID).
				SetName(cat.Name).
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetIcon(cat.Icon).
				SetDescription(cat.Description).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return nil, fmt.Errorf("create category %s: %w", cat.Slug, createErr)
			}
			log.Printf("category created: %s", cat.Name)
		case err != nil:
			return nil, fmt.Errorf("check category %s: %w", cat.Slug, err)
		default:
			if _, updateErr := client.ItemCategory.UpdateOneID(id).
				SetName(cat.Name).
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetIcon(cat.Icon).
				SetDescription(cat.Description).
				Save(ctx); updateErr != nil {
				return nil, fmt.Errorf("update category %s: %w", cat.Slug, updateErr)
			}
		}
	}
	return catIDs, nil
}
