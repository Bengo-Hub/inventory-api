package events

import "encoding/json"

// Event represents a basic event structure used by inventory handlers when
// constructing domain events.
type Event struct {
	ID        string          `json:"id"`
	Source    string          `json:"source"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}
