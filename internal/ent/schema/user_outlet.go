package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// UserOutlet links an InventoryUser to the outlets they are assigned to.
// Mirrors POS StaffOutlet: a user may be assigned to multiple outlets, and
// non-HQ users may only operate within an outlet they are assigned to.
//
// Outlets are owned by auth-service (inventory mirrors them), so outlet_id is a
// bare UUID rather than an ent edge — consistent with how outlet_id is stored
// elsewhere in this service.
type UserOutlet struct {
	ent.Schema
}

// Fields of the UserOutlet.
func (UserOutlet) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("user_id", uuid.UUID{}).
			Comment("FK to InventoryUser (auth_service_user_id space)"),
		field.UUID("outlet_id", uuid.UUID{}).
			Comment("Outlet id owned by auth-service"),
		field.Bool("is_home_outlet").Default(false).
			Comment("True for the user's primary outlet"),
		field.UUID("assigned_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("assigned_at").Default(time.Now).Immutable(),
	}
}

// Indexes of the UserOutlet.
func (UserOutlet) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "user_id", "outlet_id").Unique(),
		index.Fields("tenant_id", "outlet_id"),
		index.Fields("tenant_id", "user_id"),
	}
}
