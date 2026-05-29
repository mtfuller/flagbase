package table

import (
	"testing"

	"github.com/mtfuller/flagbase/internal/database"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := database.Connect(":memory:")
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEngine(db)
}

func strPtr(s string) *string { return &s }

// ---- schema operations ----

func TestCreateAndGetTable(t *testing.T) {
	e := newTestEngine(t)
	def := &TableDef{
		Key:  "products",
		Name: "Products",
		Columns: []Column{
			{Name: "sku", Type: ColumnTypeText, Nullable: false, Default: strPtr("")},
			{Name: "price", Type: ColumnTypeReal, Nullable: true},
			{Name: "in_stock", Type: ColumnTypeBoolean, Nullable: false, Default: strPtr("true")},
		},
	}
	if err := e.CreateTable(def); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	got, err := e.GetTable("products")
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	if got == nil {
		t.Fatal("expected table, got nil")
	}
	if got.Key != "products" {
		t.Errorf("key: want products, got %s", got.Key)
	}
	if len(got.Columns) != 3 {
		t.Errorf("columns: want 3, got %d", len(got.Columns))
	}
	if got.Columns[1].Type != ColumnTypeReal {
		t.Errorf("column[1] type: want real, got %s", got.Columns[1].Type)
	}
}

func TestCreateTable_DuplicateKey(t *testing.T) {
	e := newTestEngine(t)
	def := &TableDef{Key: "dup", Name: "Dup"}
	if err := e.CreateTable(def); err != nil {
		t.Fatalf("first CreateTable: %v", err)
	}
	if err := e.CreateTable(&TableDef{Key: "dup", Name: "Dup2"}); err == nil {
		t.Fatal("expected error for duplicate key, got nil")
	}
}

func TestCreateTable_InvalidKey(t *testing.T) {
	e := newTestEngine(t)
	cases := []string{"Uppercase", "1startsdigit", "has space", "has-dash", ""}
	for _, k := range cases {
		if err := e.CreateTable(&TableDef{Key: k, Name: "X"}); err == nil {
			t.Errorf("expected error for key %q, got nil", k)
		}
	}
}

func TestCreateTable_ReservedColumnName(t *testing.T) {
	e := newTestEngine(t)
	def := &TableDef{
		Key:  "bad",
		Name: "Bad",
		Columns: []Column{
			{Name: "_id", Type: ColumnTypeText, Nullable: true},
		},
	}
	if err := e.CreateTable(def); err == nil {
		t.Fatal("expected error for reserved column _id, got nil")
	}
}

func TestListTables(t *testing.T) {
	e := newTestEngine(t)
	for _, key := range []string{"aaa", "bbb", "ccc"} {
		if err := e.CreateTable(&TableDef{Key: key, Name: key}); err != nil {
			t.Fatalf("CreateTable %s: %v", key, err)
		}
	}
	tables, err := e.ListTables()
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 3 {
		t.Errorf("want 3 tables, got %d", len(tables))
	}
}

func TestDeleteTable(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{Key: "temp", Name: "Temp"}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := e.DeleteTable("temp"); err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}
	got, err := e.GetTable("temp")
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete, got table")
	}
}

// ---- schema evolution ----

