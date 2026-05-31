//go:build wasip1

package fnruntime

import (
	"encoding/json"
	"unsafe"
)

//go:wasmimport flagbase metrics_publish
func _metricsPublish(namePtr, nameLen uint32, value float64, tagsPtr, tagsLen uint32) uint32

//go:wasmimport flagbase get_trace_id
func _getTraceID(outPtr, outLen uint32) uint32

// PublishMetric records a named metric with a float64 value and optional tags.
// Tags are a flat map of string key→value pairs that appear in the admin console.
// Returns false if the host rejected the metric (empty name or host unavailable).
func PublishMetric(name string, value float64, tags map[string]string) bool {
	nPtr, nLen := strPtr(name)
	var tPtr, tLen uint32
	if len(tags) > 0 {
		tagsJSON, _ := json.Marshal(tags)
		tPtr = uint32(uintptr(unsafe.Pointer(unsafe.SliceData(tagsJSON))))
		tLen = uint32(len(tagsJSON))
	} else {
		// Pass the literal "{}" so the host always receives valid JSON.
		empty := []byte("{}")
		tPtr = uint32(uintptr(unsafe.Pointer(unsafe.SliceData(empty))))
		tLen = uint32(len(empty))
	}
	return _metricsPublish(nPtr, nLen, value, tPtr, tLen) != 0
}

// GetTraceID returns the current distributed trace ID injected by the host.
// The trace ID links this function execution to the originating HTTP request
// (or trigger) visible in the Traces page of the admin console.
func GetTraceID() string {
	var buf [64]byte
	n := _getTraceID(uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)))
	if n == 0 {
		return ""
	}
	return string(buf[:n])
}
