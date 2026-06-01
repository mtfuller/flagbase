package iam_test

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mtfuller/flagbase/internal/iam"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
        id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL, password TEXT NOT NULL,
        role TEXT NOT NULL DEFAULT 'user', tenant_id TEXT NOT NULL DEFAULT 'default',
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS groups (
        id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, description TEXT NOT NULL DEFAULT '',
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS user_groups (
        user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
        PRIMARY KEY (user_id, group_id))`,
	}
	for _, s := range stmts {
		if _, err = db.Exec(s); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return db
}

func TestRegisterAndLogin(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	svc := iam.NewService(db, "test-secret", time.Hour)

	_, err := svc.Register("alice@example.com", "password123", "developer", "default")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	token, err := svc.Login("alice@example.com", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if claims.Role != "developer" {
		t.Errorf("expected role=developer, got %s", claims.Role)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	svc := iam.NewService(db, "secret", time.Hour)
	_, _ = svc.Register("bob@example.com", "correct", "user", "default")

	_, err := svc.Login("bob@example.com", "wrong")
	if err != iam.ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}
}

func TestDuplicateRegister(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	svc := iam.NewService(db, "secret", time.Hour)
	_, _ = svc.Register("carol@example.com", "pass", "user", "default")

	_, err := svc.Register("carol@example.com", "pass2", "user", "default")
	if err != iam.ErrUserExists {
		t.Errorf("expected ErrUserExists, got %v", err)
	}
}
