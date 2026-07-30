package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitemcategory "github.com/bengobox/inventory-service/internal/ent/itemcategory"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

// globalCategoryDef is a platform-wide default item category, visible to every tenant
// (is_global=true). The Kind ("goods" | "service") drives the treasury-ui document item
// form's category filtering: SERVICE items surface service categories, GOODS items surface
// goods categories. Since ItemCategory has no dedicated "kind" column, the split is a
// migration-free CONVENTION keyed on the seeded slug — mirrored by treasury-ui's
// categoryKind() classifier (lib/api/inventory). New tenant-created categories default to
// "both" (shown for either item type).
type globalCategoryDef struct {
	Slug        string
	Name        string
	Code        string
	Description string
	Kind        string
}

// globalCategoryDefs are seeded once, platform-wide, under the nil "global" tenant.
var globalCategoryDefs = []globalCategoryDef{
	// Goods
	{"general-goods", "General Goods", "GEN", "Uncategorised physical products", "goods"},
	{"electronics-goods", "Electronics", "ELG", "Devices, components, and accessories", "goods"},
	{"furniture-fittings", "Furniture & Fittings", "FRN", "Furniture, fixtures, and fittings", "goods"},
	{"stationery-office", "Stationery & Office", "STN", "Office and stationery supplies", "goods"},
	{"hardware-tools", "Hardware & Tools", "HWT", "Hardware, tools, and building materials", "goods"},
	{"spare-parts", "Spare Parts", "SPR", "Replacement parts and components", "goods"},
	{"raw-materials", "Raw Materials", "RAW", "Inputs and raw materials", "goods"},

	// Services
	{"consultancy", "Consultancy", "CON", "Advisory and consultancy engagements", "service"},
	{"professional-services", "Professional Services", "PRO", "Billable professional service work", "service"},
	{"feasibility-study", "Feasibility & Studies", "FEA", "Technical feasibility, assessment, and study work", "service"},
	{"design-engineering", "Design & Engineering", "DSN", "Design, engineering, and technical design services", "service"},
	{"installation-setup", "Installation & Setup", "INS", "Installation, commissioning, and setup", "service"},
	{"maintenance-support", "Maintenance & Support", "MNT", "Maintenance, support, and servicing", "service"},
	{"training-capacity", "Training & Capacity", "TRN", "Training, workshops, and capacity building", "service"},
	{"software-development", "Software Development", "SFT", "Software development and implementation", "service"},
	{"supervision-management", "Supervision & Management", "SUP", "Project supervision and management services", "service"},
	{"subscription-service", "Subscriptions", "SUB", "Recurring subscription and retainer services", "service"},
}

func globalCategoryUUID(slug string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:global-category:"+slug))
}

// seedGlobalCategories idempotently creates the platform-wide default goods/service
// categories (is_global=true, nil tenant) so every tenant's document item form has a
// sensible default set to pick from, split by item type. Reconciles by deterministic ID.
func seedGlobalCategories(ctx context.Context, client *ent.Client) error {
	// Global categories are owned by the reserved nil "platform-global" tenant (is_global=true,
	// visible to every tenant). item_categories.tenant_id is FK-constrained to tenants(id), so
	// that sentinel row must exist first — otherwise every global insert fails the FK, which is
	// exactly why globals were silently absent in environments where it was never provisioned.
	if err := ensureGlobalTenant(ctx, client); err != nil {
		return err
	}
	for _, cat := range globalCategoryDefs {
		id := globalCategoryUUID(cat.Slug)
		icon := items.InferDefaultCategoryIcon(cat.Name, "")
		existing, err := client.ItemCategory.Get(ctx, id)

		// A global category with this NAME may already exist under a different ID — the
		// bulk-import and older seed paths created globals with random UUIDs. Reconciling
		// on the deterministic ID alone would then try to INSERT a second row with the same
		// name and trip the unique (tenant_id, name) index, which aborted the whole seed
		// before any per-tenant data was written. Adopt the existing row instead.
		if ent.IsNotFound(err) {
			byName, nameErr := client.ItemCategory.Query().
				Where(
					entitemcategory.TenantID(uuid.Nil),
					entitemcategory.NameEQ(cat.Name),
				).
				Only(ctx)
			if nameErr == nil {
				id = byName.ID
				existing, err = byName, nil
				log.Printf("global category adopted by name: %s (existing id %s)", cat.Name, byName.ID)
			} else if !ent.IsNotFound(nameErr) {
				return fmt.Errorf("check global category by name %s: %w", cat.Name, nameErr)
			}
		}

		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.ItemCategory.Create().
				SetID(id).
				SetTenantID(uuid.Nil).
				SetName(cat.Name).
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetDescription(cat.Description).
				SetIcon(icon).
				SetIsGlobal(true).
				SetIsActive(true).
				Save(ctx); createErr != nil {
				return fmt.Errorf("create global category %s: %w", cat.Slug, createErr)
			}
			log.Printf("global category created: %s (%s)", cat.Name, cat.Kind)
		case err != nil:
			return fmt.Errorf("check global category %s: %w", cat.Slug, err)
		default:
			upd := client.ItemCategory.UpdateOneID(id).
				SetName(cat.Name).
				SetSlug(cat.Slug).
				SetCode(cat.Code).
				SetDescription(cat.Description).
				SetIsGlobal(true).
				SetIsActive(true)
			// Never clobber an icon that's already set (e.g. a manual override).
			if existing.Icon == "" {
				upd = upd.SetIcon(icon)
			}
			if _, updateErr := upd.Save(ctx); updateErr != nil {
				return fmt.Errorf("update global category %s: %w", cat.Slug, updateErr)
			}
		}
	}
	return nil
}

// ensureGlobalTenant idempotently provisions the reserved nil tenant that owns platform-global
// rows (currently is_global categories). It mirrors how units/tax codes model shared platform
// data, and satisfies the item_categories → tenants FK so global categories can be seeded.
func ensureGlobalTenant(ctx context.Context, client *ent.Client) error {
	if _, err := client.Tenant.Get(ctx, uuid.Nil); err == nil {
		return nil // already provisioned
	} else if !ent.IsNotFound(err) {
		return fmt.Errorf("check platform-global tenant: %w", err)
	}
	if _, err := client.Tenant.Create().
		SetID(uuid.Nil).
		SetName("Platform Global").
		SetSlug("__platform_global__").
		Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return fmt.Errorf("create platform-global tenant: %w", err)
	}
	log.Printf("platform-global tenant ensured (nil sentinel)")
	return nil
}
