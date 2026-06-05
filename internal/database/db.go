package database

import (
	"database/sql"
	"strings"

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

// Migrate applies the embedded schema and any additive column migrations.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return migrateAdditive(db)
}

// migrateAdditive runs ALTER TABLE statements that are safe to re-run; duplicate-column
// errors are silently ignored so the function is idempotent.
func migrateAdditive(db *sql.DB) error {
	stmts := []string{
		`ALTER TABLE feature_flags      ADD COLUMN status TEXT NOT NULL DEFAULT 'ga'`,
		`ALTER TABLE flag_rules         ADD COLUMN variant_key TEXT`,
		`ALTER TABLE function_invocations ADD COLUMN execution_ms INTEGER`,
		`ALTER TABLE function_invocations ADD COLUMN peak_memory_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE function_invocations ADD COLUMN host_calls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE function_invocations ADD COLUMN output_size_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE function_invocations ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE function_versions ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS packages (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			version      TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'pending',
			error        TEXT NOT NULL DEFAULT '',
			bundle_size  INTEGER NOT NULL DEFAULT 0,
			requested_by TEXT NOT NULL DEFAULT '',
			approved_by  TEXT,
			requested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			approved_at  DATETIME,
			UNIQUE(name, version)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
		}
	}
	return nil
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

CREATE TABLE IF NOT EXISTS function_invocations (
    id           TEXT PRIMARY KEY,
    function_id  TEXT NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
    started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    success      BOOLEAN NOT NULL DEFAULT 0,
    output       TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_fn_invocations_fn ON function_invocations(function_id);

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

CREATE TABLE IF NOT EXISTS table_definitions (
    key         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS table_columns (
    id          TEXT PRIMARY KEY,
    table_key   TEXT NOT NULL REFERENCES table_definitions(key) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    col_type    TEXT NOT NULL,
    nullable    BOOLEAN NOT NULL DEFAULT 1,
    default_val TEXT,
    position    INTEGER NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(table_key, name)
);

CREATE INDEX IF NOT EXISTS idx_table_columns_table ON table_columns(table_key);

CREATE TABLE IF NOT EXISTS flag_variants (
    id         TEXT PRIMARY KEY,
    flag_key   TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    weight     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(flag_key, key)
);
CREATE INDEX IF NOT EXISTS idx_flag_variants_flag ON flag_variants(flag_key);

CREATE TABLE IF NOT EXISTS flag_overrides (
    id          TEXT PRIMARY KEY,
    flag_key    TEXT NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    user_id     TEXT NOT NULL,
    variant_key TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(flag_key, user_id)
);
CREATE INDEX IF NOT EXISTS idx_flag_overrides_flag ON flag_overrides(flag_key);

CREATE TABLE IF NOT EXISTS traces (
    id           TEXT PRIMARY KEY,
    root_type    TEXT NOT NULL DEFAULT '',
    root_ref     TEXT NOT NULL DEFAULT '',
    user_id      TEXT NOT NULL DEFAULT '',
    tenant_id    TEXT NOT NULL DEFAULT '',
    started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    status       TEXT NOT NULL DEFAULT 'running'
);
CREATE INDEX IF NOT EXISTS idx_traces_status  ON traces(status);
CREATE INDEX IF NOT EXISTS idx_traces_started ON traces(started_at);

CREATE TABLE IF NOT EXISTS trace_events (
    id              TEXT PRIMARY KEY,
    trace_id        TEXT NOT NULL REFERENCES traces(id) ON DELETE CASCADE,
    parent_event_id TEXT,
    event_type      TEXT NOT NULL,
    sequence        INTEGER NOT NULL DEFAULT 0,
    started_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at    DATETIME,
    duration_ms     INTEGER,
    status          TEXT NOT NULL DEFAULT 'success',
    metadata        TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_trace_events_trace ON trace_events(trace_id);

CREATE TABLE IF NOT EXISTS function_custom_metrics (
    id            TEXT PRIMARY KEY,
    trace_id      TEXT NOT NULL DEFAULT '',
    function_id   TEXT NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
    invocation_id TEXT NOT NULL DEFAULT '',
    metric_name   TEXT NOT NULL,
    metric_value  REAL NOT NULL DEFAULT 0,
    tags          TEXT NOT NULL DEFAULT '{}',
    recorded_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_fn_custom_metrics_fn   ON function_custom_metrics(function_id);
CREATE INDEX IF NOT EXISTS idx_fn_custom_metrics_name ON function_custom_metrics(metric_name);

CREATE TABLE IF NOT EXISTS anomaly_events (
    id            TEXT PRIMARY KEY,
    anomaly_type  TEXT NOT NULL,
    ref_id        TEXT NOT NULL,
    severity      TEXT NOT NULL DEFAULT 'warning',
    message       TEXT NOT NULL,
    detected_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    resolved_at   DATETIME,
    auto_resolved INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_anomaly_events_ref ON anomaly_events(ref_id);

CREATE TABLE IF NOT EXISTS function_triggers (
    id          TEXT PRIMARY KEY,
    function_id TEXT NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    config      TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_function_triggers_fn ON function_triggers(function_id);

CREATE TABLE IF NOT EXISTS groups (
    id          TEXT PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_groups (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id    TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_user_groups_user  ON user_groups(user_id);
CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups(group_id);
`
