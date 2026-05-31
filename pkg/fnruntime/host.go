//go:build !wasip1

// This file provides non-WASM implementations of all fnruntime host functions.
// It is compiled for native targets (GOOS=linux, darwin, windows, …) so that
// function packages can be imported and unit-tested without a WASM toolchain.
// All calls are dispatched through the activeBackend variable, which must be set
// via SetMockRuntime before the function under test is invoked.

package fnruntime

// FetchRequest describes an outbound HTTP request executed by the host.
type FetchRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// FetchResponse contains the HTTP response returned by the host.
type FetchResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// Record is a single table row returned from the host.
type Record struct {
	ID   string                 `json:"_id"`
	Data map[string]interface{} `json:"data"`
}

// Filter is a single predicate for QueryRecords.
type Filter struct {
	Column   string `json:"column"`
	Operator string `json:"operator"` // equals | not_equals | contains | gt | gte | lt | lte
	Value    string `json:"value"`
}

// QueryOptions controls filtering and pagination for QueryRecords.
type QueryOptions struct {
	Filters []Filter `json:"filters,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Offset  int      `json:"offset,omitempty"`
}

// hostError is the error type returned by host function calls.
type hostError struct{ msg string }

func (e *hostError) Error() string { return e.msg }

// Backend is the interface implemented by MockRuntime and any custom test double.
// Set it with SetMockRuntime before calling the function under test.
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
	PublishMetric(name string, value float64, tags map[string]string) bool
	GetTraceID() string
}

var activeBackend Backend

// SetMockRuntime sets the Backend used by all fnruntime calls. Call this in
// each test before invoking main(). The change is global, so avoid t.Parallel()
// across tests that use different runtimes, or reset with SetMockRuntime(nil)
// in t.Cleanup.
func SetMockRuntime(b Backend) { activeBackend = b }

func require() Backend {
	if activeBackend == nil {
		panic("fnruntime: no mock runtime — call fnruntime.SetMockRuntime(fnruntime.NewMockRuntime()) before invoking the function")
	}
	return activeBackend
}

func GetObject(bucket, key string) ([]byte, error)  { return require().GetObject(bucket, key) }
func PutObject(bucket, key string, data []byte) error { return require().PutObject(bucket, key, data) }
func DeleteObject(bucket, key string) error          { return require().DeleteObject(bucket, key) }
func ListObjects(bucket string) ([]string, error)    { return require().ListObjects(bucket) }
func EvaluateFlag(key string) bool                   { return require().EvaluateFlag(key) }
func InvokeFunction(id string) ([]byte, error)       { return require().InvokeFunction(id) }
func Fetch(req FetchRequest) (*FetchResponse, error) { return require().Fetch(req) }

func GetRecord(tableKey, id string) (*Record, error) { return require().GetRecord(tableKey, id) }
func PutRecord(tableKey string, record map[string]interface{}) (*Record, error) {
	return require().PutRecord(tableKey, record)
}
func DeleteRecord(tableKey, id string) error { return require().DeleteRecord(tableKey, id) }
func QueryRecords(tableKey string, opts QueryOptions) ([]*Record, error) {
	return require().QueryRecords(tableKey, opts)
}
func PublishMetric(name string, value float64, tags map[string]string) bool {
	return require().PublishMetric(name, value, tags)
}
func GetTraceID() string { return require().GetTraceID() }
