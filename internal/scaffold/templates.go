// Package scaffold provides source templates for Flagbase function projects.
// Both the CLI (fn init) and the HTTP API (GET /scaffold) embed these same
// templates so that locally-initialised and server-pulled projects are identical.
package scaffold

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FnruntimeDocGo is the fnruntime package doc file (no build constraint).
const FnruntimeDocGo = `// Package fnruntime provides Go bindings for Flagbase host functions available
// to WASM functions compiled with GOOS=wasip1 GOARCH=wasm.
//
// For local unit tests (non-WASM builds) the package uses an in-memory mock
// backend. Call fnruntime.SetMockRuntime(fnruntime.NewMockRuntime()) in each
// test before invoking main().
package fnruntime
`

// FnruntimeRuntimeWasip1Go is the low-level WASM memory helper file.
const FnruntimeRuntimeWasip1Go = `//go:build wasip1

package fnruntime

import "unsafe"

//go:wasmimport flagbase result_read
func _resultRead(outPtr, outLen uint32) uint32

//go:wasmimport flagbase error_read
func _errorRead(outPtr, outLen uint32) uint32

var errBuf [4096]byte

func readResult(size uint32) []byte {
	buf := make([]byte, size)
	if size == 0 {
		return buf
	}
	_resultRead(bufPtr(buf), uint32(len(buf)))
	return buf
}

func readLastError() string {
	n := _errorRead(uint32(uintptr(unsafe.Pointer(&errBuf[0]))), uint32(len(errBuf)))
	if n == 0 {
		return "unknown error"
	}
	return string(errBuf[:n])
}

func strPtr(s string) (ptr, length uint32) {
	if len(s) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s)))), uint32(len(s))
}

func bufPtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b))))
}
`

// FnruntimeBucketWasip1Go is the bucket storage host binding file.
const FnruntimeBucketWasip1Go = `//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase bucket_get
func _bucketGet(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32

//go:wasmimport flagbase bucket_put
func _bucketPut(bucketPtr, bucketLen, keyPtr, keyLen, dataPtr, dataLen uint32) uint32

//go:wasmimport flagbase bucket_delete
func _bucketDelete(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32

//go:wasmimport flagbase bucket_list
func _bucketList(bucketPtr, bucketLen uint32) uint32

const errResult = uint32(0xFFFFFFFF)

// GetObject retrieves an object from the named bucket.
func GetObject(bucket, key string) ([]byte, error) {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	size := _bucketGet(bPtr, bLen, kPtr, kLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	return readResult(size), nil
}

// PutObject stores data under key in the named bucket.
func PutObject(bucket, key string, data []byte) error {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	dPtr := bufPtr(data)
	ok := _bucketPut(bPtr, bLen, kPtr, kLen, dPtr, uint32(len(data)))
	if ok == 0 {
		return &hostError{msg: readLastError()}
	}
	return nil
}

// DeleteObject removes an object from the named bucket.
func DeleteObject(bucket, key string) error {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	ok := _bucketDelete(bPtr, bLen, kPtr, kLen)
	if ok == 0 {
		return &hostError{msg: readLastError()}
	}
	return nil
}

// ListObjects returns the names of all objects in the named bucket.
func ListObjects(bucket string) ([]string, error) {
	bPtr, bLen := strPtr(bucket)
	size := _bucketList(bPtr, bLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	data := readResult(size)
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, &hostError{msg: "decoding list response: " + err.Error()}
	}
	return names, nil
}

type hostError struct{ msg string }

func (e *hostError) Error() string { return e.msg }
`

// FnruntimeFlagsWasip1Go is the feature flag evaluation host binding file.
const FnruntimeFlagsWasip1Go = `//go:build wasip1

package fnruntime

//go:wasmimport flagbase flag_eval
func _flagEval(keyPtr, keyLen uint32) uint32

// EvaluateFlag evaluates a feature flag by key. Returns false on error.
func EvaluateFlag(key string) bool {
	kPtr, kLen := strPtr(key)
	return _flagEval(kPtr, kLen) == 1
}
`

