//go:build wasip1

package fnruntime

import "unsafe"

//go:wasmimport flagbase result_read
func _resultRead(outPtr, outLen uint32) uint32

//go:wasmimport flagbase error_read
func _errorRead(outPtr, outLen uint32) uint32

// errBuf is a package-level buffer used to read error messages from the host.
var errBuf [4096]byte

// readResult allocates a byte slice of the given size and copies the last host
// result into it via result_read.
func readResult(size uint32) []byte {
	buf := make([]byte, size)
	if size == 0 {
		return buf
	}
	_resultRead(bufPtr(buf), uint32(len(buf)))
	return buf
}

// readLastError reads the last host error message into errBuf and returns it as
// a string.
func readLastError() string {
	n := _errorRead(uint32(uintptr(unsafe.Pointer(&errBuf[0]))), uint32(len(errBuf)))
	if n == 0 {
		return "unknown error"
	}
	return string(errBuf[:n])
}

// strPtr returns a pointer and length for a Go string suitable for passing to
// host functions. Uses unsafe.StringData (Go 1.20+).
func strPtr(s string) (ptr, length uint32) {
	if len(s) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s)))), uint32(len(s))
}

// bufPtr returns a pointer to the first byte of a byte slice. Returns 0 if the
// slice is empty.
func bufPtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b))))
}
