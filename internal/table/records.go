package table

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InsertRecord writes a new row, validating values against the registered schema.
func (e *Engine) InsertRecord(tableKey string, data map[string]interface{}) (*Record, error) {
	def, err := e.GetTable(tableKey)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, fmt.Errorf("table %q not found", tableKey)
	}

	colNames, colVals, err := buildInsertData(def.Columns, data)
	if err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating record id: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(sqlTableName(tableKey))
	sb.WriteString(" (_id")
	for _, n := range colNames {
		sb.WriteString(", ")
		sb.WriteString(n) // safe: names validated against keyPattern in schema registry
	}
	sb.WriteString(") VALUES (?")
	for range colNames {
		sb.WriteString(", ?")
	}
	sb.WriteString(")")

	args := make([]interface{}, 0, 1+len(colVals))
	args = append(args, id)
	args = append(args, colVals...)
	if _, err := e.db.Exec(sb.String(), args...); err != nil {
		return nil, fmt.Errorf("inserting record: %w", err)
	}

	return e.GetRecord(tableKey, id)
}

// GetRecord fetches a single row by _id, returning nil if not found.
func (e *Engine) GetRecord(tableKey, id string) (*Record, error) {
	def, err := e.GetTable(tableKey)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, fmt.Errorf("table %q not found", tableKey)
	}

	q := selectClause(def.Columns, sqlTableName(tableKey)) + " WHERE _id = ?"
	row := e.db.QueryRow(q, id)
	rec, err := scanRow(row, def.Columns)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// UpdateRecord applies a partial update: only the supplied fields are changed.
// Returns nil if the record does not exist.
func (e *Engine) UpdateRecord(tableKey, id string, data map[string]interface{}) (*Record, error) {
	def, err := e.GetTable(tableKey)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, fmt.Errorf("table %q not found", tableKey)
	}

	colMap := make(map[string]Column, len(def.Columns))
	for _, c := range def.Columns {
		colMap[c.Name] = c
	}

	var setClauses []string
	var args []interface{}
	for k, v := range data {
		col, ok := colMap[k]
		if !ok {
			return nil, fmt.Errorf("unknown column %q", k)
		}
		if err := validateValue(col, v); err != nil {
			return nil, err
		}
		setClauses = append(setClauses, k+" = ?") // k safe: came from validated colMap
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return e.GetRecord(tableKey, id)
	}

	setClauses = append(setClauses, "_updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)

	q := "UPDATE " + sqlTableName(tableKey) + " SET " + strings.Join(setClauses, ", ") + " WHERE _id = ?"
	res, err := e.db.Exec(q, args...)
	if err != nil {
		return nil, fmt.Errorf("updating record: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return e.GetRecord(tableKey, id)
}

// DeleteRecord removes a row by _id.
func (e *Engine) DeleteRecord(tableKey, id string) error {
	if err := validateKey(tableKey); err != nil {
		return err
	}
	_, err := e.db.Exec("DELETE FROM "+sqlTableName(tableKey)+" WHERE _id = ?", id)
	return err
}

// QueryRecords returns rows matching the supplied filters, ordered by _created_at ASC.
// Limit is capped at 1000; offset defaults to 0. An empty Filters slice returns all rows.
func (e *Engine) QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error) {
	def, err := e.GetTable(tableKey)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, fmt.Errorf("table %q not found", tableKey)
	}

	colMap := make(map[string]Column, len(def.Columns))
	for _, c := range def.Columns {
		colMap[c.Name] = c
	}

	var whereClauses []string
	var args []interface{}
	for _, f := range opts.Filters {
		if _, ok := colMap[f.Column]; !ok {
			return nil, fmt.Errorf("unknown column %q in filter", f.Column)
		}
		op, ok := safeOp(f.Operator)
		if !ok {
			return nil, fmt.Errorf("unsupported filter operator %q", f.Operator)
		}
		// f.Column is safe: validated against colMap whose keys come from keyPattern
		whereClauses = append(whereClauses, f.Column+" "+op+" ?")
		if f.Operator == "contains" {
			args = append(args, "%"+f.Value+"%")
		} else {
			args = append(args, f.Value)
		}
	}

	q := selectClause(def.Columns, sqlTableName(tableKey))
	if len(whereClauses) > 0 {
		q += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	q += " ORDER BY _created_at ASC"

	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, opts.Offset)

	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying records: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		rec, err := scanRows(rows, def.Columns)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if records == nil {
		records = []*Record{}
	}
	return records, rows.Err()
}

