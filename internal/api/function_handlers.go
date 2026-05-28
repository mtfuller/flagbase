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

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addZipEntry(zw, "main.go", scaffoldMainGo(fn.Name), 0o644)
	addZipEntry(zw, "go.mod", scaffoldGoMod(fn.Name), 0o644)
	addZipEntry(zw, ".flagbase.json", scaffoldConfig(serverURL, fn.ID, fn.Name), 0o644)
	addZipEntry(zw, "build.sh", []byte(scaffoldBuildSh), 0o755)

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

func scaffoldMainGo(name string) []byte {
	return []byte(fmt.Sprintf(`package main

import "fmt"

// main is the entry point for your Flagbase function.
// Write to stdout — the output is captured and returned to the caller.
func main() {
	fmt.Println("Hello from %s!")
}
`, name))
}

func scaffoldGoMod(name string) []byte {
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, name)
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