func TestAddColumns_NewColumn(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "items",
		Name: "Items",
		Columns: []Column{
			{Name: "title", Type: ColumnTypeText, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	if err := e.AddColumns("items", []Column{
		{Name: "quantity", Type: ColumnTypeInteger, Nullable: true},
	}); err != nil {
		t.Fatalf("AddColumns: %v", err)
	}

	got, err := e.GetTable("items")
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	if len(got.Columns) != 2 {
		t.Errorf("want 2 columns, got %d", len(got.Columns))
	}
	if got.Columns[1].Name != "quantity" {
		t.Errorf("second column: want quantity, got %s", got.Columns[1].Name)
	}
}

func TestAddColumns_ExistingIdentical_OK(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "items",
		Name: "Items",
		Columns: []Column{
			{Name: "title", Type: ColumnTypeText, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Re-specifying the same column with identical definition is a no-op.
	if err := e.AddColumns("items", []Column{
		{Name: "title", Type: ColumnTypeText, Nullable: true},
	}); err != nil {
		t.Fatalf("AddColumns with identical column: %v", err)
	}
}

func TestAddColumns_ExistingDifferentType_Rejected(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "items",
		Name: "Items",
		Columns: []Column{
			{Name: "title", Type: ColumnTypeText, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	err := e.AddColumns("items", []Column{
		{Name: "title", Type: ColumnTypeInteger, Nullable: true},
	})
	if err == nil {
		t.Fatal("expected error changing column type, got nil")
	}
}

func TestAddColumns_NewNonNullableNoDefault_Rejected(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{Key: "t", Name: "T"}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	err := e.AddColumns("t", []Column{
		{Name: "strict", Type: ColumnTypeText, Nullable: false},
	})
	if err == nil {
		t.Fatal("expected error for non-nullable column with no default on AddColumns, got nil")
	}
}

// ---- record CRUD ----

func TestInsertAndGetRecord(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "orders",
		Name: "Orders",
		Columns: []Column{
			{Name: "item", Type: ColumnTypeText, Nullable: false, Default: strPtr("")},
			{Name: "qty", Type: ColumnTypeInteger, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	rec, err := e.InsertRecord("orders", map[string]interface{}{
		"item": "widget",
		"qty":  float64(5),
	})
	if err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}
	if rec.ID == "" {
		t.Error("expected non-empty record ID")
	}
	if rec.Data["item"] != "widget" {
		t.Errorf("item: want widget, got %v", rec.Data["item"])
	}

	got, err := e.GetRecord("orders", rec.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.ID != rec.ID {
		t.Errorf("ID mismatch: want %s, got %s", rec.ID, got.ID)
	}
}

func TestInsertRecord_UnknownColumn_Rejected(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{Key: "t", Name: "T"}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := e.InsertRecord("t", map[string]interface{}{"ghost": "boo"}); err == nil {
		t.Fatal("expected error for unknown column, got nil")
	}
}

func TestUpdateRecord(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "notes",
		Name: "Notes",
		Columns: []Column{
			{Name: "body", Type: ColumnTypeText, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	rec, err := e.InsertRecord("notes", map[string]interface{}{"body": "original"})
	if err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}

	updated, err := e.UpdateRecord("notes", rec.ID, map[string]interface{}{"body": "updated"})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated record, got nil")
	}
	if updated.Data["body"] != "updated" {
		t.Errorf("body: want updated, got %v", updated.Data["body"])
	}
}

func TestDeleteRecord(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{Key: "t", Name: "T"}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	rec, err := e.InsertRecord("t", map[string]interface{}{})
	if err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}

	if err := e.DeleteRecord("t", rec.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	got, err := e.GetRecord("t", rec.ID)
	if err != nil {
		t.Fatalf("GetRecord after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete, got record")
	}
}

func TestQueryRecords_FilterAndPagination(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "users",
		Name: "Users",
		Columns: []Column{
			{Name: "role", Type: ColumnTypeText, Nullable: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	for _, role := range []string{"admin", "admin", "user"} {
		if _, err := e.InsertRecord("users", map[string]interface{}{"role": role}); err != nil {
			t.Fatalf("InsertRecord: %v", err)
		}
	}

	recs, err := e.QueryRecords("users", QueryOptions{
		Filters: []Filter{{Column: "role", Operator: "equals", Value: "admin"}},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("QueryRecords (filter): %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("want 2 admin records, got %d", len(recs))
	}

	page, err := e.QueryRecords("users", QueryOptions{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("QueryRecords (pagination): %v", err)
	}
	if len(page) != 2 {
		t.Errorf("want 2 records at offset 1, got %d", len(page))
	}
}

func TestBooleanColumnRoundtrip(t *testing.T) {
	e := newTestEngine(t)
	if err := e.CreateTable(&TableDef{
		Key:  "flags",
		Name: "Flags",
		Columns: []Column{
			{Name: "active", Type: ColumnTypeBoolean, Nullable: false, Default: strPtr("false")},
		},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	rec, err := e.InsertRecord("flags", map[string]interface{}{"active": true})
	if err != nil {
		t.Fatalf("InsertRecord: %v", err)
	}

	got, err := e.GetRecord("flags", rec.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Data["active"] != true {
		t.Errorf("active: want true, got %v (%T)", got.Data["active"], got.Data["active"])
	}
}
