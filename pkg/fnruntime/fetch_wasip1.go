//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase http_fetch
func _httpFetch(reqPtr, reqLen uint32) uint32

// FetchRequest describes an outbound HTTP request to be executed by the host.
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

// Fetch performs an outbound HTTP request through the Flagbase host runtime.
// The host enforces a 15-second timeout on the underlying request.
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
