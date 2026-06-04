package items

import "fmt"

// ValidateTicketTiers ensures the sum of ticket-tier capacities (stored in metadata.ticket_tiers)
// never exceeds the event's total_capacity. Applies only to SERVICE items that carry a positive
// total_capacity and at least one tier; returns nil when not applicable.
func ValidateTicketTiers(dto *ItemDTO) error {
	if dto.Type != "SERVICE" || dto.TotalCapacity == nil || *dto.TotalCapacity <= 0 || dto.Metadata == nil {
		return nil
	}
	raw, ok := dto.Metadata["ticket_tiers"]
	if !ok {
		return nil
	}
	tiers, ok := raw.([]interface{})
	if !ok || len(tiers) == 0 {
		return nil
	}
	total := 0
	for _, t := range tiers {
		m, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		switch c := m["capacity"].(type) {
		case float64:
			total += int(c)
		case int:
			total += c
		}
	}
	if total > *dto.TotalCapacity {
		return fmt.Errorf("ticket tier capacities total %d but the event total capacity is only %d", total, *dto.TotalCapacity)
	}
	return nil
}
