package table

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// keyPattern restricts table keys and column names to safe SQL identifiers.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// reservedColumns cannot be used as user-defined column names.
var reservedColumns = map[string]bool{"_id": true, "_created_at": true, "_updated_at": true}

// ColumnType enumerates the supported SQLite-compatible column types.
type ColumnType string

const (
	ColumnTypeText    ColumnType = "text"
	ColumnTypeInteger ColumnType = "integer"
	ColumnTypeReal    ColumnType = "real"
	ColumnTypeBoolean ColumnType = "boolean"
)

// Column describes a single user-defined column in a TableDef.
// Once written to the registry, a column's Name and Type are immutable.
type Column struct {
	ID        string     `json:"id"`
	TableKey  string     `json:"table_key"`
	Name      string     `json:"name"`
	Type      ColumnType `json:"type"`
	Nullable  bool       `json:"nullable"`
	Default   *string    `json:"default,omitempty"`
	Position  int        `json:"position"`
	CreatedAt time.Time  `json:"created_at"`
}

// TableDef is the schema descriptor for a user-defined table.
type TableDef struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Columns     []Column  `json:"columns"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Record is a single row returned from a table. System fields use an underscore prefix.
type Record struct {
	ID        string                 `json:"_id"`
	CreatedAt time.Time              `json:"_created_at"`
	UpdatedAt time.Time              `json:"_updated_at"`
	Data      map[string]interface{} `json:"data"`
}

// Filter is a single predicate applied in QueryRecords.
type Filter struct {
	Column   string `json:"column"`
	Operator string `json:"operator"` // equals | not_equals | contains | gt | gte | lt | lte
	Value    string `json:"value"`
}

// QueryOptions controls filtering and pagination for QueryRecords.
type QueryOptions struct {
	Filters []Filter `json:"filters"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

// Engine manages user-defined tables: schema registry + physical SQLite tables.
type Engine struct {
	db *sql.DB
}

// NewEngine creates a table Engine backed by the supplied database.
func NewEngine(db *sql.DB) *Engine {
	return &Engine{db: db}
}

// CreateTable validates the definition, persists the schema, and creates the physical table.
// All columns must be nullable or declare a DEFAULT so that future ALTER TABLE ADD COLUMN
// operations on existing rows remain valid.
func (e *Engine) CreateTable(def *TableDef) error {
	if err := validateKey(def.Key); err != nil {
		return err
	}
	seen := make(map[string]bool, len(def.Columns))
	for i := range def.Columns {
		c := &def.Columns[i]
		if err := validateColumn(c); err != nil {
			return err
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate column name %q", c.Name)
		}
		seen[c.Name] = true
		c.TableKey = def.Key
		c.Position = i
	}

	now := time.Now()
	def.CreatedAt = now
	def.UpdatedAt = now

	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(
		`INSERT INTO table_definitions (key, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		def.Key, def.Name, def.Description, now, now,
	)
	if err != nil {
		return fmt.Errorf("inserting table definition: %w", err)
	}

	for i := range def.Columns {
		c := &def.Columns[i]
		id, err := newID()
		if err != nil {
			return fmt.Errorf("generating column id: %w", err)
		}
		c.ID = id
		c.CreatedAt = now
		if _, err := tx.Exec(
			`INSERT INTO table_columns (id, table_key, name, col_type, nullable, default_val, position, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.TableKey, c.Name, string(c.Type), c.Nullable, c.Default, c.Position, now,
		); err != nil {
			return fmt.Errorf("inserting column %q: %w", c.Name, err)
		}
	}

	if _, err := tx.Exec(buildCreateDDL(def.Key, def.Columns)); err != nil {
		return fmt.Errorf("creating physical table: %w", err)
	}

	return tx.Commit()
}

