package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mtfuller/flagbase/internal/function"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/scaffold"
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
		TimeoutSeconds int    `json:"timeout_seconds"`
		FlagKey        string `json:"flag_key"`
		VariantKey     string `json:"variant_key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 30 {
		req.TimeoutSeconds = 5
	}

	ctx := r.Context()

	// Inject the caller's IAM identity so flag_eval, flag_eval_variant, and
	// get_caller_context host calls can evaluate flags in the user's context.
	if claims, ok := ctx.Value(iam.UserContextKey).(*iam.Claims); ok {
		ctx = function.WithCallerContext(ctx, function.CallerContext{
			UserID:   claims.UserID,
			Role:     claims.Role,
			TenantID: claims.TenantID,
			Email:    claims.Email,
			Groups:   claims.Groups,
		})
	}

	// Optionally run under a specific flag context so table writes are isolated.
	if req.FlagKey != "" {
		variantKey := req.VariantKey
		if variantKey == "" {
			variantKey = "true"
		}
		ctx = function.WithFlagContext(ctx, req.FlagKey+":"+variantKey)
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	output, err := h.Functions.Invoke(ctx, id, timeout)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": string(output)})
}

// InvokeFunctionStream streams the function's stdout in real-time using
// Server-Sent Events. The client reads data events with JSON payloads:
//
//	{"type":"stdout","data":"..."}   — a chunk of stdout output
//	{"type":"done"}                  — execution completed successfully
//	{"type":"error","message":"..."}  — execution failed
func (h *FunctionHandlers) InvokeFunctionStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	timeoutSecs := 5
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
			timeoutSecs = n
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Extend write deadline to accommodate the full function timeout plus buffer.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(time.Duration(timeoutSecs+10) * time.Second))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := &sseWriter{w: w, flusher: flusher}
	timeout := time.Duration(timeoutSecs) * time.Second
	err := h.Functions.InvokeStream(r.Context(), id, timeout, sw)

	var finalEvent []byte
	if err != nil {
		finalEvent, _ = json.Marshal(map[string]string{"type": "error", "message": err.Error()})
	} else {
		finalEvent, _ = json.Marshal(map[string]string{"type": "done"})
	}
	fmt.Fprintf(w, "data: %s\n\n", finalEvent)
	flusher.Flush()
}

// sseWriter wraps an http.ResponseWriter to emit SSE events for each Write call.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s *sseWriter) Write(p []byte) (int, error) {
	b, _ := json.Marshal(map[string]string{"type": "stdout", "data": string(p)})
	_, err := fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.flusher.Flush()
	return len(p), err
}

// ListFunctionInvocations returns the invocation history for a function.
func (h *FunctionHandlers) ListFunctionInvocations(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	invocations, err := h.Functions.ListInvocations(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invocations)
}

// ListFunctionVersions returns the deployment version history for a function.
func (h *FunctionHandlers) ListFunctionVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	versions, err := h.Functions.ListVersions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// DeployFunctionVersion accepts a new WASM artifact for an existing function,
// overwrites the active artifact, and records it as a new version.
func (h *FunctionHandlers) DeployFunctionVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	const maxUploadSize = 32 << 20
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse multipart form")
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

	version, err := h.Functions.DeployVersion(r.Context(), id, wasmBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, version)
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

	safeName := scaffold.SafeName(fn.Name)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addZipEntry(zw, "main.go", scaffold.MainGo(fn.Name, safeName), 0o644)
	addZipEntry(zw, "main_test.go", scaffold.TestMainGo(safeName), 0o644)
	addZipEntry(zw, "go.mod", scaffold.GoMod(fn.Name), 0o644)
	addZipEntry(zw, ".flagbase.json", scaffold.Config(serverURL, fn.ID, fn.Name), 0o644)
	addZipEntry(zw, "build.sh", []byte(scaffold.BuildSh), 0o755)

	// Embed fnruntime source so the scaffold is self-contained (no external deps).
	addZipEntry(zw, "fnruntime/doc.go", []byte(scaffold.FnruntimeDocGo), 0o644)
	addZipEntry(zw, "fnruntime/runtime_wasip1.go", []byte(scaffold.FnruntimeRuntimeWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/bucket_wasip1.go", []byte(scaffold.FnruntimeBucketWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/flags_wasip1.go", []byte(scaffold.FnruntimeFlagsWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/invoke_wasip1.go", []byte(scaffold.FnruntimeInvokeWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/fetch_wasip1.go", []byte(scaffold.FnruntimeFetchWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/table_wasip1.go", []byte(scaffold.FnruntimeTableWasip1Go), 0o644)
	addZipEntry(zw, "fnruntime/host.go", []byte(scaffold.FnruntimeHostGo), 0o644)
	addZipEntry(zw, "fnruntime/mock.go", []byte(scaffold.FnruntimeMockGo), 0o644)

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

// UpdateFunction recompiles a JavaScript function with updated source code.
func (h *FunctionHandlers) UpdateFunction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Source == "" {
		writeError(w, http.StatusBadRequest, "name and source are required")
		return
	}
	fn, err := h.Functions.Update(r.Context(), id, req.Name, req.Description, req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fn)
}

// GetFunctionVersion returns a single version record including its source snapshot.
func (h *FunctionHandlers) GetFunctionVersion(w http.ResponseWriter, r *http.Request) {
	fnID := chi.URLParam(r, "id")
	versionID := chi.URLParam(r, "versionId")
	v, err := h.Functions.GetVersion(r.Context(), fnID, versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// flagbaseSDKTypes is the TypeScript ambient declaration for the flagbase JS SDK.
// Served to Monaco Editor so IntelliSense works in-browser.
const flagbaseSDKTypes = `
/** flagbase host SDK — available in every JavaScript function. */
declare namespace flagbase {
  namespace flag {
    /**
     * Evaluate a feature flag and return true/false for the current caller.
     * @example
     * const enabled = flagbase.flag.evaluate('dark-mode');
     * if (enabled) { console.log('dark mode on'); }
     */
    function evaluate(key: string): boolean;
    /**
     * Evaluate a feature flag and return the matched variant key string.
     * @example
     * const variant = flagbase.flag.variant('checkout-flow');
     * console.log('variant:', variant); // e.g. "v2" or "control"
     */
    function variant(key: string): string;
  }
  namespace bucket {
    /**
     * Retrieve an object from storage; returns null if not found.
     * @example
     * const data = flagbase.bucket.get('my-bucket', 'config.json');
     * if (data) { const cfg = JSON.parse(data); }
     */
    function get(bucket: string, key: string): string | null;
    /**
     * Store a string value in a bucket. Returns true on success.
     * @example
     * flagbase.bucket.put('my-bucket', 'result.json', JSON.stringify({ ok: true }));
     */
    function put(bucket: string, key: string, data: string): boolean;
    /**
     * Delete an object from a bucket. Returns true on success.
     * @example
     * flagbase.bucket.delete('my-bucket', 'old-key');
     */
    function delete(bucket: string, key: string): boolean;
    /**
     * List all object keys in a bucket.
     * @example
     * const keys = flagbase.bucket.list('my-bucket');
     * keys.forEach(k => console.log(k));
     */
    function list(bucket: string): string[];
  }
  namespace table {
    /**
     * Retrieve a record by ID; returns null if not found.
     * @example
     * const user = flagbase.table.get('users', '123');
     * if (user) { console.log(user.email); }
     */
    function get(tableKey: string, id: string): Record<string, unknown> | null;
    /**
     * Insert or update a record; returns the saved record with its generated _id.
     * @example
     * const saved = flagbase.table.put('orders', { product: 'widget', qty: 2 });
     * console.log('saved with id', saved._id);
     */
    function put(tableKey: string, data: Record<string, unknown>): Record<string, unknown> | null;
    /**
     * Delete a record by ID. Returns true on success.
     * @example
     * flagbase.table.delete('orders', '456');
     */
    function delete(tableKey: string, id: string): boolean;
    /**
     * Query records with optional pagination.
     * @example
     * const rows = flagbase.table.query('orders', { limit: 10, offset: 0 });
     * rows.forEach(r => console.log(r));
     */
    function query(tableKey: string, opts?: {
      limit?: number;
      offset?: number;
    }): Record<string, unknown>[];
  }
  namespace fn {
    /**
     * Synchronously invoke another function by ID and return its stdout.
     * @example
     * const result = flagbase.fn.invoke('abc123def456');
     * console.log('child output:', result);
     */
    function invoke(id: string): string;
  }
  namespace http {
    interface FetchRequest {
      method: string;
      url: string;
      headers?: Record<string, string>;
      body?: string;
    }
    interface FetchResponse {
      status: number;
      headers: Record<string, string>;
      body: string;
    }
    /**
     * Make an outbound HTTP request with a 15-second timeout.
     * @example
     * const res = flagbase.http.fetch({
     *   method: 'POST',
     *   url: 'https://api.example.com/notify',
     *   headers: { 'Content-Type': 'application/json' },
     *   body: JSON.stringify({ event: 'deploy' }),
     * });
     * console.log('status:', res.status, 'body:', res.body);
     */
    function fetch(req: FetchRequest): FetchResponse;
  }
  namespace metrics {
    /**
     * Publish a custom numeric metric observable in the Monitoring dashboard.
     * @example
     * flagbase.metrics.publish('checkout.duration_ms', 142, { region: 'us-east' });
     */
    function publish(name: string, value: number, tags?: Record<string, string>): boolean;
  }
  namespace context {
    interface CallerContext {
      userId: string;
      role: string;
      tenantID: string;
      email: string;
      groups: string[];
    }
    /**
     * Return the IAM identity of the user or trigger that invoked this function.
     * @example
     * const caller = flagbase.context.caller();
     * console.log('invoked by', caller.email, 'role:', caller.role);
     */
    function caller(): CallerContext;
    /**
     * Return the raw event payload (UTF-8 string) passed to InvokeWithEvent.
     * @example
     * const payload = JSON.parse(flagbase.context.event() || '{}');
     * console.log('event type:', payload.type);
     */
    function event(): string;
    /**
     * Return the distributed trace ID for this invocation.
     * @example
     * console.log('trace:', flagbase.context.traceId());
     */
    function traceId(): string;
  }
}

