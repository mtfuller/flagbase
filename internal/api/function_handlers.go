package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/function"
)

// FunctionHandlers handles function management: JS source upload, WASM binary upload, and invocation.
type FunctionHandlers struct {
	Functions *function.Store
}

func (h *FunctionHandlers) ListFunctions(w http.ResponseWriter, r *http.Request) {
	fns, err := h.Functions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fns)
}

// CreateFunction handles both JS source (application/json) and pre-compiled WASM (multipart/form-data).
func (h *FunctionHandlers) CreateFunction(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		h.uploadWASMFunction(w, r)
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Language    string `json:"language"`
		Source      string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Language == "" || req.Source == "" {
		writeError(w, http.StatusBadRequest, "name, language and source are required")
		return
	}

	fn, err := h.Functions.Create(r.Context(), req.Name, req.Description, req.Language, req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, fn)
}

// uploadWASMFunction handles multipart/form-data uploads of pre-compiled WASM binaries.
// Fields: name (text), description (text), artifact (file, .wasm binary).
func (h *FunctionHandlers) uploadWASMFunction(w http.ResponseWriter, r *http.Request) {
	const maxUploadSize = 32 << 20 // 32 MB
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse multipart form")
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	file, _, err := r.FormFile("artifact")
	if err != nil {
		writeError(w, http.StatusBadRequest, "artifact file is required (field name: artifact)")
		return
	}
	defer file.Close()

	wasmBytes, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading artifact")
		return
	}

	fn, err := h.Functions.Upload(r.Context(), name, description, wasmBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, fn)
}

func (h *FunctionHandlers) GetFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	fn, err := h.Functions.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		writeError(w, http.StatusNotFound, "function not found")
		return
	}
	writeJSON(w, http.StatusOK, fn)
}

func (h *FunctionHandlers) DeleteFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Functions.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FunctionHandlers) InvokeFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 30 {
		req.TimeoutSeconds = 5
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	output, err := h.Functions.Invoke(r.Context(), id, timeout)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": string(output)})
}

// GetFunctionScaffold returns a zip archive containing a starter Go project wired
// to this function. The developer edits main.go, runs `flagbase fn build`, then
// `flagbase fn deploy` to upload the compiled WASM.
func (h *FunctionHandlers) GetFunctionScaffold(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	fn, err := h.Functions.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if fn == nil {
		writeError(w, http.StatusNotFound, "function not found")
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	serverURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	safeName := safeModuleName(fn.Name)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addZipEntry(zw, "main.go", scaffoldMainGo(fn.Name, safeName), 0o644)
	addZipEntry(zw, "go.mod", scaffoldGoMod(fn.Name), 0o644)
	addZipEntry(zw, ".flagbase.json", scaffoldConfig(serverURL, fn.ID, fn.Name), 0o644)
	addZipEntry(zw, "build.sh", []byte(scaffoldBuildSh), 0o755)

	// Embed fnruntime source so the scaffold is self-contained (no external deps).
	addZipEntry(zw, "fnruntime/doc.go", []byte(fnruntimeDocGo), 0o644)
	addZipEntry(zw, "fnruntime/runtime_wasip1.go", []byte(fnruntimeRuntimeWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/bucket_wasip1.go", []byte(fnruntimeBucketWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/flags_wasip1.go", []byte(fnruntimeFlagsWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/invoke_wasip1.go", []byte(fnruntimeInvokeWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/fetch_wasip1.go", []byte(fnruntimeFetchWasip1Go), 0o644)

	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "building scaffold archive")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-scaffold.zip"`, id))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func addZipEntry(zw *zip.Writer, name string, data []byte, mode os.FileMode) {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.SetMode(mode)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
}

func safeModuleName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, name)
}