// GetTable returns the full schema for a table, or nil if not found.
func (e *Engine) GetTable(key string) (*TableDef, error) {
	var def TableDef
	err := e.db.QueryRow(
		`SELECT key, name, description, created_at, updated_at FROM table_definitions WHERE key = ?`, key,
	).Scan(&def.Key, &def.Name, &def.Description, &def.CreatedAt, &def.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading table definition: %w", err)
	}
	cols, err := e.loadColumns(key)
	if err != nil {
		return nil, err
	}
	def.Columns = cols
	return &def, nil
}

// ListTables returns all table definitions without their column lists.
func (e *Engine) ListTables() ([]*TableDef, error) {
	rows, err := e.db.Query(
		`SELECT key, name, description, created_at, updated_at FROM table_definitions ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var defs []*TableDef
	for rows.Next() {
		var def TableDef
		if err := rows.Scan(&def.Key, &def.Name, &def.Description, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return nil, err
		}
		defs = append(defs, &def)
	}
	if defs == nil {
		defs = []*TableDef{}
	}
	return defs, rows.Err()
}

// AddColumns adds new columns to an existing table. Any column whose name already
// exists in the registry must have an identical type and nullability — existing
// column definitions are immutable. New columns must be nullable or declare a
// DEFAULT so that existing rows remain valid without a backfill.
func (e *Engine) AddColumns(key string, newCols []Column) error {
	def, err := e.GetTable(key)
	if err != nil {
		return err
	}
	if def == nil {
		return fmt.Errorf("table %q not found", key)
	}

	existing := make(map[string]Column, len(def.Columns))
	for _, c := range def.Columns {
		existing[c.Name] = c
	}
	nextPos := len(def.Columns)
	now := time.Now()

	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i := range newCols {
		c := &newCols[i]
		if err := validateColumn(c); err != nil {
			return err
		}
		if ex, exists := existing[c.Name]; exists {
			// Enforce immutability: same name must have same type + nullability.
			if ex.Type != c.Type || ex.Nullable != c.Nullable {
				return fmt.Errorf("column %q already exists with a different definition (columns are immutable)", c.Name)
			}
			continue
		}
		// Brand-new column must be backwards-compatible with existing rows.
		if !c.Nullable && c.Default == nil {
			return fmt.Errorf("new column %q must be nullable or declare a default value", c.Name)
		}

		id, err := newID()
		if err != nil {
			return fmt.Errorf("generating column id: %w", err)
		}
		c.ID = id
		c.TableKey = key
		c.Position = nextPos
		c.CreatedAt = now
		nextPos++

		if _, err := tx.Exec(
			`INSERT INTO table_columns (id, table_key, name, col_type, nullable, default_val, position, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, key, c.Name, string(c.Type), c.Nullable, c.Default, c.Position, now,
		); err != nil {
			return fmt.Errorf("registering column %q: %w", c.Name, err)
		}

		if _, err := tx.Exec(buildAlterDDL(key, *c)); err != nil {
			return fmt.Errorf("adding column %q to physical table: %w", c.Name, err)
		}
	}

	if _, err := tx.Exec(`UPDATE table_definitions SET updated_at = ? WHERE key = ?`, now, key); err != nil {
		return fmt.Errorf("updating table timestamp: %w", err)
	}

	return tx.Commit()
}