// FnruntimeInvokeWasip1Go is the peer function invocation host binding file.
const FnruntimeInvokeWasip1Go = `//go:build wasip1

package fnruntime

//go:wasmimport flagbase fn_invoke
func _fnInvoke(idPtr, idLen uint32) uint32

// InvokeFunction calls another Flagbase function by ID and returns its output.
func InvokeFunction(id string) ([]byte, error) {
	iPtr, iLen := strPtr(id)
	size := _fnInvoke(iPtr, iLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	return readResult(size), nil
}
`

// FnruntimeFetchWasip1Go is the outbound HTTP host binding file.
const FnruntimeFetchWasip1Go = `//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase http_fetch
func _httpFetch(reqPtr, reqLen uint32) uint32

// FetchRequest describes an outbound HTTP request executed by the host.
type FetchRequest struct {
	Method  string            ` + "`" + `json:"method"` + "`" + `
	URL     string            ` + "`" + `json:"url"` + "`" + `
	Headers map[string]string ` + "`" + `json:"headers,omitempty"` + "`" + `
	Body    []byte            ` + "`" + `json:"body,omitempty"` + "`" + `
}

// FetchResponse contains the HTTP response returned by the host.
type FetchResponse struct {
	Status  int               ` + "`" + `json:"status"` + "`" + `
	Headers map[string]string ` + "`" + `json:"headers"` + "`" + `
	Body    []byte            ` + "`" + `json:"body"` + "`" + `
}

// Fetch performs an outbound HTTP request. The host enforces a 15-second timeout.
func Fetch(req FetchRequest) (*FetchResponse, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, &hostError{msg: "encoding fetch request: " + err.Error()}
	}
	rPtr := bufPtr(reqBytes)
	size := _httpFetch(rPtr, uint32(len(reqBytes)))
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	data := readResult(size)
	var resp FetchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, &hostError{msg: "decoding fetch response: " + err.Error()}
	}
	return &resp, nil
}
`

// FnruntimeTableWasip1Go is the table storage host binding file.
const FnruntimeTableWasip1Go = `//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase table_get
func _tableGet(tableKeyPtr, tableKeyLen, idPtr, idLen uint32) uint32

//go:wasmimport flagbase table_put
func _tablePut(tableKeyPtr, tableKeyLen, dataPtr, dataLen uint32) uint32

//go:wasmimport flagbase table_delete
func _tableDelete(tableKeyPtr, tableKeyLen, idPtr, idLen uint32) uint32

//go:wasmimport flagbase table_query
func _tableQuery(tableKeyPtr, tableKeyLen, optsPtr, optsLen uint32) uint32

// Record is a single table row returned from the host.
type Record struct {
	ID   string                 ` + "`" + `json:"_id"` + "`" + `
	Data map[string]interface{} ` + "`" + `json:"data"` + "`" + `
}

// Filter is a single predicate for QueryRecords.
type Filter struct {
	Column   string ` + "`" + `json:"column"` + "`" + `
	Operator string ` + "`" + `json:"operator"` + "`" + ` // equals | not_equals | contains | gt | gte | lt | lte
	Value    string ` + "`" + `json:"value"` + "`" + `
}

// QueryOptions controls filtering and pagination for QueryRecords.
type QueryOptions struct {
	Filters []Filter ` + "`" + `json:"filters,omitempty"` + "`" + `
	Limit   int      ` + "`" + `json:"limit,omitempty"` + "`" + `
	Offset  int      ` + "`" + `json:"offset,omitempty"` + "`" + `
}

// GetRecord retrieves a single table row by ID. Returns nil, nil when not found.
func GetRecord(tableKey, id string) (*Record, error) {
	tPtr, tLen := strPtr(tableKey)
	iPtr, iLen := strPtr(id)
	size := _tableGet(tPtr, tLen, iPtr, iLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	var rec Record
	if err := json.Unmarshal(readResult(size), &rec); err != nil {
		return nil, &hostError{msg: "decoding record: " + err.Error()}
	}
	return &rec, nil
}

// PutRecord inserts or updates a table row. If record contains "_id" the host
// updates that row; otherwise a new row is inserted. Returns the saved record.
func PutRecord(tableKey string, record map[string]interface{}) (*Record, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, &hostError{msg: "encoding record: " + err.Error()}
	}
	tPtr, tLen := strPtr(tableKey)
	dPtr := bufPtr(data)
	size := _tablePut(tPtr, tLen, dPtr, uint32(len(data)))
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	var rec Record
	if err := json.Unmarshal(readResult(size), &rec); err != nil {
		return nil, &hostError{msg: "decoding record: " + err.Error()}
	}
	return &rec, nil
}

// DeleteRecord removes a table row by ID.
func DeleteRecord(tableKey, id string) error {
	tPtr, tLen := strPtr(tableKey)
	iPtr, iLen := strPtr(id)
	ok := _tableDelete(tPtr, tLen, iPtr, iLen)
	if ok == 0 {
		return &hostError{msg: readLastError()}
	}
	return nil
}

// QueryRecords returns rows matching the supplied options.
func QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error) {
	data, err := json.Marshal(opts)
	if err != nil {
		return nil, &hostError{msg: "encoding query options: " + err.Error()}
	}
	tPtr, tLen := strPtr(tableKey)
	oPtr := bufPtr(data)
	size := _tableQuery(tPtr, tLen, oPtr, uint32(len(data)))
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	var records []*Record
	if err := json.Unmarshal(readResult(size), &records); err != nil {
		return nil, &hostError{msg: "decoding records: " + err.Error()}
	}
	return records, nil
}
`

