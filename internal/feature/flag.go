package feature

import "time"

// Flag is a named boolean feature toggle with optional targeting rules.
type Flag struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Enabled      bool      `json:"enabled"`
	DefaultValue bool      `json:"default_value"`
	Rules        []Rule    `json:"rules,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Rule targets a specific context attribute and returns a variant when matched.
type Rule struct {
	ID        string `json:"id"`
	FlagKey   string `json:"flag_key"`
	Attribute string `json:"attribute"`            // e.g. "role", "userId"
	Operator  string `json:"operator"`             // "equals"|"not_equals"|"contains"|"in"
	Value     string `json:"value"`                // comparison value; "in" uses comma-separated list
	Variant   bool   `json:"variant"`              // returned when rule matches
	Priority  int    `json:"priority"`
}
