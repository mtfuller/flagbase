//go:build !wasip1

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
//	    rt.PutObjectInBucket("cfg", "settings.json", []byte(`{"limit":10}`))
//	    rt.SetFlag("new-checkout", true)
//	    fnruntime.SetMockRuntime(rt)
//
//	    main() // invoke your function
//
//	    rows := rt.RecordsInTable("orders")
//	    if len(rows) != 1 { t.Fatalf("expected 1 order") }
//	    metrics := rt.PublishedMetrics()
//	    // assert metrics…
//	}
type MockRuntime struct {
	mu             sync.Mutex
	buckets        map[string]map[string][]byte
	flags          map[string]bool
	tables         map[string][]map[string]interface{}
	fetcher        func(FetchRequest) (*FetchResponse, error)
	invoker        func(id string) ([]byte, error)
	seq            int
	traceID        string
	publishedMetrics []MockMetric
}

// MockMetric is a metric captured during a mock function execution.
type MockMetric struct {
	Name  string
	Value float64
	Tags  map[string]string
}

// NewMockRuntime returns an empty MockRuntime ready for test seeding.
func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		buckets: make(map[string]map[string][]byte),
		flags:   make(map[string]bool),
		tables:  make(map[string][]map[string]interface{}),
	}
}

// SetTraceID sets the trace ID returned by GetTraceID calls during test execution.
func (m *MockRuntime) SetTraceID(id string) *MockRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traceID = id
	return m
}

// PublishedMetrics returns a copy of all metrics recorded via PublishMetric during the test.
func (m *MockRuntime) PublishedMetrics() []MockMetric {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MockMetric, len(m.publishedMetrics))
	copy(out, m.publishedMetrics)
	return out
}

// ---------- seed / setup helpers (chainable) ----------

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

// SeedRecord inserts a row with the given ID directly into the mock table store,
// bypassing the normal PutRecord upsert logic. Use this to pre-populate read
// fixtures; the "_id" key in data is ignored — id is used instead.
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
// Return a FetchResponse and nil error to simulate a successful HTTP response.
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

// ---------- observation helpers ----------

// ObjectsInBucket returns copies of all objects currently stored in a bucket.
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

// RecordsInTable returns copies of all rows currently stored in a table,
// including the "_id" field inside the map.
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

// ---------- Backend interface ----------

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
			cp := make(map[string]interface{}, len(row))
			for k, v := range row {
				cp[k] = v
			}
			delete(cp, "_id")
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
				cp := make(map[string]interface{}, len(m.tables[tableKey][i]))
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

func (m *MockRuntime) PublishMetric(name string, value float64, tags map[string]string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make(map[string]string, len(tags))
	for k, v := range tags {
		cp[k] = v
	}
	m.publishedMetrics = append(m.publishedMetrics, MockMetric{Name: name, Value: value, Tags: cp})
	return true
}

func (m *MockRuntime) GetTraceID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.traceID
}
