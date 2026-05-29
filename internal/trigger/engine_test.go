package trigger

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nats-io/nats.go"
	"github.com/robfig/cron/v3"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS functions (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		language TEXT NOT NULL,
		source TEXT NOT NULL,
		runtime TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		error TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create functions table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS function_triggers (
		id TEXT PRIMARY KEY,
		function_id TEXT NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
		event_type TEXT NOT NULL,
		config TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create function_triggers table: %v", err)
	}
	return db
}

func insertTestFunction(t *testing.T, db *sql.DB) string {
	t.Helper()
	id, _ := newID()
	if _, err := db.Exec(
		`INSERT INTO functions (id, name, description, language, source, runtime, status) VALUES (?, 'test', '', 'go', '', 'wasm', 'ready')`,
		id,
	); err != nil {
		t.Fatalf("insert function: %v", err)
	}
	return id
}

func TestCreateAndGet(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	fnID := insertTestFunction(t, db)

	e := &Engine{db: db, subs: map[string]*nats.Subscription{}, cronEnts: map[string]cron.EntryID{}, scheduler: cron.New()}
	ctx := context.Background()

	trig, err := e.Create(ctx, fnID, EventTypeHTTP, map[string]interface{}{}, true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if trig.ID == "" {
		t.Fatal("expected non-empty trigger ID")
	}
	if trig.EventType != EventTypeHTTP {
		t.Errorf("want event_type=http, got %s", trig.EventType)
	}
	if !trig.Enabled {
		t.Error("want enabled=true")
	}

	got, err := e.Get(ctx, trig.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected trigger, got nil")
	}
	if got.ID != trig.ID {
		t.Errorf("ID mismatch: want %s got %s", trig.ID, got.ID)
	}
}

func TestList(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	fnID := insertTestFunction(t, db)
	e := &Engine{db: db, subs: map[string]*nats.Subscription{}, cronEnts: map[string]cron.EntryID{}, scheduler: cron.New()}
	ctx := context.Background()

	for _, et := range []string{EventTypeHTTP, EventTypeCron, EventTypeBucketCreate} {
		cfg := map[string]interface{}{}
		if et == EventTypeCron {
			cfg["schedule"] = "*/5 * * * *"
		} else if et == EventTypeBucketCreate {
			cfg["bucket"] = "test-bucket"
		}
		if _, err := e.Create(ctx, fnID, et, cfg, false); err != nil {
			t.Fatalf("Create %s: %v", et, err)
		}
	}

	all, err := e.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("want 3 triggers, got %d", len(all))
	}

	byFn, err := e.ListByFunction(ctx, fnID)
	if err != nil {
		t.Fatalf("ListByFunction: %v", err)
	}
	if len(byFn) != 3 {
		t.Errorf("want 3 triggers for function, got %d", len(byFn))
	}
}

func TestUpdate(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	fnID := insertTestFunction(t, db)
	e := &Engine{db: db, subs: map[string]*nats.Subscription{}, cronEnts: map[string]cron.EntryID{}, scheduler: cron.New()}
	ctx := context.Background()

	trig, _ := e.Create(ctx, fnID, EventTypeBucketCreate, map[string]interface{}{"bucket": "old"}, true)
	updated, err := e.Update(ctx, trig.ID, false, map[string]interface{}{"bucket": "new"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Enabled {
		t.Error("want enabled=false after update")
	}
	if updated.Config["bucket"] != "new" {
		t.Errorf("want bucket=new, got %v", updated.Config["bucket"])
	}
}

func TestDelete(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()
	fnID := insertTestFunction(t, db)
	e := &Engine{db: db, subs: map[string]*nats.Subscription{}, cronEnts: map[string]cron.EntryID{}, scheduler: cron.New()}
	ctx := context.Background()

	trig, _ := e.Create(ctx, fnID, EventTypeHTTP, map[string]interface{}{}, true)
	if err := e.Delete(ctx, trig.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := e.Get(ctx, trig.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestValidateEventType(t *testing.T) {
	for _, et := range []string{EventTypeHTTP, EventTypeBucketCreate, EventTypeTableCreate, EventTypeCron} {
		if err := validateEventType(et); err != nil {
			t.Errorf("valid type %q rejected: %v", et, err)
		}
	}
	if err := validateEventType("invalid"); err == nil {
		t.Error("expected error for invalid event type")
	}
}

func TestEventPayloadJSON(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"bucket": "test", "object_name": "file.txt"})
	p := EventPayload{
		Type:      EventTypeBucketCreate,
		TriggerID: "abc123",
		Timestamp: time.Now(),
		Data:      json.RawMessage(raw),
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded EventPayload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TriggerID != "abc123" {
		t.Errorf("TriggerID mismatch: %s", decoded.TriggerID)
	}
}

func TestNatsSubject(t *testing.T) {
	e := &Engine{}
	cases := []struct {
		trigger  Trigger
		expected string
	}{
		{Trigger{EventType: EventTypeBucketCreate, Config: map[string]interface{}{"bucket": "my-bucket"}}, "flagbase.bucket.my-bucket.created"},
		{Trigger{EventType: EventTypeTableCreate, Config: map[string]interface{}{"table_key": "orders"}}, "flagbase.table.orders.created"},
		{Trigger{EventType: EventTypeBucketCreate, Config: map[string]interface{}{}}, ""},
		{Trigger{EventType: EventTypeHTTP}, ""},
	}
	for _, c := range cases {
		got := e.natsSubject(&c.trigger)
		if got != c.expected {
			t.Errorf("natsSubject(%+v) = %q, want %q", c.trigger, got, c.expected)
		}
	}
}