// ---- internal helpers ----

func selectClause(cols []Column, table string) string {
	var sb strings.Builder
	sb.WriteString("SELECT _id, _created_at, _updated_at")
	for _, c := range cols {
		sb.WriteString(", ")
		sb.WriteString(c.Name)
	}
	sb.WriteString(" FROM ")
	sb.WriteString(table)
	return sb.String()
}

func scanRow(row *sql.Row, cols []Column) (*Record, error) {
	var id string
	var createdAt, updatedAt time.Time
	ptrs := []interface{}{&id, &createdAt, &updatedAt}
	holders := make([]interface{}, len(cols))
	for i := range cols {
		var v interface{}
		holders[i] = &v
		ptrs = append(ptrs, &v)
	}
	if err := row.Scan(ptrs...); err != nil {
		return nil, err
	}
	return buildRecord(id, createdAt, updatedAt, cols, holders), nil
}

func scanRows(rows *sql.Rows, cols []Column) (*Record, error) {
	var id string
	var createdAt, updatedAt time.Time
	ptrs := []interface{}{&id, &createdAt, &updatedAt}
	holders := make([]interface{}, len(cols))
	for i := range cols {
		var v interface{}
		holders[i] = &v
		ptrs = append(ptrs, &v)
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return buildRecord(id, createdAt, updatedAt, cols, holders), nil
}

// buildRecord assembles a Record, converting BOOLEAN columns from int64 to bool.
func buildRecord(id string, createdAt, updatedAt time.Time, cols []Column, holders []interface{}) *Record {
	data := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		v := *(holders[i].(*interface{}))
		if c.Type == ColumnTypeBoolean {
			if n, ok := v.(int64); ok {
				v = n != 0
			}
		}
		data[c.Name] = v
	}
	return &Record{ID: id, CreatedAt: createdAt, UpdatedAt: updatedAt, Data: data}
}

// buildInsertData returns parallel column-name / value slices for an INSERT,
// validating each supplied value and rejecting unknown columns.
func buildInsertData(cols []Column, data map[string]interface{}) ([]string, []interface{}, error) {
	colSet := make(map[string]Column, len(cols))
	for _, c := range cols {
		colSet[c.Name] = c
	}

	// Reject unknown columns before touching the DB.
	for k := range data {
		if _, ok := colSet[k]; !ok {
			return nil, nil, fmt.Errorf("unknown column %q", k)
		}
	}

	var names []string
	var vals []interface{}
	for _, c := range cols {
		v, supplied := data[c.Name]
		if !supplied {
			if !c.Nullable && c.Default == nil {
				return nil, nil, fmt.Errorf("column %q is required (not nullable and has no default)", c.Name)
			}
			continue
		}
		if err := validateValue(c, v); err != nil {
			return nil, nil, err
		}
		names = append(names, c.Name)
		vals = append(vals, v)
	}
	return names, vals, nil
}

// validateValue performs a lightweight type check on a user-supplied value.
// SQLite enforces storage types at the driver level as a second line of defence.
func validateValue(c Column, v interface{}) error {
	if v == nil {
		if !c.Nullable {
			return fmt.Errorf("column %q is not nullable", c.Name)
		}
		return nil
	}
	switch c.Type {
	case ColumnTypeInteger, ColumnTypeReal:
		switch v.(type) {
		case float64, float32, int, int32, int64:
			// JSON numbers decode as float64; direct Go callers may use int variants.
		default:
			return fmt.Errorf("column %q expects a numeric value, got %T", c.Name, v)
		}
	case ColumnTypeBoolean:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("column %q expects a boolean, got %T", c.Name, v)
		}
	case ColumnTypeText:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("column %q expects a string, got %T", c.Name, v)
		}
	}
	return nil
}
