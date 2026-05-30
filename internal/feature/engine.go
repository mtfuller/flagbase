package feature

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Engine is a thread-safe, in-memory feature flag evaluation engine backed by SQLite.
type Engine struct {
	db    *sql.DB
	mu    sync.RWMutex
	flags map[string]*Flag
}

// NewEngine creates an Engine and loads all flags from the database.
func NewEngine(db *sql.DB) (*Engine, error) {
	e := &Engine{
		db:    db,
		flags: make(map[string]*Flag),
	}
	return e, e.reload()
}

// EvaluateBool evaluates a boolean flag for the given context attributes.
//
// Evaluation order:
//  1. If the flag is disabled or missing → false.
//  2. If status=deprecated → DefaultValue (signal to remove gating code).
//  3. Per-user override (checked in all non-deprecated statuses).
//  4. If status=draft → false (overrides are the only way in).
//  5. Rule evaluation in priority order.
//  6. If status=testing → false for non-matched (DefaultValue not used).
//  7. DefaultValue (status=ga only).
func (e *Engine) EvaluateBool(key string, ctx map[string]interface{}) bool {
	variant := e.EvaluateVariant(key, ctx)
	return variantKeyToBool(variant)
}

// EvaluateVariant returns the resolved variant key for a flag.
// Returns "true"/"false" for legacy bool rules, or a named variant key for A/B splits.
func (e *Engine) EvaluateVariant(key string, ctx map[string]interface{}) string {
	e.mu.RLock()
	f, ok := e.flags[key]
	e.mu.RUnlock()

	if !ok || !f.Enabled {
		return "false"
	}

	if f.Status == FlagStatusDeprecated {
		return boolToVariantKey(f.DefaultValue)
	}

	// Per-user override: works in draft, testing, and ga.
	if userID, _ := ctx["userId"].(string); userID != "" && userID != "anonymous" {
		for i := range f.Overrides {
			if f.Overrides[i].UserID == userID {
				return f.Overrides[i].VariantKey
			}
		}
	}

	// draft: only overrides grant access.
	if f.Status == FlagStatusDraft {
		return "false"
	}

	for _, rule := range f.Rules {
		attrVal, exists := ctx[rule.Attribute]
		if !exists {
			continue
		}
		if matchRule(rule, fmt.Sprintf("%v", attrVal)) {
			return resolveRuleVariant(rule)
		}
	}

	// testing: non-matched users get false (not DefaultValue) to keep roll-out intentional.
	if f.Status == FlagStatusTesting {
		return "false"
	}

	return boolToVariantKey(f.DefaultValue)
}

