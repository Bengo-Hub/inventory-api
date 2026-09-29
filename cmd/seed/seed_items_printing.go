package main

import entitem "github.com/bengobox/inventory-service/internal/ent/item"

// printingServiceItems returns the demo print shop's catalog services (SERVICE items tagged
// PRINTING_SERVICE) and the paper/blank stock it also sells (GOODS). A POS services outlet on the
// printing_branding profile shows exactly these.
func printingServiceItems() []itemDef {
	return []itemDef{
		{"PRT-BC-100", "Business Cards (100 pcs)", "Full colour, 350gsm, single or double sided", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"business cards", "print"}, nil},
		{"PRT-BC-500", "Business Cards (500 pcs)", "Full colour, 350gsm, single or double sided", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"business cards", "print"}, nil},
		{"PRT-FLY-A5", "A5 Flyers (per 100)", "Full colour on 135gsm art paper", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"flyers", "print"}, nil},
		{"PRT-POS-A3", "A3 Poster", "Full colour poster on 170gsm gloss", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"poster", "print"}, nil},
		{"PRT-BAN-SQM", "Banner Printing (per sq m)", "PVC flex banner with eyelets", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"banner", "large format"}, nil},
		{"PRT-RUP-001", "Roll-up Banner (85 x 200 cm)", "Printed banner with roll-up stand and carry bag", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"roll-up", "large format"}, nil},
		{"PRT-STK-SQM", "Sticker / Vinyl Printing (per sq m)", "Printed and cut vinyl stickers", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"stickers", "vinyl"}, nil},
		{"PRT-TSH-001", "T-shirt Branding (per piece)", "Screen or heat-press print, customer or house t-shirt", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"branding", "apparel"}, nil},
		{"PRT-MUG-001", "Mug Branding", "Sublimation print on a white ceramic mug", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"branding", "mugs"}, nil},
		{"PRT-DES-001", "Graphic Design (per design)", "Design from scratch or artwork correction, up to 2 revisions", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"design"}, nil},
		{"PRT-PHC-BW", "Photocopy B/W (per page)", "A4 black and white photocopy", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"photocopy"}, nil},
		{"PRT-LAM-A4", "Lamination A4", "Gloss or matte lamination", "printing-branding", entitem.TypeSERVICE, "PIECE", mediaPlaceholder, 0, []string{"lamination", "finishing"}, nil},
	}
}

// printingServicePrices is the KES retail price of each seeded printing service. Services carry
// no cost, so the goods cost-markup rule cannot price them; seedItemPricing applies these.
var printingServicePrices = map[string]float64{
	"PRT-BC-100":  1500,
	"PRT-BC-500":  4500,
	"PRT-FLY-A5":  1800,
	"PRT-POS-A3":  250,
	"PRT-BAN-SQM": 650,
	"PRT-RUP-001": 8500,
	"PRT-STK-SQM": 1200,
	"PRT-TSH-001": 450,
	"PRT-MUG-001": 650,
	"PRT-DES-001": 2000,
	"PRT-PHC-BW":  5,
	"PRT-LAM-A4":  80,
}
