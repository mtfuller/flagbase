package trigger

import (
	"encoding/json"
	"time"
)

// Event type constants for function_triggers.event_type.
const (
	EventTypeHTTP         = "http"
	EventTypeBucketCreate = "bucket_create"
	EventTypeTableCreate  = "table_create"
	EventTypeCron         = "cron"
)

// Trigger defines when and how a function is invoked by a platform event.
type Trigger struct {
	ID         string                 `json:"id"`
	FunctionID string                 `json:"function_id"`
	EventType  string                 `json:"event_type"`
	Config     map[string]interface{} `json:"config"`
	Enabled    bool                   `json:"enabled"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

// EventPayload is the JSON body passed to a WASM function via the event_read host call.
// Type is one of the EventType constants; Data is event-specific JSON.
type EventPayload struct {
	Type      string          `json:"type"`
	TriggerID string          `json:"trigger_id"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}
