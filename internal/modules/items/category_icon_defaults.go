package items

import "strings"

// categoryIconKeywordGroup matches any of Keywords (case-insensitive substring match
// against the category name) to Icon, a path under /media/icons/ following the same
// convention as cmd/seed/seed_categories.go's categoryDefs table.
type categoryIconKeywordGroup struct {
	Icon     string
	Keywords []string
}

// categoryIconKeywords is checked in order — first match wins — so more specific
// keywords (e.g. "smart phone") should be listed ahead of broader ones where two
// groups could otherwise both match the same name.
//
// Mirrors the shape of ordering-backend's categoryAllowedForUseCase /
// isStorefrontSellableCategory keyword classifiers (internal/modules/catalog/proxy_service.go
// in that sibling repo), applied here to icon inference instead of use-case filtering.
var categoryIconKeywords = []categoryIconKeywordGroup{
	{"/media/icons/padlock-colored.svg", []string{"padlock", "lock"}},
	{"/media/icons/light-bulb-colored.svg", []string{"bulb", "light"}},
	{"/media/icons/extension-cord-colored.svg", []string{"extension", "cord", "cable"}},
	{"/media/icons/gas-cooker-colored.svg", []string{"gas cooker", "cooker", "stove", "gas"}},
	// "flash" is ambiguous (camera flash vs. flash drive vs. a brand name) — only treat it
	// as a memory-card signal when paired with a storage-ish word, otherwise let it fall
	// through to the generic/use-case fallback rather than mis-tagging it.
	{"/media/icons/memory-card-colored.svg", []string{"memory card", "memory", "sd card", "flash drive", "flash disk"}},
	{"/media/icons/usb-drive-colored.svg", []string{"usb"}},
	{"/media/icons/radio-colored.svg", []string{"radio"}},
	{"/media/icons/camera-colored.svg", []string{"camera"}},
	{"/media/icons/smartphone-colored.svg", []string{"smart phone", "smartphone", "phone"}},
	{"/media/icons/accessories-colored.svg", []string{"accessor"}},

	// Existing generic icon set (reuse — don't duplicate assets already on disk).
	{"/media/icons/coffee-colored.svg", []string{"coffee", "espresso", "hot beverage"}},
	{"/media/icons/juice-colored.svg", []string{"juice", "smoothie", "cold beverage"}},
	{"/media/icons/cake-colored.svg", []string{"cake", "pastry", "bakery"}},
	{"/media/icons/sandwich-colored.svg", []string{"sandwich", "wrap", "panini"}},
	{"/media/icons/fresh-colored.svg", []string{"salad", "fresh", "produce", "vegetable", "fruit"}},
	{"/media/icons/burger-colored.svg", []string{"burger", "main course", "grill", "curry"}},
	{"/media/icons/snack-colored.svg", []string{"snack", "light bite", "samosa"}},
	{"/media/icons/breakfast-colored.svg", []string{"breakfast", "pancake"}},
	{"/media/icons/pizza-colored.svg", []string{"pizza"}},
	{"/media/icons/drumstick-colored.svg", []string{"chicken", "drumstick"}},
	{"/media/icons/sushi-colored.svg", []string{"sushi", "japanese"}},
	{"/media/icons/grocery-colored.svg", []string{"grocery", "chemical", "detergent", "cleaning"}},
	{"/media/icons/medicine-colored.svg", []string{"pharmacy", "medicine", "drug", "medication"}},
	{"/media/icons/gift-colored.svg", []string{"gift", "hamper"}},
	{"/media/icons/flower-colored.svg", []string{"flower", "bouquet"}},
	{"/media/icons/liquor-colored.svg", []string{"alcohol", "liquor", "wine", "spirit", "beer"}},
	{"/media/icons/chinese-colored.svg", []string{"chinese"}},
	{"/media/icons/curry-colored.svg", []string{"indian"}},
	{"/media/icons/dessert-colored.svg", []string{"dessert", "sweet"}},
	{"/media/icons/retail-colored.svg", []string{"retail", "fashion goods", "shopping"}},
	{"/media/icons/fashion-colored.svg", []string{"fashion", "clothing", "apparel"}},
	{"/media/icons/electronics-colored.svg", []string{"electronic", "device", "gadget"}},
	// Deliberately no bare "spa" keyword here: it's a literal substring of unrelated
	// words like "Spare" (e.g. "Spare Parts" was mis-tagged with this icon before this
	// fix), and "beauty" alone already catches the seeded "Beauty & Spa" category.
	{"/media/icons/beauty-colored.svg", []string{"beauty", "salon", "wellness", "day spa", "med spa"}},
}

// categoryIconUseCaseFallback maps an outlet use_case to a sensible generic icon when
// no keyword in the category's own name matched anything above.
var categoryIconUseCaseFallback = map[string]string{
	"retail":        "/media/icons/retail-colored.svg",
	"hospitality":   "/media/icons/coffee-colored.svg",
	"restaurant":    "/media/icons/burger-colored.svg",
	"pharmacy":      "/media/icons/medicine-colored.svg",
	"electronics":   "/media/icons/electronics-colored.svg",
	"services":      "/media/icons/accessories-colored.svg",
	"warehouse":     "/media/icons/category-colored.svg",
	"manufacturing": "/media/icons/category-colored.svg",
	"beauty":        "/media/icons/beauty-colored.svg",
	"detergent":     "/media/icons/grocery-colored.svg",
	"events":        "/media/icons/events-colored.svg",
}

// categoryIconGenericFallback is the final catch-all when neither the category name
// nor the use_case yields a match.
const categoryIconGenericFallback = "/media/icons/category-colored.svg"

// InferDefaultCategoryIcon returns a sensible default /media/icons/*.svg path for a
// category that has no icon of its own, based on keyword matches against its name,
// falling back to a use_case-keyed generic icon, and finally to a single catch-all
// generic icon. It never returns an empty string.
//
// Callers must only invoke this when the category's existing icon is empty — this
// function does not know about (and must never be used to clobber) a user-supplied
// icon.
func InferDefaultCategoryIcon(name string, useCase string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized != "" {
		for _, group := range categoryIconKeywords {
			for _, kw := range group.Keywords {
				if strings.Contains(normalized, kw) {
					return group.Icon
				}
			}
		}
	}

	if uc := strings.ToLower(strings.TrimSpace(useCase)); uc != "" {
		if icon, ok := categoryIconUseCaseFallback[uc]; ok {
			return icon
		}
	}

	return categoryIconGenericFallback
}
