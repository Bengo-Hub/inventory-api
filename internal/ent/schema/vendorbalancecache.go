package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// VendorBalanceCache is a READ-ONLY, best-effort mirror of a supplier's treasury AP balance,
// kept fresh by the durable treasury.vendor.balance_updated event consumer (see
// consumers/treasury_vendor_balance_events.go). Treasury owns AP balances (supplier master —
// Supplier — is owned here, per treasury-vendor-master-via-inventory); this cache exists purely
// to close the one-way sync gap where a bill payment recorded directly in treasury-ui never
// reached inventory-api at all — procurement/supplier surfaces here previously had no way to
// know a supplier had just been paid.
type VendorBalanceCache struct{ ent.Schema }

func (VendorBalanceCache) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("vendor_id", uuid.UUID{}).Optional().Nillable().Comment("inventory Supplier ID"),
		field.String("vendor_identifier").Optional(),
		field.String("vendor_name").Optional(),
		field.String("balance_owed").Default("0"),
		field.String("outstanding_payable").Default("0"),
		field.String("currency").Default("KES"),
		field.Time("synced_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (VendorBalanceCache) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "vendor_id").Unique(),
		index.Fields("tenant_id", "vendor_identifier"),
	}
}
