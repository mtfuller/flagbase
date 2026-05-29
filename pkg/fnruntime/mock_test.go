//go:build !wasip1

package fnruntime_test

import (
	"testing"

	"github.com/mtfuller/flagbase/pkg/fnruntime"
)

func TestMockRuntime_Bucket(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.PutObjectInBucket("cfg", "a.json", []byte(`{"x":1}`))

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	data, err := fnruntime.GetObject("cfg", "a.json")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(data) != `{"x":1}` {
		t.Errorf("got %q", data)
	}

	keys, _ := fnruntime.ListObjects("cfg")
	if len(keys) != 1 || keys[0] != "a.json" {
		t.Errorf("ListObjects = %v", keys)
	}

	if err := fnruntime.DeleteObject("cfg", "a.json"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := fnruntime.GetObject("cfg", "a.json"); err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestMockRuntime_BucketWrite(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	if err := fnruntime.PutObject("out", "result.txt", []byte("hello")); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	objects := rt.ObjectsInBucket("out")
	if string(objects["result.txt"]) != "hello" {
		t.Errorf("observation mismatch: %v", objects)
	}
}

func TestMockRuntime_Flags(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.SetFlag("feat-a", true)

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	if !fnruntime.EvaluateFlag("feat-a") {
		t.Error("expected feat-a = true")
	}
	if fnruntime.EvaluateFlag("feat-b") {
		t.Error("expected feat-b = false (not seeded)")
	}
}

func TestMockRuntime_Table_InsertAndQuery(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	rec, err := fnruntime.PutRecord("orders", map[string]interface{}{
		"item": "widget",
		"qty":  float64(3),
	})
	if err != nil {
		t.Fatalf("PutRecord insert: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected generated ID")
	}
	if rec.Data["item"] != "widget" {
		t.Errorf("item = %v", rec.Data["item"])
	}

	rows, err := fnruntime.QueryRecords("orders", fnruntime.QueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("QueryRecords: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	// Observation via rt
	all := rt.RecordsInTable("orders")
	if len(all) != 1 {
		t.Fatalf("expected 1 in observation, got %d", len(all))
	}
}

func TestMockRuntime_Table_Update(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.SeedRecord("items", "id-1", map[string]interface{}{"name": "original"})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	updated, err := fnruntime.PutRecord("items", map[string]interface{}{
		"_id":  "id-1",
		"name": "updated",
	})
	if err != nil {
		t.Fatalf("PutRecord update: %v", err)
	}
	if updated.Data["name"] != "updated" {
		t.Errorf("name = %v", updated.Data["name"])
	}

	got, err := fnruntime.GetRecord("items", "id-1")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got == nil {
		t.Fatal("record not found")
	}
	if got.Data["name"] != "updated" {
		t.Errorf("GetRecord name = %v", got.Data["name"])
	}
}

func TestMockRuntime_Table_Delete(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.SeedRecord("items", "id-2", map[string]interface{}{"name": "to-delete"})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	if err := fnruntime.DeleteRecord("items", "id-2"); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	got, _ := fnruntime.GetRecord("items", "id-2")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestMockRuntime_Fetch(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.SetFetcher(func(req fnruntime.FetchRequest) (*fnruntime.FetchResponse, error) {
		return &fnruntime.FetchResponse{Status: 200, Body: []byte("ok")}, nil
	})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	resp, err := fnruntime.Fetch(fnruntime.FetchRequest{Method: "GET", URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "ok" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestMockRuntime_FetchUnset(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	_, err := fnruntime.Fetch(fnruntime.FetchRequest{Method: "GET", URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error when no fetcher is set")
	}
}

func TestMockRuntime_InvokeFunction(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	rt.SetInvoker(func(id string) ([]byte, error) {
		return []byte("result from " + id), nil
	})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	out, err := fnruntime.InvokeFunction("fn-abc")
	if err != nil {
		t.Fatalf("InvokeFunction: %v", err)
	}
	if string(out) != "result from fn-abc" {
		t.Errorf("got %q", out)
	}
}

func TestMockRuntime_NilPanics(t *testing.T) {
	fnruntime.SetMockRuntime(nil)
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when no runtime is set")
		}
	}()
	fnruntime.EvaluateFlag("x") //nolint:errcheck
}

func TestMockRuntime_Chaining(t *testing.T) {
	rt := fnruntime.NewMockRuntime().
		PutObjectInBucket("b", "k", []byte("v")).
		SetFlag("f", true).
		SeedRecord("t", "r1", map[string]interface{}{"x": 1})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	if !fnruntime.EvaluateFlag("f") {
		t.Error("flag not set")
	}
	if d, _ := fnruntime.GetObject("b", "k"); string(d) != "v" {
		t.Errorf("bucket = %q", d)
	}
	if rec, _ := fnruntime.GetRecord("t", "r1"); rec == nil {
		t.Error("seeded record not found")
	}
}
