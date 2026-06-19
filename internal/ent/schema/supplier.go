package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Supplier holds the schema for vendor/supplier management.
// Enables purchase order workflows for all industry types.
type Supplier struct {
	ent.Schema
}

// Fields of the Supplier.
func (Supplier) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("name").
			NotEmpty(),
		field.String("code").
			NotEmpty().
			Comment("Short supplier code for reference"),
		field.String("contact_name").
			Optional(),
		field.String("contact_email").
			Optional(),
		field.String("contact_phone").
			Optional(),
		field.Text("address").
			Optional().
			Comment("Legacy single-line address; structured parts below are preferred"),
		// Structured address (preferred over the legacy flat `address` text).
		field.String("address_line1").Optional(),
		field.String("address_line2").Optional(),
		field.String("city").Optional(),
		field.String("address_state").Optional().Comment("State / county / province"),
		field.String("address_postal_code").Optional(),
		field.String("country").Optional().Comment("ISO country name or code"),
		// Business profile (vendor onboarding).
		field.String("industry").Optional(),
		field.String("website").Optional(),
		field.Text("notes").Optional(),
		field.String("logo_url").Optional(),
		field.String("payment_terms").
			Optional().
			Comment("Net30, Net60, COD, etc."),
		field.Bool("is_active").
			Default(true),
		// Payment details for automated disbursements via treasury-api
		field.Enum("payment_method_type").
			Values("mpesa", "mpesa_b2b", "bank_transfer", "cash", "cheque").
			Optional(),
		field.String("mpesa_phone").
			Optional().
			Comment("M-Pesa phone (254...) for B2C supplier payment"),
		field.String("mpesa_business_name").
			Optional().
			Comment("M-Pesa business name for B2B paybill payments"),
		field.String("bank_account_number").
			Optional(),
		field.String("bank_account_name").
			Optional().
			Comment("Name on the bank account"),
		field.String("bank_name").
			Optional(),
		field.String("bank_branch").
			Optional(),
		field.String("swift_bic").
			Optional().
			Comment("SWIFT / BIC code for international transfers"),
		field.String("currency").
			Optional().
			Comment("Default transaction currency, e.g. KES"),
		field.String("tax_pin").
			Optional().
			Comment("KRA PIN for WHT calculation on supplier payments"),
		field.String("vat_number").
			Optional().
			Comment("VAT registration number (if distinct from KRA PIN)"),
		field.Bool("requires_invoice_before_payment").
			Default(false),
		field.Bool("auto_pay_enabled").
			Default(false).
			Comment("When true, treasury auto-disburses payment to supplier on PO receipt confirmation"),
		field.Int("payment_terms_days").
			Default(0).
			Comment("Net payment terms in days (0 = immediate, 30 = Net30, etc.)"),
		field.Float("credit_limit").
			Optional().
			Comment("Maximum outstanding balance allowed for this supplier"),
		field.String("paystack_recipient_code").
			Optional().
			Comment("Cached Paystack RCP_xxx to avoid re-creating recipient on each payout"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Supplier.
func (Supplier) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("purchase_orders", PurchaseOrder.Type),
	}
}

// Indexes of the Supplier.
func (Supplier) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "code").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
