package database

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

// Connect opens a SQLite database with WAL mode and sensible production settings.
func Connect(path string) (*sql.DB, error) {
	dsn := path + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// Single writer connection avoids WAL lock contention.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// Migrate applies the embedded schema to the database.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,
    email       TEXT UNIQUE NOT NULL,
    password    TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'user',
    tenant_id   TEXT NOT NULL DEFAULT 'default',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS feature_flags (
    id            TEXT PRIMARY KEY,
    key           TEXT UNIQUE NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT,
    enabled       BOOLEAN NOT NULL DEFAULT 0,
    default_value BOOLEAN NOT NULL DEFAULT 0,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS flag_rules (
    id          TEXT PRIMARY KEY,
    flag_key    TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    attribute   TEXT NOT NULL,
    operator    TEXT NOT NULL,
    value       TEXT NOT NULL,
    variant     BOOLEAN NOT NULL DEFAULT 1,
    priority    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS metrics (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    flag_key    TEXT NOT NULL,
    variant     TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    value       REAL NOT NULL DEFAULT 1,
    recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_metrics_flag_key  ON metrics(flag_key);
CREATE INDEX IF NOT EXISTS idx_flag_rules_flag   ON flag_rules(flag_key);

CREATE TABLE IF NOT EXISTS functions (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    language    TEXT NOT NULL,
    source      TEXT NOT NULL,
    runtime     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    error       TEXT NOT NULL DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS function_versions (
    id          TEXT PRIMARY KEY,
    function_id TEXT NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_function_versions_fn ON function_versions(function_id);

CREATE TABLE IF NOT EXISTS frontends (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    slug              TEXT UNIQUE NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    active_version_id TEXT,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS frontend_versions (
    id          TEXT PRIMARY KEY,
    frontend_id TEXT NOT NULL REFERENCES frontends(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    file_count  INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_frontend_versions_frontend ON frontend_versions(frontend_id);
`
