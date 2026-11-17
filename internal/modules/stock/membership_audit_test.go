package stock

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/stockadjustment"
)

// TestMembershipAdjustmentServiceCaller pins the set-membership audit row for a service-key
// caller: adjusted_by is required, so it is always set (uuid.Nil, the system actor, when there is
// no user), and the notes name the service.
func TestMembershipAdjustmentServiceCaller(t *testing.T) {
	client := ent.NewClient()
	req := SetItemOutletMembershipRequest{ActorService: "platform-internal"}
	adj := newMembershipAdjustment(client.StockAdjustment, uuid.New(), uuid.New(), uuid.New(), 5, 5,
		stockadjustment.ReasonLocationHidden, "Hidden from outlet, 5 units retained", req, time.Now())

	by, ok := adj.Mutation().AdjustedBy()
	require.True(t, ok, "adjusted_by must be set or Save fails with a missing required field")
	require.Equal(t, uuid.Nil, by)
	notes, _ := adj.Mutation().Notes()
	require.Equal(t, "Hidden from outlet, 5 units retained. By service platform-internal", notes)
}

func TestMembershipAuditNotes(t *testing.T) {
	user := uuid.New()
	cases := []struct {
		name string
		auto string
		req  SetItemOutletMembershipRequest
		want string
	}{
		{"user with note", "Moved to B", SetItemOutletMembershipRequest{AdjustedBy: user, Notes: "restock"}, "restock — Moved to B"},
		{"user, service name ignored", "Moved to B", SetItemOutletMembershipRequest{AdjustedBy: user, ActorService: "x"}, "Moved to B"},
		{"service only", "", SetItemOutletMembershipRequest{ActorService: "pos-api"}, "By service pos-api"},
		{"neither", "Hidden", SetItemOutletMembershipRequest{}, "Hidden"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, membershipAuditNotes(c.auto, c.req))
		})
	}
}