/** Every JS function must export a handle() entry point. */
declare function handle(): void;

/** Standard console output — each log line appears in the invocation output. */
declare const console: {
  log(...args: unknown[]): void;
};

/**
 * Require an approved package from the Flagbase Package Registry.
 * Packages must be approved by an admin before they can be used.
 * @example
 * const dayjs = require('dayjs');
 * console.log(dayjs().format('YYYY-MM-DD'));
 */
declare function require(packageName: string): unknown;
`

// GetFlagbaseSDKTypes serves the TypeScript ambient declarations for the flagbase
// JS SDK so that Monaco Editor can provide IntelliSense in the browser.
// No authentication required — the file contains no secrets.
func (h *FunctionHandlers) GetFlagbaseSDKTypes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/typescript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(flagbaseSDKTypes))
}

// InvokeFunctionStreamWithEvent streams a function's stdout as SSE, injecting
// the POST body as the event payload readable via flagbase.context.event().
func (h *FunctionHandlers) InvokeFunctionStreamWithEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	timeoutSecs := 5
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
			timeoutSecs = n
		}
	}

	eventData, _ := io.ReadAll(r.Body)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(time.Duration(timeoutSecs+10) * time.Second))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	if claims, ok := ctx.Value(iam.UserContextKey).(*iam.Claims); ok {
		ctx = function.WithCallerContext(ctx, function.CallerContext{
			UserID:   claims.UserID,
			Role:     claims.Role,
			TenantID: claims.TenantID,
			Email:    claims.Email,
			Groups:   claims.Groups,
		})
	}

	sw := &sseWriter{w: w, flusher: flusher}
	timeout := time.Duration(timeoutSecs) * time.Second
	err := h.Functions.InvokeStreamWithEvent(ctx, id, timeout, eventData, sw)

	var finalEvent []byte
	if err != nil {
		finalEvent, _ = json.Marshal(map[string]string{"type": "error", "message": err.Error()})
	} else {
		finalEvent, _ = json.Marshal(map[string]string{"type": "done"})
	}
	fmt.Fprintf(w, "data: %s\n\n", finalEvent)
	flusher.Flush()
}
