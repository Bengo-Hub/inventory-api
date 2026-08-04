package handlers

import "encoding/json"

// projectFields prunes each row in a JSON-marshalable slice down to only the requested
// top-level keys before it goes over the wire. Used by list endpoints whose UI lets users
// hide columns (inventory-ui's DataTable column-visibility picker) so an unchecked column's
// data is never fetched/serialized in the first place — a client opts in via ?fields=a,b,c.
//
// Business logic is never affected: enrichment always runs on the FULL object server-side;
// only the OUTPUT is trimmed. alwaysKeep is merged into the requested set unconditionally
// (e.g. a row's id/sku so the client can still key rows regardless of which columns it hid).
func projectFields[T any](rows []T, fields []string, alwaysKeep ...string) ([]map[string]any, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	keep := make(map[string]bool, len(fields)+len(alwaysKeep))
	for _, f := range fields {
		if f != "" {
			keep[f] = true
		}
	}
	for _, f := range alwaysKeep {
		keep[f] = true
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		trimmed := make(map[string]any, len(keep))
		for k, v := range m {
			if keep[k] {
				trimmed[k] = v
			}
		}
		out = append(out, trimmed)
	}
	return out, nil
}
