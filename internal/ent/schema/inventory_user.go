package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// InventoryUser holds the schema definition for inventory service users.
type InventoryUser struct {
	ent.Schema
}

// Fields of the InventoryUser.
func (InventoryUser) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Tenant identifier"),
		field.UUID("auth_service_user_id", uuid.UUID{}).
			Unique().
			Comment("Reference to auth-service user (no duplication)"),
		field.String("email").
			NotEmpty().
			Comment("Denormalized email for convenience"),
		field.String("name").
			Optional().
			Comment("Denormalized full name (from the auth user profile) for display, e.g. document signatures"),
		field.String("status").
			Default("active").
			Comment("Status: active, inactive, suspended"),
		field.String("sync_status").
			Default("synced").
			Comment("Sync status: synced, pending, failed"),
		field.Time("last_sync_at").
			Optional(),
		// ── Terminal/PIN login (desk/kiosk) ──────────────────────────────────
		// Populated from auth.user.pin_set events; used by /inventory/auth/pin/* to
		// authenticate staff at a warehouse terminal without SSO.
		field.String("pin_hash").
			Optional().
			Nillable().
			Sensitive().
			Comment("bcrypt hash of the 4-digit service PIN"),
		field.String("pin_fast_hash").
			Optional().
			Comment("hex(SHA256(tenant:user:pin)) for O(1) PIN lookup"),
		field.Int("pin_failed_attempts").
			Default(0).
			Comment("consecutive wrong-PIN attempts (lockout)"),
		field.Time("pin_locked_until").
			Optional().
			Nillable().
			Comment("PIN login locked until this time after too many failures"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Indexes of the InventoryUser.
func (InventoryUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("auth_service_user_id").Unique(),
		index.Fields("tenant_id", "auth_service_user_id").Unique(),
		index.Fields("status"),
		index.Fields("sync_status"),
	}
}