// DeleteTable removes the schema registration and drops the physical table.
func (e *Engine) DeleteTable(key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM table_definitions WHERE key = ?`, key); err != nil {
		return fmt.Errorf("deleting table definition: %w", err)
	}
	// sqlTableName output is safe — validated by keyPattern above.
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + sqlTableName(key)); err != nil {
		return fmt.Errorf("dropping physical table: %w", err)
	}

	return tx.Commit()
}

// ---- DDL builders ----

// sqlTableName returns the physical SQLite table name for a given user-defined key.
// The "tbl_" prefix prevents collisions with Flagbase's own tables.
func sqlTableName(key string) string {
	return "tbl_" + key
}

func buildCreateDDL(key string, cols []Column) string {
	var sb strings.Builder
	sb.WriteString("CREATE TABLE IF NOT EXISTS ")
	sb.WriteString(sqlTableName(key)) // safe: keyPattern validated
	sb.WriteString(" (_id TEXT PRIMARY KEY, _created_at DATETIME DEFAULT CURRENT_TIMESTAMP, _updated_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	for _, c := range cols {
		sb.WriteString(", ")
		sb.WriteString(colFragment(c))
	}
	sb.WriteString(")")
	return sb.String()
}

func buildAlterDDL(tableKey string, c Column) string {
	return "ALTER TABLE " + sqlTableName(tableKey) + " ADD COLUMN " + colFragment(c)
}

func colFragment(c Column) string {
	var sb strings.Builder
	sb.WriteString(c.Name) // safe: keyPattern validated
	sb.WriteString(" ")
	sb.WriteString(sqlColType(c.Type))
	if !c.Nullable {
		sb.WriteString(" NOT NULL")
	}
	if c.Default != nil {
		sb.WriteString(" DEFAULT ")
		sb.WriteString(sqlLiteral(c.Type, *c.Default))
	}
	return sb.String()
}

// ---- helpers ----

func (e *Engine) loadColumns(tableKey string) ([]Column, error) {
	rows, err := e.db.Query(
		`SELECT id, table_key, name, col_type, nullable, default_val, position, created_at
		 FROM table_columns WHERE table_key = ? ORDER BY position ASC`,
		tableKey,
	)
	if err != nil {
		return nil, fmt.Errorf("loading columns: %w", err)
	}
	defer rows.Close()
	var cols []Column
	for rows.Next() {
		var c Column
		var def sql.NullString
		if err := rows.Scan(&c.ID, &c.TableKey, &c.Name, &c.Type, &c.Nullable, &def, &c.Position, &c.CreatedAt); err != nil {
			return nil, err
		}
		if def.Valid {
			s := def.String
			c.Default = &s
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// ---- validation ----

func validateKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid key %q: must match ^[a-z][a-z0-9_]{0,62}$", key)
	}
	return nil
}

func validateColumn(c *Column) error {
	if reservedColumns[c.Name] {
		return fmt.Errorf("column name %q is reserved", c.Name)
	}
	if !keyPattern.MatchString(c.Name) {
		return fmt.Errorf("invalid column name %q: must match ^[a-z][a-z0-9_]{0,62}$", c.Name)
	}
	if !validColumnType(c.Type) {
		return fmt.Errorf("column %q: unsupported type %q (must be text, integer, real, or boolean)", c.Name, c.Type)
	}
	return nil
}

func validColumnType(t ColumnType) bool {
	switch t {
	case ColumnTypeText, ColumnTypeInteger, ColumnTypeReal, ColumnTypeBoolean:
		return true
	}
	return false
}

// sqlColType maps ColumnType to a SQLite type affinity string.
func sqlColType(t ColumnType) string {
	switch t {
	case ColumnTypeInteger:
		return "INTEGER"
	case ColumnTypeReal:
		return "REAL"
	case ColumnTypeBoolean:
		return "INTEGER" // SQLite has no native BOOLEAN; 0/1 integers
	default:
		return "TEXT"
	}
}

// sqlLiteral produces a safe SQL DEFAULT literal. Names and types are
// already validated; this only needs to sanitize the value string.
func sqlLiteral(t ColumnType, v string) string {
	switch t {
	case ColumnTypeBoolean:
		if v == "true" || v == "1" {
			return "1"
		}
		return "0"
	case ColumnTypeInteger, ColumnTypeReal:
		safe := strings.TrimSpace(v)
		ok := len(safe) > 0
		for _, ch := range safe {
			if !((ch >= '0' && ch <= '9') || ch == '.' || ch == '-') {
				ok = false
				break
			}
		}
		if ok {
			return safe
		}
		return "NULL"
	default:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
}

func safeOp(op string) (string, bool) {
	switch op {
	case "equals":
		return "=", true
	case "not_equals":
		return "!=", true
	case "contains":
		return "LIKE", true
	case "gt":
		return ">", true
	case "gte":
		return ">=", true
	case "lt":
		return "<", true
	case "lte":
		return "<=", true
	}
	return "", false
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
