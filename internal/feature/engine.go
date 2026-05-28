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
// Rules are tested in priority order; the first match wins.
// Returns DefaultValue when no rule matches, or false when the flag is disabled/missing.
func (e *Engine) EvaluateBool(key string, ctx map[string]interface{}) bool {
	e.mu.RLock()
	f, ok := e.flags[key]
	e.mu.RUnlock()

	if !ok || !f.Enabled {
		return false
	}

	for _, rule := range f.Rules {
		attrVal, exists := ctx[rule.Attribute]
		if !exists {
			continue
		}
		if matchRule(rule, fmt.Sprintf("%v", attrVal)) {
			return rule.Variant
		}
	}
	return f.DefaultValue
}

// CreateFlag persists a new flag (and its rules) and reloads the cache.
func (e *Engine) CreateFlag(f *Flag) error {
	if f.ID == "" {
		f.ID = newID()
	}
	now := time.Now()
	f.CreatedAt = now
	f.UpdatedAt = now

	_, err := e.db.Exec(
		`INSERT INTO feature_flags (id, key, name, description, enabled, default_value, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Key, f.Name, f.Description, f.Enabled, f.DefaultValue, f.CreatedAt, f.UpdatedAt,
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
	f.UpdatedAt = time.Now()
	_, err := e.db.Exec(
		`UPDATE feature_flags SET name=?, description=?, enabled=?, default_value=?, updated_at=? WHERE key=?`,
		f.Name, f.Description, f.Enabled, f.DefaultValue, f.UpdatedAt, f.Key,
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

// reload rebuilds the in-memory flag cache from the database.
func (e *Engine) reload() error {
	rows, err := e.db.Query(
		`SELECT id, key, name, description, enabled, default_value, created_at, updated_at FROM feature_flags`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	flags := make(map[string]*Flag)
	for rows.Next() {
		f := &Flag{}
		if err := rows.Scan(&f.ID, &f.Key, &f.Name, &f.Description, &f.Enabled, &f.DefaultValue, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return err
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
	}

	e.mu.Lock()
	e.flags = flags
	e.mu.Unlock()
	return nil
}

func (e *Engine) loadRules(flagKey string) ([]Rule, error) {
	rows, err := e.db.Query(
		`SELECT id, flag_key, attribute, operator, value, variant, priority
         FROM flag_rules WHERE flag_key = ? ORDER BY priority ASC`,
		flagKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		r := Rule{}
		if err := rows.Scan(&r.ID, &r.FlagKey, &r.Attribute, &r.Operator, &r.Value, &r.Variant, &r.Priority); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (e *Engine) insertRule(r *Rule) error {
	if r.ID == "" {
		r.ID = newID()
	}
	_, err := e.db.Exec(
		`INSERT INTO flag_rules (id, flag_key, attribute, operator, value, variant, priority)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.FlagKey, r.Attribute, r.Operator, r.Value, r.Variant, r.Priority,
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

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
