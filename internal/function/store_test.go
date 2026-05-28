package function_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mtfuller/flagbase/internal/function"
	"github.com/mtfuller/flagbase/internal/storage"
)

func setupStore(t *testing.T) *function.Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS functions (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		language TEXT NOT NULL, source TEXT NOT NULL, runtime TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := os.MkdirTemp("", "flagbase-store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	ctx := context.Background()
	eng := function.NewEngine(ctx)
	t.Cleanup(func() { eng.Close(ctx) })

	return function.NewStore(db, storage.NewLocalAdapter(dir), eng)
}

func TestStore_CreateAndInvokeJS(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	fn, err := st.Create(ctx, "greet", "say hello", "javascript",
		`function handle() { console.log("hello js"); }`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if fn.Status != "ready" {
		t.Fatalf("expected status ready, got %s: %s", fn.Status, fn.Error)
	}

	out, err := st.Invoke(ctx, fn.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(string(out), "hello js") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestStore_JSMissingHandle(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	fn, err := st.Create(ctx, "no-handle", "", "javascript", `console.log("no handle fn");`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if fn.Status != "ready" {
		t.Fatalf("expected status ready, got %s", fn.Status)
	}
	_, err = st.Invoke(ctx, fn.ID, 5*time.Second)
	if err == nil {
		t.Fatal("expected error for missing handle function")
	}
}

func TestStore_InvalidLanguage(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	_, err := st.Create(ctx, "bad", "", "rust", `fn main() {}`)
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestStore_DeleteRemovesRecord(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	fn, err := st.Create(ctx, "temp", "", "javascript", `function handle() {}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Delete(ctx, fn.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := st.Get(ctx, fn.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}
