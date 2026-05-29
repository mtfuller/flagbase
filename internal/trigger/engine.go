package trigger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mtfuller/flagbase/internal/event"
	"github.com/mtfuller/flagbase/internal/function"
	"github.com/nats-io/nats.go"
	"github.com/robfig/cron/v3"
)

// Engine manages function triggers: persistence, NATS subscriptions, and cron scheduling.
type Engine struct {
	db        *sql.DB
	bus       *event.Bus
	functions *function.Store

	mu        sync.Mutex
	subs      map[string]*nats.Subscription
	cronEnts  map[string]cron.EntryID
	scheduler *cron.Cron
}

// NewEngine creates a trigger Engine.
func NewEngine(db *sql.DB, bus *event.Bus, functions *function.Store) *Engine {
	return &Engine{
		db:        db,
		bus:       bus,
		functions: functions,
		subs:      make(map[string]*nats.Subscription),
		cronEnts:  make(map[string]cron.EntryID),
		scheduler: cron.New(),
	}
}

// Start loads all enabled triggers from the DB and activates their subscriptions/cron jobs.
func (e *Engine) Start(ctx context.Context) error {
	triggers, err := e.List(ctx)
	if err != nil {
		return fmt.Errorf("loading triggers: %w", err)
	}
	for i := range triggers {
		t := &triggers[i]
		if !t.Enabled {
			continue
		}
		// Non-fatal: log activation errors at startup so one bad trigger doesn't block the rest.
		if err := e.activate(t); err != nil {
			_ = err
		}
	}
	e.scheduler.Start()
	return nil
}

// Stop unsubscribes all NATS subscriptions and stops the cron scheduler.
func (e *Engine) Stop() {
	e.mu.Lock()
	for _, sub := range e.subs {
		_ = sub.Unsubscribe()
	}
	e.subs = make(map[string]*nats.Subscription)
	e.cronEnts = make(map[string]cron.EntryID)
	e.mu.Unlock()
	e.scheduler.Stop()
}

// activate sets up the event subscription or cron job for an enabled trigger.
func (e *Engine) activate(t *Trigger) error {
	switch t.EventType {
	case EventTypeBucketCreate, EventTypeTableCreate:
		return e.subscribe(t)
	case EventTypeCron:
		return e.scheduleCron(t)
	case EventTypeHTTP:
		return nil // handled synchronously in ServeHTTPTrigger
	}
	return fmt.Errorf("unknown event type: %s", t.EventType)
}

// deactivate removes an active NATS subscription or cron entry for a trigger.
func (e *Engine) deactivate(triggerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sub, ok := e.subs[triggerID]; ok {
		_ = sub.Unsubscribe()
		delete(e.subs, triggerID)
	}
	if entID, ok := e.cronEnts[triggerID]; ok {
		e.scheduler.Remove(entID)
		delete(e.cronEnts, triggerID)
	}
}

func (e *Engine) subscribe(t *Trigger) error {
	if e.bus == nil {
		return nil // no event bus wired; subscriptions are no-ops
	}
	subject := e.natsSubject(t)
	if subject == "" {
		return fmt.Errorf("trigger %s: cannot derive NATS subject (check config)", t.ID)
	}
	id := t.ID
	fnID := t.FunctionID
	eventType := t.EventType
	sub, err := e.bus.Subscribe(subject, func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		payload, _ := json.Marshal(EventPayload{
			Type:      eventType,
			TriggerID: id,
			Timestamp: time.Now(),
			Data:      json.RawMessage(msg.Data),
		})
		_, _ = e.functions.InvokeWithEvent(ctx, fnID, 30*time.Second, payload)
	})
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", subject, err)
	}
	e.mu.Lock()
	e.subs[t.ID] = sub
	e.mu.Unlock()
	return nil
}

func (e *Engine) scheduleCron(t *Trigger) error {
	schedule, _ := t.Config["schedule"].(string)
	if schedule == "" {
		return fmt.Errorf("cron trigger %s: missing schedule in config", t.ID)
	}
	id := t.ID
	fnID := t.FunctionID
	entID, err := e.scheduler.AddFunc(schedule, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, _ := json.Marshal(map[string]interface{}{
			"schedule": schedule,
			"fired_at": time.Now(),
		})
		payload, _ := json.Marshal(EventPayload{
			Type:      EventTypeCron,
			TriggerID: id,
			Timestamp: time.Now(),
			Data:      json.RawMessage(raw),
		})
		_, _ = e.functions.InvokeWithEvent(ctx, fnID, 30*time.Second, payload)
	})
	if err != nil {
		return fmt.Errorf("scheduling cron %q: %w", schedule, err)
	}
	e.mu.Lock()
	e.cronEnts[t.ID] = entID
	e.mu.Unlock()
	return nil
}

// natsSubject returns the NATS subject to subscribe to for a given trigger.
func (e *Engine) natsSubject(t *Trigger) string {
	switch t.EventType {
	case EventTypeBucketCreate:
		bucket, _ := t.Config["bucket"].(string)
		if bucket == "" {
			return ""
		}
		return "flagbase.bucket." + bucket + ".created"
	case EventTypeTableCreate:
		tableKey, _ := t.Config["table_key"].(string)
		if tableKey == "" {
			return ""
		}
		return "flagbase.table." + tableKey + ".created"
	}
	return ""
}

