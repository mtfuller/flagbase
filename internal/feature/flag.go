package feature

import "time"

// FlagStatus is the lifecycle stage of a feature flag.
type FlagStatus string

const (
	// FlagStatusDraft: visible only to users with an explicit override — safe for
	// day-one dev testing without exposing the flag to anyone else.
	FlagStatusDraft FlagStatus = "draft"

	// FlagStatusTesting: overrides + rules are evaluated; non-matching users get
	// false regardless of DefaultValue, so roll-out is still intentional.
	FlagStatusTesting FlagStatus = "testing"

	// FlagStatusGA: full production evaluation — overrides, rules, then DefaultValue.
	FlagStatusGA FlagStatus = "ga"

	// FlagStatusDeprecated: always returns DefaultValue. Signals that the gating
	// code should be removed; rules and overrides are no longer consulted.
	FlagStatusDeprecated FlagStatus = "deprecated"
)

// Variant is a named outcome of a feature flag used for A/B testing.
// Variants let you bucket users into labelled groups (e.g. "control" vs
// "treatment") while keeping a single flag as the gate.
type Variant struct {
	ID      string `json:"id"`
	FlagKey string `json:"flag_key"`
	Key     string `json:"key"`    // e.g. "control", "treatment"
	Weight  int    `json:"weight"` // relative weight for future % allocation
}

// Override pins a specific user to a specific variant, bypassing normal rule
// evaluation entirely.  Use this to let a developer test a draft or in-progress
// flag without it becoming visible to other users.
type Override struct {
	ID         string    `json:"id"`
	FlagKey    string    `json:"flag_key"`
	UserID     string    `json:"user_id"`
	VariantKey string    `json:"variant_key"` // "true", "false", or a named Variant key
	CreatedAt  time.Time `json:"created_at"`
}

// Flag is a named boolean feature toggle with optional targeting rules.
type Flag struct {
	ID           string     `json:"id"`
	Key          string     `json:"key"`
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Status       FlagStatus `json:"status"`
	Enabled      bool       `json:"enabled"`
	DefaultValue bool       `json:"default_value"`
	Rules        []Rule     `json:"rules,omitempty"`
	Variants     []Variant  `json:"variants,omitempty"`
	Overrides    []Override `json:"overrides,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Rule targets a specific context attribute and returns a variant when matched.
type Rule struct {
	ID         string `json:"id"`
	FlagKey    string `json:"flag_key"`
	Attribute  string `json:"attribute"`             // e.g. "role", "userId"
	Operator   string `json:"operator"`              // "equals"|"not_equals"|"contains"|"in"
	Value      string `json:"value"`                 // comparison value; "in" uses comma-separated list
	Variant    bool   `json:"variant"`               // backward-compat bool result
	VariantKey string `json:"variant_key,omitempty"` // named variant; takes precedence over Variant when set
	Priority   int    `json:"priority"`
}
