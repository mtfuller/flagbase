//go:build wasip1

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

// GetRecord retrieves a single table row by ID. Returns nil, nil when the row
// does not exist.
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

// PutRecord inserts or updates a table row. If record contains "_id", that row
// is updated; otherwise a new row is inserted and the returned Record carries
// its generated ID.
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

// QueryRecords returns rows matching the supplied options, ordered by creation
// time ascending.
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
