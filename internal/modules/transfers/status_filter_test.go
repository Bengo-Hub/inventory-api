package transfers

import (
	"reflect"
	"testing"

	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
)

func TestParseTransferStatuses(t *testing.T) {
	cases := map[string][]stocktransfer.Status{
		"":                  nil,
		"in_transit":        {stocktransfer.StatusInTransit},
		"draft, in_transit": {stocktransfer.StatusDraft, stocktransfer.StatusInTransit},
		"bogus":             nil,
		"received,bogus,,":  {stocktransfer.StatusReceived},
	}
	for in, want := range cases {
		if got := parseTransferStatuses(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseTransferStatuses(%q) = %v, want %v", in, got, want)
		}
	}
}
