package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// beautyServiceItems returns SERVICE-type beauty and spa service items.
func beautyServiceItems() []itemDef {
	return []itemDef{
		{"SVC-HAR-001", "Ladies Haircut & Style", "Wash, cut, and blow-dry (60 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"hair", "ladies"}, nil},
		{"SVC-HAR-002", "Gents Haircut", "Classic or fade haircut with finish (45 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"hair", "gents"}, nil},
		{"SVC-HAR-003", "Hair Colour & Highlights", "Full colour or highlight treatment (90 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"hair", "colour"}, nil},
		{"SVC-MAS-001", "Swedish Massage (60 min)", "Full-body relaxation massage with aromatherapy oil", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"massage", "wellness"}, nil},
		{"SVC-MAS-002", "Deep Tissue Massage (45 min)", "Targeted deep muscle relief massage", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"massage", "therapy"}, nil},
		{"SVC-FCI-001", "Classic Facial (60 min)", "Deep cleansing, exfoliation, and hydration facial", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"facial", "skin"}, nil},
		{"SVC-MNI-001", "Manicure", "Classic nail care, shaping, and polish (30 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"nails", "beauty"}, nil},
		{"SVC-PED-001", "Pedicure", "Foot soak, exfoliation, nail care, and polish (45 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"nails", "beauty"}, nil},
		{"SVC-EYB-001", "Eyebrow Threading", "Precise brow shaping by threading (15 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"brows", "beauty"}, nil},
		{"SVC-WAX-001", "Full Leg Wax", "Smooth full leg waxing treatment (45 min)", "beauty-services", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"waxing", "beauty"}, nil},
	}
}