func scaffoldMainGo(name, safeName string) []byte {
	return []byte(fmt.Sprintf(`package main

import (
	"fmt"

	"flagbase-fn-%s/fnruntime"
)

// main is the entry point for your Flagbase function.
// Write to stdout — the output is captured and returned to the caller.
// The fnruntime package exposes bucket storage, flag evaluation, peer function
// invocation, and outbound HTTP via Flagbase host functions.
func main() {
	// Example: evaluate a feature flag.
	if fnruntime.EvaluateFlag("my-feature") {
		fmt.Println("my-feature is enabled")
	}

	// Example: read an object from bucket storage.
	data, err := fnruntime.GetObject("my-bucket", "config.json")
	if err == nil {
		fmt.Printf("config: %%s\n", data)
	}

	// Example: make an outbound HTTP request.
	resp, err := fnruntime.Fetch(fnruntime.FetchRequest{
		Method: "GET",
		URL:    "https://httpbin.org/get",
	})
	if err == nil {
		fmt.Printf("status: %%d\n", resp.Status)
	}

	fmt.Println("Hello from %s!")
}
`, safeName, name))
}

func scaffoldGoMod(name string) []byte {
	safeName := safeModuleName(name)
	return []byte(fmt.Sprintf("module flagbase-fn-%s\n\ngo 1.21\n", safeName))
}

func scaffoldConfig(serverURL, functionID, name string) []byte {
	type cfg struct {
		Server     string `json:"server"`
		FunctionID string `json:"function_id"`
		Name       string `json:"name"`
	}
	b, _ := json.MarshalIndent(cfg{Server: serverURL, FunctionID: functionID, Name: name}, "", "  ")
	return append(b, '\n')
}

const scaffoldBuildSh = `#!/bin/sh
set -e
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o function.wasm .
echo "Built function.wasm ($(wc -c < function.wasm) bytes)"
`

// fnruntime source files embedded in scaffold ZIPs so developers get a
// self-contained project with no external dependencies.

const fnruntimeDocGo = `// Package fnruntime provides Go bindings for Flagbase host functions available
// to WASM functions compiled with GOOS=wasip1 GOARCH=wasm.
//
// Import this package in your function's main package to access bucket storage,
// feature flag evaluation, peer function invocation, and outbound HTTP.
//
// Build constraint note: all non-doc files carry //go:build wasip1 so the package
// compiles only when targeting the WASM runtime. Add the package as a local module
// replace directive or embed the source files directly (the scaffold ZIP includes
// fnruntime/ as a subdirectory so no external dependencies are required).
package fnruntime
`

const fnruntimeRuntimeWasip1Go = `//go:build wasip1

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
`

const fnruntimeBucketWasip1Go = `//go:build wasip1

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

// GetObject retrieves an object from the named bucket. Returns the object bytes
// or an error if the object does not exist or cannot be read.
func GetObject(bucket, key string) ([]byte, error) {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	size := _bucketGet(bPtr, bLen, kPtr, kLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	return readResult(size), nil
}

// PutObject stores data under key in the named bucket. Returns an error on
// failure.
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

// hostError is a simple error type for host function failures.
type hostError struct{ msg string }

func (e *hostError) Error() string { return e.msg }
`

const fnruntimeFlagsWasip1Go = `//go:build wasip1

package fnruntime

//go:wasmimport flagbase flag_eval
func _flagEval(keyPtr, keyLen uint32) uint32

// EvaluateFlag evaluates a feature flag by key for the current invocation
// context. Returns true when the flag is enabled, false otherwise (including
// on error, which is treated as false).
func EvaluateFlag(key string) bool {
	kPtr, kLen := strPtr(key)
	result := _flagEval(kPtr, kLen)
	return result == 1
}
`

const fnruntimeInvokeWasip1Go = `//go:build wasip1

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
`

const fnruntimeFetchWasip1Go = `//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase http_fetch
func _httpFetch(reqPtr, reqLen uint32) uint32

// FetchRequest describes an outbound HTTP request to be executed by the host.
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
`
