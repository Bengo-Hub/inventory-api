package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// eventItems returns SERVICE-type event and experience catalog items.
func eventItems() []itemDef {
	return []itemDef{
		{"EVT-JAZ-001", "Weekend Jazz Night", "Live jazz performance with curated dinner menu", "events", entitem.TypeSERVICE, "TICKET", imgCocktail, 0, []string{"event"}, nil},
		{"EVT-BAR-001", "Barista Masterclass", "Learn espresso extraction, latte art, and coffee tasting", "events", entitem.TypeSERVICE, "TICKET", imgEspresso, 0, []string{"event"}, nil},
		{"EVT-WIN-001", "Wine & Cheese Evening", "Sommelier-guided wine pairing with artisan cheeses", "events", entitem.TypeSERVICE, "TICKET", imgCocktail, 0, []string{"event"}, nil},
		{"EVT-BRN-001", "Sunday Brunch Buffet", "Unlimited brunch spread with live cooking stations", "events", entitem.TypeSERVICE, "TICKET", imgBreakfast, 0, []string{"event"}, nil},
		{"EVT-MIX-001", "Cocktail Mixology Workshop", "Craft Urban Loft signature cocktails with our expert bartender", "events", entitem.TypeSERVICE, "TICKET", imgCocktail, 0, []string{"event"}, nil},
		{"EVT-QUZ-001", "Urban Loft Quiz Night", "Monthly trivia night with prizes and two-course dinner", "events", entitem.TypeSERVICE, "TICKET", imgMain1, 0, []string{"event"}, nil},
	}
}
