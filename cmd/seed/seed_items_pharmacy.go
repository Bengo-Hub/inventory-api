package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// pharmacyItems returns OTC pharmacy GOODS items.
func pharmacyItems() []itemDef {
	return []itemDef{
		{"PHM-ANL-001", "Paracetamol 500mg", "Pain reliever and fever reducer (pack of 24 tablets)", "pharmacy", entitem.TypeGOODS, "PACK", mediaPlaceholder, 200, []string{"otc", "pain-relief"}, ptr(80.0)},
		{"PHM-ANL-002", "Ibuprofen 400mg", "Anti-inflammatory and pain reliever (pack of 16 tablets)", "pharmacy", entitem.TypeGOODS, "PACK", mediaPlaceholder, 150, []string{"otc", "pain-relief"}, ptr(120.0)},
		{"PHM-ANT-001", "Antacid Tablets", "Rapid relief from heartburn and indigestion (pack of 20)", "pharmacy", entitem.TypeGOODS, "PACK", mediaPlaceholder, 100, []string{"otc", "digestive"}, ptr(90.0)},
		{"PHM-VIT-001", "Vitamin C 1000mg", "Immune support supplement (bottle of 60 tablets)", "pharmacy", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 80, []string{"otc", "vitamins"}, ptr(350.0)},
		{"PHM-VIT-002", "Multivitamin Daily", "Comprehensive daily multivitamin (bottle of 30 tablets)", "pharmacy", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 60, []string{"otc", "vitamins"}, ptr(480.0)},
		{"PHM-ANT-002", "Antihistamine 10mg", "Allergy relief antihistamine tablets (pack of 10)", "pharmacy", entitem.TypeGOODS, "PACK", mediaPlaceholder, 120, []string{"otc", "allergy"}, ptr(95.0)},
		{"PHM-CLD-001", "Cold & Flu Relief", "Multi-symptom cold and flu formula (pack of 12 capsules)", "pharmacy", entitem.TypeGOODS, "PACK", mediaPlaceholder, 90, []string{"otc", "cold-flu"}, ptr(180.0)},
		{"PHM-SAL-001", "Saline Nasal Spray", "Isotonic nasal spray for congestion relief (100ml)", "pharmacy", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 75, []string{"otc", "nasal"}, ptr(200.0)},
		{"PHM-SKN-001", "Hydrocortisone Cream 1%", "Mild steroid cream for skin irritation and itch (30g tube)", "pharmacy", entitem.TypeGOODS, "PIECE", mediaPlaceholder, 50, []string{"otc", "skin"}, ptr(250.0)},
		{"PHM-EYE-001", "Eye Drops Lubricant", "Artificial tears for dry eye relief (10ml)", "pharmacy", entitem.TypeGOODS, "BOTTLE", mediaPlaceholder, 60, []string{"otc", "eye"}, ptr(300.0)},
	}
}
