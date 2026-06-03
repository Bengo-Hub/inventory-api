package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ContractOrderLink links a Contract to a PurchaseOrder (ERP ContractOrderLink).
type ContractOrderLink struct{ ent.Schema }

func (ContractOrderLink) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("contract_id", uuid.UUID{}),
		field.UUID("purchase_order_id", uuid.UUID{}).Comment("FK to PurchaseOrder"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ContractOrderLink) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("contract", Contract.Type).Ref("order_links").Field("contract_id").Unique().Required(),
	}
}

func (ContractOrderLink) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("contract_id", "purchase_order_id").Unique(),
	}
}