// CreateFlag persists a new flag (and its rules) and reloads the cache.
func (e *Engine) CreateFlag(f *Flag) error {
	if f.ID == "" {
		id, err := newID()
		if err != nil {
			return fmt.Errorf("generating flag id: %w", err)
		}
		f.ID = id
	}
	if f.Status == "" {
		f.Status = FlagStatusDraft
	}
	now := time.Now()
	f.CreatedAt = now
	f.UpdatedAt = now

	_, err := e.db.Exec(
		`INSERT INTO feature_flags (id, key, name, description, enabled, default_value, status, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Key, f.Name, f.Description, f.Enabled, f.DefaultValue, string(f.Status), f.CreatedAt, f.UpdatedAt,
	)
	if err != nil {
		return err
	}

	for i := range f.Rules {
		f.Rules[i].FlagKey = f.Key
		if err := e.insertRule(&f.Rules[i]); err != nil {
			return err
		}
	}

	return e.reload()
}

// UpdateFlag updates an existing flag's metadata and reloads the cache.
func (e *Engine) UpdateFlag(f *Flag) error {
	if f.Status == "" {
		f.Status = FlagStatusGA
	}
	f.UpdatedAt = time.Now()
	_, err := e.db.Exec(
		`UPDATE feature_flags SET name=?, description=?, enabled=?, default_value=?, status=?, updated_at=? WHERE key=?`,
		f.Name, f.Description, f.Enabled, f.DefaultValue, string(f.Status), f.UpdatedAt, f.Key,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// TransitionStatus moves a flag to a new lifecycle status.
func (e *Engine) TransitionStatus(key string, status FlagStatus) error {
	switch status {
	case FlagStatusDraft, FlagStatusTesting, FlagStatusGA, FlagStatusDeprecated:
	default:
		return fmt.Errorf("unknown status %q: must be draft, testing, ga, or deprecated", status)
	}
	_, err := e.db.Exec(
		`UPDATE feature_flags SET status=?, updated_at=? WHERE key=?`,
		string(status), time.Now(), key,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// DeleteFlag removes a flag and its rules, then reloads the cache.
func (e *Engine) DeleteFlag(key string) error {
	_, err := e.db.Exec(`DELETE FROM feature_flags WHERE key = ?`, key)
	if err != nil {
		return err
	}
	return e.reload()
}

// ListFlags returns a snapshot of all flags in the cache.
func (e *Engine) ListFlags() []*Flag {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*Flag, 0, len(e.flags))
	for _, f := range e.flags {
		out = append(out, f)
	}
	return out
}

// GetFlag returns a single flag by key.
func (e *Engine) GetFlag(key string) (*Flag, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	f, ok := e.flags[key]
	return f, ok
}

// --- Variants ---

// CreateVariant adds a named variant to a flag (used for A/B testing).
func (e *Engine) CreateVariant(v *Variant) error {
	id, err := newID()
	if err != nil {
		return fmt.Errorf("generating variant id: %w", err)
	}
	v.ID = id
	_, err = e.db.Exec(
		`INSERT INTO flag_variants (id, flag_key, key, weight) VALUES (?, ?, ?, ?)`,
		v.ID, v.FlagKey, v.Key, v.Weight,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// DeleteVariant removes a named variant from a flag.
func (e *Engine) DeleteVariant(flagKey, variantKey string) error {
	_, err := e.db.Exec(
		`DELETE FROM flag_variants WHERE flag_key = ? AND key = ?`, flagKey, variantKey,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// ListVariants returns all variants for a flag (from the live cache).
func (e *Engine) ListVariants(flagKey string) []Variant {
	e.mu.RLock()
	f, ok := e.flags[flagKey]
	e.mu.RUnlock()
	if !ok {
		return []Variant{}
	}
	out := make([]Variant, len(f.Variants))
	copy(out, f.Variants)
	return out
}

// --- Overrides ---

// CreateOverride pins a user to a specific variant, bypassing normal evaluation.
func (e *Engine) CreateOverride(ov *Override) error {
	id, err := newID()
	if err != nil {
		return fmt.Errorf("generating override id: %w", err)
	}
	ov.ID = id
	ov.CreatedAt = time.Now()
	_, err = e.db.Exec(
		`INSERT INTO flag_overrides (id, flag_key, user_id, variant_key, created_at)
         VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(flag_key, user_id) DO UPDATE SET variant_key=excluded.variant_key`,
		ov.ID, ov.FlagKey, ov.UserID, ov.VariantKey, ov.CreatedAt,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// DeleteOverride removes the per-user override for a flag.
func (e *Engine) DeleteOverride(flagKey, userID string) error {
	_, err := e.db.Exec(
		`DELETE FROM flag_overrides WHERE flag_key = ? AND user_id = ?`, flagKey, userID,
	)
	if err != nil {
		return err
	}
	return e.reload()
}

// ListOverrides returns all overrides for a flag (from the live cache).
func (e *Engine) ListOverrides(flagKey string) []Override {
	e.mu.RLock()
	f, ok := e.flags[flagKey]
	e.mu.RUnlock()
	if !ok {
		return []Override{}
	}
	out := make([]Override, len(f.Overrides))
	copy(out, f.Overrides)
	return out
}

// --- internal ---

func (e *Engine) reload() error {
	rows, err := e.db.Query(
		`SELECT id, key, name, description, enabled, default_value, status, created_at, updated_at FROM feature_flags`,
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	flags := make(map[string]*Flag)
	for rows.Next() {
		f := &Flag{}
		if err := rows.Scan(&f.ID, &f.Key, &f.Name, &f.Description, &f.Enabled, &f.DefaultValue, &f.Status, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return err
		}
		if f.Status == "" {
			f.Status = FlagStatusGA
		}
		flags[f.Key] = f
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for key, f := range flags {
		rules, err := e.loadRules(key)
		if err != nil {
			return err
		}
		f.Rules = rules

		variants, err := e.loadVariants(key)
		if err != nil {
			return err
		}
		f.Variants = variants

		overrides, err := e.loadOverrides(key)
		if err != nil {
			return err
		}
		f.Overrides = overrides
	}

	e.mu.Lock()
	e.flags = flags
	e.mu.Unlock()
	return nil
}

func (e *Engine) loadRules(flagKey string) ([]Rule, error) {
	rows, err := e.db.Query(
		`SELECT id, flag_key, attribute, operator, value, variant, COALESCE(variant_key,''), priority
         FROM flag_rules WHERE flag_key = ? ORDER BY priority ASC`,
		flagKey,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var rules []Rule
	for rows.Next() {
		r := Rule{}
		if err := rows.Scan(&r.ID, &r.FlagKey, &r.Attribute, &r.Operator, &r.Value, &r.Variant, &r.VariantKey, &r.Priority); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (e *Engine) loadVariants(flagKey string) ([]Variant, error) {
	rows, err := e.db.Query(
		`SELECT id, flag_key, key, weight FROM flag_variants WHERE flag_key = ? ORDER BY key ASC`,
		flagKey,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var variants []Variant
	for rows.Next() {
		v := Variant{}
		if err := rows.Scan(&v.ID, &v.FlagKey, &v.Key, &v.Weight); err != nil {
			return nil, err
		}
		variants = append(variants, v)
	}
	return variants, rows.Err()
}

func (e *Engine) loadOverrides(flagKey string) ([]Override, error) {
	rows, err := e.db.Query(
		`SELECT id, flag_key, user_id, variant_key, created_at
         FROM flag_overrides WHERE flag_key = ? ORDER BY created_at ASC`,
		flagKey,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var overrides []Override
	for rows.Next() {
		ov := Override{}
		if err := rows.Scan(&ov.ID, &ov.FlagKey, &ov.UserID, &ov.VariantKey, &ov.CreatedAt); err != nil {
			return nil, err
		}
		overrides = append(overrides, ov)
	}
	return overrides, rows.Err()
}

func (e *Engine) insertRule(r *Rule) error {
	if r.ID == "" {
		id, err := newID()
		if err != nil {
			return fmt.Errorf("generating rule id: %w", err)
		}
		r.ID = id
	}
	_, err := e.db.Exec(
		`INSERT INTO flag_rules (id, flag_key, attribute, operator, value, variant, variant_key, priority)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.FlagKey, r.Attribute, r.Operator, r.Value, r.Variant, r.VariantKey, r.Priority,
	)
	return err
}

func matchRule(rule Rule, val string) bool {
	switch rule.Operator {
	case "equals":
		return strings.EqualFold(val, rule.Value)
	case "not_equals":
		return !strings.EqualFold(val, rule.Value)
	case "contains":
		return strings.Contains(strings.ToLower(val), strings.ToLower(rule.Value))
	case "in":
		for _, p := range strings.Split(rule.Value, ",") {
			if strings.EqualFold(strings.TrimSpace(p), val) {
				return true
			}
		}
	}
	return false
}

// resolveRuleVariant returns the variant key for a matched rule.
// VariantKey takes precedence over the legacy bool Variant field.
func resolveRuleVariant(r Rule) string {
	if r.VariantKey != "" {
		return r.VariantKey
	}
	return boolToVariantKey(r.Variant)
}

// variantKeyToBool converts a variant key to a boolean.
// Only "false" and the empty string are falsy; everything else (including named variants) is truthy.
func variantKeyToBool(key string) bool {
	return key != "false" && key != ""
}

func boolToVariantKey(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
