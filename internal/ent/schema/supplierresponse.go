package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// QuotedItemJSON is one supplier's quote for a single RFQ line, stored inline on
// the SupplierResponse so a quote round-trips as one record.
type QuotedItemJSON struct {
	RFQLineID    uuid.UUID `json:"rfq_line_id"`
	UnitPrice    float64   `json:"unit_price"`
	LeadTimeDays int       `json:"lead_time_days"`
	Available    bool      `json:"available"`
	Notes        string    `json:"notes,omitempty"`
}

// SupplierResponse is one invited supplier's participation in an RFQ — invited,
// then submitted (with quoted_items) or declined.
type SupplierResponse struct {
	ent.Schema
}

func (SupplierResponse) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("rfq_id", uuid.UUID{}).Comment("FK to RFQ"),
		field.UUID("supplier_id", uuid.UUID{}).Comment("FK to Supplier"),
		field.Enum("status").Values("invited", "submitted", "declined").Default("invited"),
		field.String("currency").Default("KES"),
		field.Text("notes").Optional(),
		field.Time("submitted_at").Optional().Nillable(),
		field.JSON("quoted_items", []QuotedItemJSON{}).Default([]QuotedItemJSON{}),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (SupplierResponse) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("rfq", RFQ.Type).Ref("responses").Field("rfq_id").Unique().Required(),
	}
}

func (SupplierResponse) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rfq_id", "supplier_id").Unique(),
		index.Fields("tenant_id"),
	}
}