// FnruntimeHostGo is the non-WASM stub file that enables unit testing.
const FnruntimeHostGo = `//go:build !wasip1

package fnruntime

// FetchRequest describes an outbound HTTP request executed by the host.
type FetchRequest struct {
	Method  string            ` + "`" + `json:"method"` + "`" + `
	URL     string            ` + "`" + `json:"url"` + "`" + `
	Headers map[string]string ` + "`" + `json:"headers,omitempty"` + "`" + `
	Body    []byte            ` + "`" + `json:"body,omitempty"` + "`" + `
}

// FetchResponse contains the HTTP response returned by the host.
type FetchResponse struct {
	Status  int               ` + "`" + `json:"status"` + "`" + `
	Headers map[string]string ` + "`" + `json:"headers"` + "`" + `
	Body    []byte            ` + "`" + `json:"body"` + "`" + `
}

// Record is a single table row returned from the host.
type Record struct {
	ID   string                 ` + "`" + `json:"_id"` + "`" + `
	Data map[string]interface{} ` + "`" + `json:"data"` + "`" + `
}

// Filter is a single predicate for QueryRecords.
type Filter struct {
	Column   string ` + "`" + `json:"column"` + "`" + `
	Operator string ` + "`" + `json:"operator"` + "`" + `
	Value    string ` + "`" + `json:"value"` + "`" + `
}

// QueryOptions controls filtering and pagination for QueryRecords.
type QueryOptions struct {
	Filters []Filter ` + "`" + `json:"filters,omitempty"` + "`" + `
	Limit   int      ` + "`" + `json:"limit,omitempty"` + "`" + `
	Offset  int      ` + "`" + `json:"offset,omitempty"` + "`" + `
}

type hostError struct{ msg string }

func (e *hostError) Error() string { return e.msg }

// Backend is the interface implemented by MockRuntime and any custom test double.
type Backend interface {
	GetObject(bucket, key string) ([]byte, error)
	PutObject(bucket, key string, data []byte) error
	DeleteObject(bucket, key string) error
	ListObjects(bucket string) ([]string, error)
	EvaluateFlag(key string) bool
	InvokeFunction(id string) ([]byte, error)
	Fetch(req FetchRequest) (*FetchResponse, error)
	GetRecord(tableKey, id string) (*Record, error)
	PutRecord(tableKey string, record map[string]interface{}) (*Record, error)
	DeleteRecord(tableKey, id string) error
	QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error)
}

var activeBackend Backend

// SetMockRuntime sets the Backend for all fnruntime calls during tests.
func SetMockRuntime(b Backend) { activeBackend = b }

func require() Backend {
	if activeBackend == nil {
		panic("fnruntime: no mock runtime — call fnruntime.SetMockRuntime(fnruntime.NewMockRuntime()) before invoking the function")
	}
	return activeBackend
}

func GetObject(bucket, key string) ([]byte, error)   { return require().GetObject(bucket, key) }
func PutObject(bucket, key string, data []byte) error { return require().PutObject(bucket, key, data) }
func DeleteObject(bucket, key string) error           { return require().DeleteObject(bucket, key) }
func ListObjects(bucket string) ([]string, error)     { return require().ListObjects(bucket) }
func EvaluateFlag(key string) bool                    { return require().EvaluateFlag(key) }
func InvokeFunction(id string) ([]byte, error)        { return require().InvokeFunction(id) }
func Fetch(req FetchRequest) (*FetchResponse, error)  { return require().Fetch(req) }

func GetRecord(tableKey, id string) (*Record, error) { return require().GetRecord(tableKey, id) }
func PutRecord(tableKey string, record map[string]interface{}) (*Record, error) {
	return require().PutRecord(tableKey, record)
}
func DeleteRecord(tableKey, id string) error { return require().DeleteRecord(tableKey, id) }
func QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error) {
	return require().QueryRecords(tableKey, opts)
}
`