// ServeHTTPTrigger builds an HTTP event payload and invokes the function synchronously.
// eventData should be the raw JSON body (or nil).
func (e *Engine) ServeHTTPTrigger(ctx context.Context, triggerID string, eventData []byte) ([]byte, error) {
	t, err := e.Get(ctx, triggerID)
	if err != nil {
		return nil, fmt.Errorf("loading trigger: %w", err)
	}
	if t == nil {
		return nil, fmt.Errorf("trigger not found")
	}
	if !t.Enabled {
		return nil, fmt.Errorf("trigger is disabled")
	}
	if t.EventType != EventTypeHTTP {
		return nil, fmt.Errorf("trigger %s is not an HTTP trigger (got %s)", triggerID, t.EventType)
	}
	if eventData == nil {
		eventData = []byte("{}")
	}
	payload, _ := json.Marshal(EventPayload{
		Type:      EventTypeHTTP,
		TriggerID: triggerID,
		Timestamp: time.Now(),
		Data:      json.RawMessage(eventData),
	})
	return e.functions.InvokeWithEvent(ctx, t.FunctionID, 30*time.Second, payload)
}

// --- CRUD ---

// Create persists a new trigger and activates it if enabled.
func (e *Engine) Create(ctx context.Context, functionID, eventType string, config map[string]interface{}, enabled bool) (*Trigger, error) {
	if err := validateEventType(eventType); err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	now := time.Now()
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO function_triggers (id, function_id, event_type, config, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, functionID, eventType, string(configJSON), boolToInt(enabled), now, now,
	); err != nil {
		return nil, fmt.Errorf("inserting trigger: %w", err)
	}
	t := &Trigger{
		ID:         id,
		FunctionID: functionID,
		EventType:  eventType,
		Config:     config,
		Enabled:    enabled,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if enabled {
		_ = e.activate(t)
	}
	return t, nil
}

// List returns all triggers ordered by creation date descending.
func (e *Engine) List(ctx context.Context) ([]Trigger, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, function_id, event_type, config, enabled, created_at, updated_at
		 FROM function_triggers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

// ListByFunction returns triggers for a specific function ordered by creation date descending.
func (e *Engine) ListByFunction(ctx context.Context, functionID string) ([]Trigger, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, function_id, event_type, config, enabled, created_at, updated_at
		 FROM function_triggers WHERE function_id = ? ORDER BY created_at DESC`, functionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

// Get returns a single trigger by ID, or nil if not found.
func (e *Engine) Get(ctx context.Context, id string) (*Trigger, error) {
	var t Trigger
	var configJSON string
	var enabledInt int
	err := e.db.QueryRowContext(ctx,
		`SELECT id, function_id, event_type, config, enabled, created_at, updated_at
		 FROM function_triggers WHERE id = ?`, id,
	).Scan(&t.ID, &t.FunctionID, &t.EventType, &configJSON, &enabledInt, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = enabledInt != 0
	if err := json.Unmarshal([]byte(configJSON), &t.Config); err != nil {
		t.Config = map[string]interface{}{}
	}
	return &t, nil
}

// Update changes the enabled flag and config of an existing trigger, then re-activates it.
func (e *Engine) Update(ctx context.Context, id string, enabled bool, config map[string]interface{}) (*Trigger, error) {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	now := time.Now()
	if _, err := e.db.ExecContext(ctx,
		`UPDATE function_triggers SET enabled = ?, config = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), string(configJSON), now, id,
	); err != nil {
		return nil, fmt.Errorf("updating trigger: %w", err)
	}
	t, err := e.Get(ctx, id)
	if err != nil || t == nil {
		return t, err
	}
	// Re-activate with the new config.
	e.deactivate(id)
	if enabled {
		_ = e.activate(t)
	}
	return t, nil
}

// Delete removes a trigger and its active subscription or cron job.
func (e *Engine) Delete(ctx context.Context, id string) error {
	e.deactivate(id)
	_, err := e.db.ExecContext(ctx, `DELETE FROM function_triggers WHERE id = ?`, id)
	return err
}

// --- helpers ---

func scanTriggers(rows *sql.Rows) ([]Trigger, error) {
	var triggers []Trigger
	for rows.Next() {
		var t Trigger
		var configJSON string
		var enabledInt int
		if err := rows.Scan(&t.ID, &t.FunctionID, &t.EventType, &configJSON, &enabledInt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabledInt != 0
		if err := json.Unmarshal([]byte(configJSON), &t.Config); err != nil {
			t.Config = map[string]interface{}{}
		}
		triggers = append(triggers, t)
	}
	if triggers == nil {
		triggers = []Trigger{}
	}
	return triggers, rows.Err()
}

func validateEventType(et string) error {
	switch et {
	case EventTypeHTTP, EventTypeBucketCreate, EventTypeTableCreate, EventTypeCron:
		return nil
	}
	return fmt.Errorf("unsupported event_type %q (must be http, bucket_create, table_create, or cron)", et)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
