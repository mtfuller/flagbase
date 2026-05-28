//go:build wasip1

package fnruntime

//go:wasmimport flagbase fn_invoke
func _fnInvoke(idPtr, idLen uint32) uint32

// InvokeFunction calls another Flagbase function by ID and returns its stdout
// output. Returns an error if the function cannot be found, is not ready, or
// its execution fails.
func InvokeFunction(id string) ([]byte, error) {
	iPtr, iLen := strPtr(id)
	size := _fnInvoke(iPtr, iLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	return readResult(size), nil
}