// FnruntimeMockGo is the MockRuntime source file included in scaffolds.
const FnruntimeMockGo = `//go:build !wasip1

package fnruntime

import (
	"fmt"
	"sync"
)

// MockRuntime is an in-memory Backend for unit-testing Flagbase functions
// locally without a running server or WASM compilation.
//
// Typical test pattern:
//
//	func TestMyFunction(t *testing.T) {
//	    rt := fnruntime.NewMockRuntime()
//	    rt.PutObjectInBucket("cfg", "settings.json", []byte(` + "`" + `{"limit":10}` + "`" + `))
//	    rt.SetFlag("new-checkout", true)
//	    fnruntime.SetMockRuntime(rt)
//
//	    main() // invoke your function
//
//	    rows := rt.RecordsInTable("orders")
//	    if len(rows) != 1 { t.Fatalf("expected 1 order") }
//	}
type MockRuntime struct {
	mu      sync.Mutex
	buckets map[string]map[string][]byte
	flags   map[string]bool
	tables  map[string][]map[string]interface{}
	fetcher func(FetchRequest) (*FetchResponse, error)
	invoker func(id string) ([]byte, error)
	seq     int
}

// NewMockRuntime returns an empty MockRuntime ready for test seeding.
func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		buckets: make(map[string]map[string][]byte),
		flags:   make(map[string]bool),
		tables:  make(map[string][]map[string]interface{}),
	}
}

// PutObjectInBucket seeds data into a mock bucket before running the function.
func (m *MockRuntime) PutObjectInBucket(bucket, key string, data []byte) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buckets[bucket] == nil {
		m.buckets[bucket] = make(map[string][]byte)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.buckets[bucket][key] = cp
	return m
}

// SetFlag seeds a feature flag value that EvaluateFlag will return.
func (m *MockRuntime) SetFlag(key string, value bool) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flags[key] = value
	return m
}

// SeedRecord inserts a row with the given ID directly into the mock table store.
func (m *MockRuntime) SeedRecord(tableKey, id string, data map[string]interface{}) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := make(map[string]interface{}, len(data)+1)
	for k, v := range data {
		row[k] = v
	}
	row["_id"] = id
	m.tables[tableKey] = append(m.tables[tableKey], row)
	return m
}

// SetFetcher installs a custom handler for all Fetch calls the function makes.
func (m *MockRuntime) SetFetcher(fn func(FetchRequest) (*FetchResponse, error)) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetcher = fn
	return m
}

// SetInvoker installs a custom handler for all InvokeFunction calls.
func (m *MockRuntime) SetInvoker(fn func(id string) ([]byte, error)) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invoker = fn
	return m
}

// ObjectsInBucket returns copies of all objects in the named bucket.
func (m *MockRuntime) ObjectsInBucket(bucket string) map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(m.buckets[bucket]))
	for k, v := range m.buckets[bucket] {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// RecordsInTable returns copies of all rows in the named table.
func (m *MockRuntime) RecordsInTable(tableKey string) []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, len(m.tables[tableKey]))
	for i, row := range m.tables[tableKey] {
		cp := make(map[string]interface{}, len(row))
		for k, v := range row {
			cp[k] = v
		}
		out[i] = cp
	}
	return out
}

func (m *MockRuntime) GetObject(bucket, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		return nil, &hostError{fmt.Sprintf("bucket %q not found", bucket)}
	}
	data, ok := b[key]
	if !ok {
		return nil, &hostError{fmt.Sprintf("key %q not found in bucket %q", key, bucket)}
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MockRuntime) PutObject(bucket, key string, data []byte) error {
	m.PutObjectInBucket(bucket, key, data)
	return nil
}

func (m *MockRuntime) DeleteObject(bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.buckets[bucket]; ok {
		delete(b, key)
	}
	return nil
}

func (m *MockRuntime) ListObjects(bucket string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.buckets[bucket]))
	for k := range m.buckets[bucket] {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *MockRuntime) EvaluateFlag(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flags[key]
}

func (m *MockRuntime) InvokeFunction(id string) ([]byte, error) {
	m.mu.Lock()
	fn := m.invoker
	m.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return nil, &hostError{fmt.Sprintf("no invoker registered for %q; call SetInvoker()", id)}
}

func (m *MockRuntime) Fetch(req FetchRequest) (*FetchResponse, error) {
	m.mu.Lock()
	fn := m.fetcher
	m.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return nil, &hostError{"no fetcher registered; call SetFetcher() to mock HTTP calls"}
}

func (m *MockRuntime) GetRecord(tableKey, id string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.tables[tableKey] {
		if row["_id"] == id {
			cp := make(map[string]interface{}, len(row)-1)
			for k, v := range row {
				if k != "_id" {
					cp[k] = v
				}
			}
			return &Record{ID: id, Data: cp}, nil
		}
	}
	return nil, nil
}

func (m *MockRuntime) PutRecord(tableKey string, record map[string]interface{}) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existingID, ok := record["_id"].(string); ok && existingID != "" {
		for i, row := range m.tables[tableKey] {
			if row["_id"] == existingID {
				for k, v := range record {
					if k != "_id" {
						m.tables[tableKey][i][k] = v
					}
				}
				cp := make(map[string]interface{}, len(m.tables[tableKey][i])-1)
				for k, v := range m.tables[tableKey][i] {
					if k != "_id" {
						cp[k] = v
					}
				}
				return &Record{ID: existingID, Data: cp}, nil
			}
		}
		return nil, &hostError{fmt.Sprintf("record %q not found in table %q", existingID, tableKey)}
	}
	m.seq++
	id := fmt.Sprintf("mock-%d", m.seq)
	row := make(map[string]interface{}, len(record)+1)
	for k, v := range record {
		if k != "_id" {
			row[k] = v
		}
	}
	row["_id"] = id
	m.tables[tableKey] = append(m.tables[tableKey], row)
	data := make(map[string]interface{}, len(row)-1)
	for k, v := range row {
		if k != "_id" {
			data[k] = v
		}
	}
	return &Record{ID: id, Data: data}, nil
}

func (m *MockRuntime) DeleteRecord(tableKey, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, row := range m.tables[tableKey] {
		if row["_id"] == id {
			m.tables[tableKey] = append(m.tables[tableKey][:i], m.tables[tableKey][i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *MockRuntime) QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Record
	for _, row := range m.tables[tableKey] {
		id, _ := row["_id"].(string)
		data := make(map[string]interface{}, len(row)-1)
		for k, v := range row {
			if k != "_id" {
				data[k] = v
			}
		}
		out = append(out, &Record{ID: id, Data: data})
	}
	return out, nil
}
`

// BuildSh is the WASM build script included in all scaffolds.
const BuildSh = `#!/bin/sh
set -e
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o function.wasm .
echo "Built function.wasm ($(wc -c < function.wasm) bytes)"
`

// MainGo returns a starter main.go for a function project.
func MainGo(name, safeName string) []byte {
	return []byte(fmt.Sprintf(`package main

import (
	"encoding/json"
	"fmt"
	"log"

	"flagbase-fn-%s/fnruntime"
)

// main is the entry point for your Flagbase function.
// Write to stdout — the output is captured and returned to the caller.
func main() {
	// ── Feature flag ──────────────────────────────────────────────────────────
	if fnruntime.EvaluateFlag("my-feature") {
		fmt.Println("my-feature is enabled for this invocation")
	}

	// ── Bucket storage ────────────────────────────────────────────────────────
	// Read an object from bucket storage.
	raw, err := fnruntime.GetObject("my-bucket", "config.json")
	if err != nil {
		log.Printf("config not found: %%v", err)
	} else {
		var cfg map[string]interface{}
		if err := json.Unmarshal(raw, &cfg); err == nil {
			fmt.Printf("config: %%v\n", cfg)
		}
	}

	// ── Table storage ─────────────────────────────────────────────────────────
	// Insert a record. The host assigns an _id and returns the saved row.
	rec, err := fnruntime.PutRecord("events", map[string]interface{}{
		"source": "%s",
		"action": "invoked",
	})
	if err != nil {
		log.Fatalf("insert failed: %%v", err)
	}
	fmt.Printf("created event %%s\n", rec.ID)

	// Read the record back by ID.
	saved, err := fnruntime.GetRecord("events", rec.ID)
	if err != nil {
		log.Printf("get record failed: %%v", err)
	} else if saved != nil {
		fmt.Printf("event action: %%s\n", saved.Data["action"])
	}

	// Query the table.
	rows, err := fnruntime.QueryRecords("events", fnruntime.QueryOptions{Limit: 10})
	if err != nil {
		log.Fatalf("query failed: %%v", err)
	}
	fmt.Printf("total events: %%d\n", len(rows))
}
`, safeName, name))
}

// GoMod returns a go.mod for a function project.
func GoMod(name string) []byte {
	safeName := SafeName(name)
	return []byte(fmt.Sprintf("module flagbase-fn-%s\n\ngo 1.22\n", safeName))
}

// Config returns the .flagbase.json for a function project.
func Config(serverURL, functionID, name string) []byte {
	type cfg struct {
		Server     string `json:"server"`
		FunctionID string `json:"function_id,omitempty"`
		Name       string `json:"name"`
	}
	b, _ := json.MarshalIndent(cfg{Server: serverURL, FunctionID: functionID, Name: name}, "", "  ")
	return append(b, '\n')
}

// TestMainGo returns a starter main_test.go for a function project.
func TestMainGo(safeName string) []byte {
	return []byte(fmt.Sprintf(`//go:build !wasip1

package main

import (
	"encoding/json"
	"testing"

	"flagbase-fn-%s/fnruntime"
)

// TestFunction demonstrates how to unit-test your function using MockRuntime.
// Run with: go test ./...
//
// The mock backend runs entirely in-process — no server or WASM compilation
// needed. Seed data before calling main(), then inspect side effects on rt.
func TestFunction(t *testing.T) {
	rt := fnruntime.NewMockRuntime()

	// Seed a feature flag.
	rt.SetFlag("my-feature", true)

	// Seed a bucket object that GetObject will read.
	cfg, _ := json.Marshal(map[string]interface{}{"mode": "test"})
	rt.PutObjectInBucket("my-bucket", "config.json", cfg)

	// Seed an existing table row.
	rt.SeedRecord("events", "existing-1", map[string]interface{}{
		"source": "seed",
		"action": "setup",
	})

	// Install the mock and invoke the function.
	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil) // reset after test

	main()

	// Assert table side effects: function inserts one new row.
	rows := rt.RecordsInTable("events")
	if len(rows) < 2 { // seeded row + inserted row
		t.Fatalf("expected at least 2 rows in 'events', got %%d", len(rows))
	}
	t.Logf("table 'events' has %%d row(s)", len(rows))
}

// TestFunctionGetRecord shows how to test reading a record by ID.
func TestFunctionGetRecord(t *testing.T) {
	rt := fnruntime.NewMockRuntime()

	// Seed a specific record and verify it can be retrieved.
	rt.SeedRecord("events", "evt-42", map[string]interface{}{
		"source": "test",
		"action": "seeded",
	})

	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	rec, err := fnruntime.GetRecord("events", "evt-42")
	if err != nil {
		t.Fatalf("GetRecord: %%v", err)
	}
	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.Data["action"] != "seeded" {
		t.Fatalf("action: want %%q, got %%v", "seeded", rec.Data["action"])
	}
}

// TestFunctionFlagDisabled shows how to test a branch where a flag is off.
func TestFunctionFlagDisabled(t *testing.T) {
	rt := fnruntime.NewMockRuntime()
	// "my-feature" defaults to false — no SetFlag call needed.
	fnruntime.SetMockRuntime(rt)
	defer fnruntime.SetMockRuntime(nil)

	main() // should not panic even without the flag enabled
}
`, safeName))
}

// SafeName converts an arbitrary function name to a safe Go module name component.
// Uppercase letters are lowercased; any character that is not a-z, 0-9, or '-'
// is replaced with '-'.
func SafeName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, name)
}
