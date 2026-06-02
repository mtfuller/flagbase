package function

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/mtfuller/flagbase/internal/compiler"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/storage"
	"github.com/mtfuller/flagbase/internal/table"
	"github.com/mtfuller/flagbase/internal/tracing"
)

const functionsBucket = "functions"

// eventPayloadKey is the context key for the event payload injected by InvokeWithEvent.
type eventPayloadKey struct{}

// InvokeWithEvent executes a function with an event payload readable via the event_read host call.
func (s *Store) InvokeWithEvent(ctx context.Context, id string, timeout time.Duration, eventData []byte) ([]byte, error) {
	ctx = context.WithValue(ctx, eventPayloadKey{}, eventData)
	return s.Invoke(ctx, id, timeout)
}

// Function is a stored, potentially compiled, user function.
type Function struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Language    string    `json:"language"`
	Source      string    `json:"source"`
	Runtime     string    `json:"runtime"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FunctionVersion records a single deployment of a function.
type FunctionVersion struct {
	ID         string    `json:"id"`
	FunctionID string    `json:"function_id"`
	Version    int       `json:"version"`
	Source     string    `json:"source,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// FunctionInvocation records a single execution of a function.
type FunctionInvocation struct {
	ID              string     `json:"id"`
	FunctionID      string     `json:"function_id"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Success         bool       `json:"success"`
	Output          string     `json:"output,omitempty"`
	Error           string     `json:"error,omitempty"`
	ExecutionMs     *int64     `json:"execution_ms,omitempty"`
	PeakMemoryBytes int64      `json:"peak_memory_bytes"`
	HostCalls       int        `json:"host_calls"`
	OutputSizeBytes int        `json:"output_size_bytes"`
	TraceID         string     `json:"trace_id,omitempty"`
}

// invMetrics holds compute metrics captured during a function invocation.
type invMetrics struct {
	executionMs     int64
	peakMemoryBytes uint32
	hostCalls       int
	outputSizeBytes int
}

// Store manages function lifecycle: persistence, compilation, and invocation.
type Store struct {
	db     *sql.DB
	store  *storage.LocalAdapter
	engine *Engine
	flags  *feature.Engine
	tables *table.Engine
	tracer *tracing.Recorder
}

func NewStore(db *sql.DB, store *storage.LocalAdapter, engine *Engine, flags *feature.Engine) *Store {
	return &Store{db: db, store: store, engine: engine, flags: flags}
}

// WithTables injects the table engine so WASM functions can call table host functions.
func (s *Store) WithTables(t *table.Engine) { s.tables = t }

// WithTracer injects the distributed tracing recorder.
func (s *Store) WithTracer(t *tracing.Recorder) { s.tracer = t }

// Create persists a new JavaScript function record and validates it synchronously.
// For Go/WASM functions, use Upload instead.
func (s *Store) Create(ctx context.Context, name, description, language, source string) (*Function, error) {
	c, err := compilerFor(language)
	if err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	fn := &Function{
		ID:          id,
		Name:        name,
		Description: description,
		Language:    language,
		Source:      source,
		Status:      "compiling",
	}

	if err := s.insert(ctx, fn); err != nil {
		return nil, fmt.Errorf("inserting function: %w", err)
	}

	result, compileErr := c.Compile(source)
	if compileErr != nil {
		fn.Status = "error"
		fn.Error = compileErr.Error()
		_ = s.updateStatus(ctx, id, "error", compileErr.Error(), "")
		return fn, nil
	}

	fn.Runtime = string(result.Runtime)
	objectName := id + artifactExt(result.Runtime)
	if err := s.store.PutObject(ctx, functionsBucket, objectName, bytes.NewReader(result.Artifact)); err != nil {
		fn.Status = "error"
		fn.Error = fmt.Sprintf("storing artifact: %v", err)
		_ = s.updateStatus(ctx, id, "error", fn.Error, "")
		return fn, nil
	}

	fn.Status = "ready"
	_ = s.updateStatus(ctx, id, "ready", "", string(result.Runtime))
	return fn, nil
}

// Upload persists a pre-compiled WASM artifact. The caller is responsible for
// compiling Go source to WASI preview1 WASM (e.g. via `flagbase fn build`).
func (s *Store) Upload(ctx context.Context, name, description string, wasmBytes []byte) (*Function, error) {
	if len(wasmBytes) < 4 || string(wasmBytes[:4]) != "\x00asm" {
		return nil, fmt.Errorf("invalid artifact: missing WASM magic number (\\x00asm)")
	}

	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	fn := &Function{
		ID:          id,
		Name:        name,
		Description: description,
		Language:    string(compiler.LanguageGo),
		Runtime:     string(compiler.RuntimeWASM),
		Status:      "ready",
	}

	if err := s.insert(ctx, fn); err != nil {
		return nil, fmt.Errorf("inserting function: %w", err)
	}

	if err := s.store.PutObject(ctx, functionsBucket, id+".wasm", bytes.NewReader(wasmBytes)); err != nil {
		fn.Status = "error"
		fn.Error = fmt.Sprintf("storing artifact: %v", err)
		_ = s.updateStatus(ctx, id, "error", fn.Error, "")
		return fn, nil
	}

	_ = s.updateStatus(ctx, id, "ready", "", string(compiler.RuntimeWASM))
	_, _ = s.createVersion(ctx, id)
	return fn, nil
}

// DeployVersion uploads a new WASM artifact for an existing function,
// overwrites the active artifact, and records a new version entry.
func (s *Store) DeployVersion(ctx context.Context, id string, wasmBytes []byte) (*FunctionVersion, error) {
	fn, err := s.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading function: %w", err)
	}
	if fn == nil {
		return nil, fmt.Errorf("function not found")
	}
	if len(wasmBytes) < 4 || string(wasmBytes[:4]) != "\x00asm" {
		return nil, fmt.Errorf("invalid artifact: missing WASM magic number (\\x00asm)")
	}

	if err := s.store.PutObject(ctx, functionsBucket, id+".wasm", bytes.NewReader(wasmBytes)); err != nil {
		return nil, fmt.Errorf("storing artifact: %w", err)
	}

	_, _ = s.db.ExecContext(ctx,
		`UPDATE functions SET status = 'ready', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)

	return s.createVersion(ctx, id)
}

// ListVersions returns all deployment versions for a function, oldest first.
func (s *Store) ListVersions(ctx context.Context, functionID string) ([]FunctionVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, function_id, version, source, created_at
		FROM function_versions WHERE function_id = ?
		ORDER BY version ASC`, functionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []FunctionVersion
	for rows.Next() {
		var v FunctionVersion
		if err := rows.Scan(&v.ID, &v.FunctionID, &v.Version, &v.Source, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []FunctionVersion{}
	}
	return versions, nil
}

// GetVersion returns a single version record by ID, including source snapshot.
func (s *Store) GetVersion(ctx context.Context, functionID, versionID string) (*FunctionVersion, error) {
	var v FunctionVersion
	err := s.db.QueryRowContext(ctx, `
		SELECT id, function_id, version, source, created_at
		FROM function_versions WHERE id = ? AND function_id = ?`,
		versionID, functionID).Scan(&v.ID, &v.FunctionID, &v.Version, &v.Source, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &v, err
}

// InvokeStream executes a ready function, writing its stdout output to w as
// bytes arrive. This enables real-time streaming to HTTP clients.
func (s *Store) InvokeStream(ctx context.Context, id string, timeout time.Duration, w io.Writer) error {
	fn, err := s.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("loading function: %w", err)
	}
	if fn == nil {
		return fmt.Errorf("function not found")
	}
	if fn.Status != "ready" {
		return fmt.Errorf("function is not ready (status: %s)", fn.Status)
	}

	invID, _ := s.startInvocation(ctx, fn.ID)
	var capBuf bytes.Buffer
	mw := io.MultiWriter(w, &capBuf)

	start := time.Now()
	var m WASIMetrics
	var invokeErr error
	switch compiler.Runtime(fn.Runtime) {
	case compiler.RuntimeWASM:
		invokeErr = s.invokeWASMStream(ctx, fn, invID, timeout, mw, &m)
	case compiler.RuntimeJS:
		var out []byte
		out, invokeErr = s.invokeJS(ctx, fn, timeout)
		if invokeErr == nil {
			_, invokeErr = w.Write(out)
			capBuf.Write(out) //nolint:errcheck
		}
	default:
		invokeErr = fmt.Errorf("unknown runtime: %s", fn.Runtime)
	}

	im := invMetrics{
		executionMs:     time.Since(start).Milliseconds(),
		peakMemoryBytes: m.PeakMemoryBytes,
		hostCalls:       m.HostCalls,
		outputSizeBytes: capBuf.Len(),
	}
	errMsg := ""
	if invokeErr != nil {
		errMsg = invokeErr.Error()
	}
	_ = s.completeInvocation(ctx, invID, capBuf.String(), errMsg, im)
	return invokeErr
}

func (s *Store) invokeWASMStream(ctx context.Context, fn *Function, invID string, timeout time.Duration, w io.Writer, m *WASIMetrics) error {
	rc, err := s.store.GetObject(ctx, functionsBucket, fn.ID+".wasm")
	if err != nil {
		return fmt.Errorf("loading wasm artifact: %w", err)
	}
	defer rc.Close()
	wasmBytes, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("reading wasm bytes: %w", err)
	}
	deps := s.buildHostDeps(ctx, fn.ID, invID)
	return s.engine.InvokeWASIStream(ctx, wasmBytes, timeout, deps, w, m)
}

// List returns all functions ordered by creation date descending.
func (s *Store) List(ctx context.Context) ([]Function, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, language, source, runtime, status, error, created_at, updated_at
		FROM functions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fns []Function
	for rows.Next() {
		var f Function
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.Language, &f.Source,
			&f.Runtime, &f.Status, &f.Error, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		fns = append(fns, f)
	}
	if fns == nil {
		fns = []Function{}
	}
	return fns, nil
}

// Get returns a single function by ID.
func (s *Store) Get(ctx context.Context, id string) (*Function, error) {
	var f Function
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, language, source, runtime, status, error, created_at, updated_at
		FROM functions WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.Description, &f.Language, &f.Source,
			&f.Runtime, &f.Status, &f.Error, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &f, err
}

// Delete removes a function and its stored artifact.
func (s *Store) Delete(ctx context.Context, id string) error {
	fn, err := s.Get(ctx, id)
	if err != nil || fn == nil {
		return err
	}
	ext := artifactExt(compiler.Runtime(fn.Runtime))
	_ = s.store.DeleteObject(ctx, functionsBucket, id+ext)
	_, err = s.db.ExecContext(ctx, `DELETE FROM functions WHERE id = ?`, id)
	return err
}

// Invoke executes a ready function and returns its output.
func (s *Store) Invoke(ctx context.Context, id string, timeout time.Duration) ([]byte, error) {
	fn, err := s.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading function: %w", err)
	}
	if fn == nil {
		return nil, fmt.Errorf("function not found")
	}
	if fn.Status != "ready" {
		return nil, fmt.Errorf("function is not ready (status: %s)", fn.Status)
	}

	invID, _ := s.startInvocation(ctx, fn.ID)

	start := time.Now()
	var m WASIMetrics
	var output []byte
	var invokeErr error
	switch compiler.Runtime(fn.Runtime) {
	case compiler.RuntimeWASM:
		output, invokeErr = s.invokeWASM(ctx, fn, invID, timeout, &m)
	case compiler.RuntimeJS:
		output, invokeErr = s.invokeJS(ctx, fn, timeout)
	default:
		invokeErr = fmt.Errorf("unknown runtime: %s", fn.Runtime)
	}

	im := invMetrics{
		executionMs:     time.Since(start).Milliseconds(),
		peakMemoryBytes: m.PeakMemoryBytes,
		hostCalls:       m.HostCalls,
		outputSizeBytes: len(output),
	}
	errMsg := ""
	if invokeErr != nil {
		errMsg = invokeErr.Error()
	}
	_ = s.completeInvocation(ctx, invID, string(output), errMsg, im)
	return output, invokeErr
}

// startInvocation inserts a pending invocation record, creates a linked trace,
// and returns the invocation ID.
func (s *Store) startInvocation(ctx context.Context, fnID string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	traceID := tracing.TraceIDFromCtx(ctx)
	// Create a trace record when one is tracked so trace_events can reference it.
	if traceID != "" && s.tracer != nil {
		_, _ = s.tracer.StartTrace(ctx, traceID, "http_invoke", fnID, "", "")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO function_invocations (id, function_id, trace_id) VALUES (?, ?, ?)`,
		id, fnID, traceID)
	return id, err
}

// completeInvocation updates the invocation record with outcome and compute metrics.
func (s *Store) completeInvocation(ctx context.Context, invID, output, errMsg string, m invMetrics) error {
	success := errMsg == ""
	// Record the function_invoke span in the trace.
	if m.executionMs > 0 && s.tracer != nil {
		traceID := tracing.TraceIDFromCtx(ctx)
		if traceID != "" {
			status := "success"
			if !success {
				status = "error"
			}
			_, _ = s.tracer.RecordSpan(
				traceID, "", "function_invoke", status,
				time.Now().Add(-time.Duration(m.executionMs)*time.Millisecond),
				m.executionMs, 0,
				map[string]interface{}{
					"invocation_id":    invID,
					"host_calls":       m.hostCalls,
					"peak_memory_bytes": m.peakMemoryBytes,
					"output_size_bytes": m.outputSizeBytes,
				},
			)
			s.tracer.CompleteTrace(traceID, status)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE function_invocations
		SET completed_at      = CURRENT_TIMESTAMP,
		    success           = ?,
		    output            = ?,
		    error             = ?,
		    execution_ms      = ?,
		    peak_memory_bytes = ?,
		    host_calls        = ?,
		    output_size_bytes = ?
		WHERE id = ?`,
		success, output, errMsg,
		m.executionMs, m.peakMemoryBytes, m.hostCalls, m.outputSizeBytes,
		invID)
	return err
}

// ListInvocations returns the 100 most recent invocations for a function.
func (s *Store) ListInvocations(ctx context.Context, fnID string) ([]FunctionInvocation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, function_id, started_at, completed_at, success, output, error,
		       execution_ms, peak_memory_bytes, host_calls, output_size_bytes, trace_id
		FROM function_invocations
		WHERE function_id = ?
		ORDER BY started_at DESC LIMIT 100`, fnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []FunctionInvocation
	for rows.Next() {
		var inv FunctionInvocation
		var completedAt sql.NullTime
		var errText string
		var execMs sql.NullInt64
		if err := rows.Scan(
			&inv.ID, &inv.FunctionID, &inv.StartedAt, &completedAt,
			&inv.Success, &inv.Output, &errText,
			&execMs, &inv.PeakMemoryBytes, &inv.HostCalls, &inv.OutputSizeBytes, &inv.TraceID,
		); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			inv.CompletedAt = &completedAt.Time
		}
		if errText != "" {
			inv.Error = errText
		}
		if execMs.Valid {
			inv.ExecutionMs = &execMs.Int64
		}
		invs = append(invs, inv)
	}
	if invs == nil {
		invs = []FunctionInvocation{}
	}
	return invs, nil
}

func (s *Store) invokeWASM(ctx context.Context, fn *Function, invID string, timeout time.Duration, m *WASIMetrics) ([]byte, error) {
	rc, err := s.store.GetObject(ctx, functionsBucket, fn.ID+".wasm")
	if err != nil {
		return nil, fmt.Errorf("loading wasm artifact: %w", err)
	}
	defer rc.Close()
	wasmBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading wasm bytes: %w", err)
	}
	deps := s.buildHostDeps(ctx, fn.ID, invID)
	return s.engine.InvokeWASI(ctx, wasmBytes, timeout, deps, m)
}

// buildHostDeps constructs the HostDeps for a function invocation, wiring all
// available services including the tracer and compute-metric callbacks.
func (s *Store) buildHostDeps(ctx context.Context, fnID, invID string) *HostDeps {
	return &HostDeps{
		Storage: s.store,
		Flags:   s.flags,
		Store:   s,
		Tables:  s.tables,
		Tracer:  s.tracer,
		DB:      s.db,
		FnID:    fnID,
		InvID:   invID,
		TraceID: tracing.TraceIDFromCtx(ctx),
	}
}

func (s *Store) invokeJS(ctx context.Context, fn *Function, timeout time.Duration) ([]byte, error) {
	vm := goja.New()

	var out strings.Builder
	_ = vm.Set("console", map[string]interface{}{
		"log": func(args ...interface{}) {
			for i, a := range args {
				if i > 0 {
					out.WriteByte(' ')
				}
				fmt.Fprintf(&out, "%v", a)
			}
			out.WriteByte('\n')
		},
	})

	s.buildJSHostObject(ctx, vm)

	done := make(chan error, 1)
	go func() {
		_, err := vm.RunString(fn.Source)
		if err != nil {
			done <- err
			return
		}
		handleFn, ok := goja.AssertFunction(vm.Get("handle"))
		if !ok {
			done <- fmt.Errorf("javascript function must export a 'handle' function")
			return
		}
		_, err = handleFn(goja.Undefined())
		done <- err
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("js execution: %w", err)
		}
	case <-timer.C:
		vm.Interrupt("execution timeout")
		return nil, fmt.Errorf("js execution timed out after %s", timeout)
	}

	return []byte(out.String()), nil
}

// buildJSHostObject injects the flagbase SDK namespace into a goja VM, giving
// JavaScript functions the same capabilities as WASM host functions.
func (s *Store) buildJSHostObject(ctx context.Context, vm *goja.Runtime) {
	flagObj := map[string]interface{}{
		"evaluate": func(key string) bool {
			if s.flags == nil {
				return false
			}
			return s.flags.EvaluateBool(key, callerEvalCtx(ctx))
		},
		"variant": func(key string) string {
			if s.flags == nil {
				return "false"
			}
			return s.flags.EvaluateVariant(key, callerEvalCtx(ctx))
		},
	}

	bucketObj := map[string]interface{}{
		"get": func(bucket, key string) interface{} {
			if s.store == nil {
				return nil
			}
			rc, err := s.store.GetObject(ctx, bucket, key)
			if err != nil {
				return nil
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return nil
			}
			return string(data)
		},
		"put": func(bucket, key, data string) bool {
			if s.store == nil {
				return false
			}
			return s.store.PutObject(ctx, bucket, key, strings.NewReader(data)) == nil
		},
		"delete": func(bucket, key string) bool {
			if s.store == nil {
				return false
			}
			return s.store.DeleteObject(ctx, bucket, key) == nil
		},
		"list": func(bucket string) []string {
			if s.store == nil {
				return []string{}
			}
			objs, err := s.store.ListObjects(ctx, bucket)
			if err != nil {
				return []string{}
			}
			return objs
		},
	}

	tableObj := map[string]interface{}{
		"get": func(tableKey, id string) interface{} {
			if s.tables == nil {
				return nil
			}
			rec, err := s.tables.GetRecord(tableKey, id)
			if err != nil || rec == nil {
				return nil
			}
			return rec
		},
		"put": func(tableKey string, data map[string]interface{}) interface{} {
			if s.tables == nil {
				return nil
			}
			flagCtx, _ := ctx.Value(flagCtxKey{}).(string)
			var rec *table.Record
			var err error
			if flagCtx != "" {
				rec, err = s.tables.InsertRecordFlagged(tableKey, data, flagCtx)
			} else {
				rec, err = s.tables.InsertRecord(tableKey, data)
			}
			if err != nil {
				return nil
			}
			return rec
		},
		"delete": func(tableKey, id string) bool {
			if s.tables == nil {
				return false
			}
			return s.tables.DeleteRecord(tableKey, id) == nil
		},
		"query": func(tableKey string, opts map[string]interface{}) interface{} {
			if s.tables == nil {
				return []interface{}{}
			}
			qo := table.QueryOptions{}
			if opts != nil {
				if v, ok := opts["limit"].(int64); ok {
					qo.Limit = int(v)
				}
				if v, ok := opts["offset"].(int64); ok {
					qo.Offset = int(v)
				}
			}
			recs, err := s.tables.QueryRecords(tableKey, qo)
			if err != nil {
				return []interface{}{}
			}
			return recs
		},
	}

	fnObj := map[string]interface{}{
		"invoke": func(id string) string {
			out, err := s.Invoke(ctx, id, 30*time.Second)
			if err != nil {
				return ""
			}
			return string(out)
		},
	}

	httpObj := map[string]interface{}{
		"fetch": func(reqObj map[string]interface{}) map[string]interface{} {
			method, _ := reqObj["method"].(string)
			url, _ := reqObj["url"].(string)
			bodyStr, _ := reqObj["body"].(string)
			if method == "" {
				method = "GET"
			}
			httpReq, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(bodyStr))
			if err != nil {
				return map[string]interface{}{"status": 0, "body": err.Error(), "headers": map[string]string{}}
			}
			if hdrs, ok := reqObj["headers"].(map[string]interface{}); ok {
				for k, v := range hdrs {
					if vs, ok := v.(string); ok {
						httpReq.Header.Set(k, vs)
					}
				}
			}
			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(httpReq)
			if err != nil {
				return map[string]interface{}{"status": 0, "body": err.Error(), "headers": map[string]string{}}
			}
			defer resp.Body.Close()
			respBytes, _ := io.ReadAll(resp.Body)
			hdrs := map[string]string{}
			for k, vs := range resp.Header {
				if len(vs) > 0 {
					hdrs[k] = vs[0]
				}
			}
			return map[string]interface{}{
				"status":  resp.StatusCode,
				"body":    string(respBytes),
				"headers": hdrs,
			}
		},
	}

	metricsObj := map[string]interface{}{
		"publish": func(name string, value float64, tags map[string]interface{}) bool {
			if s.db == nil {
				return false
			}
			tagsJSON, _ := json.Marshal(tags)
			_, err := s.db.ExecContext(ctx, `
				INSERT INTO function_custom_metrics (id, function_id, metric_name, metric_value, tags)
				VALUES (?, '', ?, ?, ?)`,
				fmt.Sprintf("%d", time.Now().UnixNano()), name, value, string(tagsJSON))
			return err == nil
		},
	}

	contextObj := map[string]interface{}{
		"caller": func() map[string]interface{} {
			cc, _ := ctx.Value(callerCtxKey{}).(CallerContext)
			return map[string]interface{}{
				"userId":   cc.UserID,
				"role":     cc.Role,
				"tenantID": cc.TenantID,
				"email":    cc.Email,
				"groups":   cc.Groups,
			}
		},
		"event": func() string {
			if data, ok := ctx.Value(eventPayloadKey{}).([]byte); ok {
				return string(data)
			}
			return ""
		},
		"traceId": func() string {
			return tracing.TraceIDFromCtx(ctx)
		},
	}

	_ = vm.Set("flagbase", map[string]interface{}{
		"flag":    flagObj,
		"bucket":  bucketObj,
		"table":   tableObj,
		"fn":      fnObj,
		"http":    httpObj,
		"metrics": metricsObj,
		"context": contextObj,
	})
}

// Update recompiles a JavaScript function from new source and stores a new version.
func (s *Store) Update(ctx context.Context, id, name, description, source string) (*Function, error) {
	fn, err := s.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading function: %w", err)
	}
	if fn == nil {
		return nil, fmt.Errorf("function not found")
	}
	if fn.Language != string(compiler.LanguageJavaScript) {
		return nil, fmt.Errorf("only javascript functions can be edited in the browser; use the CLI for WASM functions")
	}

	c, err := compilerFor(fn.Language)
	if err != nil {
		return nil, err
	}

	fn.Name = name
	fn.Description = description
	fn.Source = source

	_, err = s.db.ExecContext(ctx, `
		UPDATE functions SET name = ?, description = ?, source = ?, status = 'compiling', error = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, name, description, source, id)
	if err != nil {
		return nil, fmt.Errorf("updating function: %w", err)
	}

	result, compileErr := c.Compile(source)
	if compileErr != nil {
		fn.Status = "error"
		fn.Error = compileErr.Error()
		_ = s.updateStatus(ctx, id, "error", compileErr.Error(), "")
		return fn, nil
	}

	fn.Runtime = string(result.Runtime)
	objectName := id + artifactExt(result.Runtime)
	if err := s.store.PutObject(ctx, functionsBucket, objectName, bytes.NewReader(result.Artifact)); err != nil {
		fn.Status = "error"
		fn.Error = fmt.Sprintf("storing artifact: %v", err)
		_ = s.updateStatus(ctx, id, "error", fn.Error, "")
		return fn, nil
	}

	fn.Status = "ready"
	_ = s.updateStatus(ctx, id, "ready", "", string(result.Runtime))
	_, _ = s.createVersion(ctx, id)
	return fn, nil
}

// InvokeStreamWithEvent executes a function with an event payload, streaming
// stdout to w in real-time.
func (s *Store) InvokeStreamWithEvent(ctx context.Context, id string, timeout time.Duration, eventData []byte, w io.Writer) error {
	ctx = context.WithValue(ctx, eventPayloadKey{}, eventData)
	return s.InvokeStream(ctx, id, timeout, w)
}

func (s *Store) insert(ctx context.Context, fn *Function) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO functions (id, name, description, language, source, runtime, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fn.ID, fn.Name, fn.Description, fn.Language, fn.Source, fn.Runtime, fn.Status, fn.Error)
	return err
}

func (s *Store) updateStatus(ctx context.Context, id, status, errMsg, runtime string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE functions SET status = ?, error = ?, runtime = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		status, errMsg, runtime, id)
	return err
}

func compilerFor(language string) (compiler.Compiler, error) {
	switch compiler.Language(language) {
	case compiler.LanguageGo:
		return nil, fmt.Errorf("Go functions must be pre-compiled to WASM; use `flagbase fn build` then upload via the CLI or multipart API")
	case compiler.LanguageJavaScript:
		return compiler.NewJSCompiler(), nil
	default:
		return nil, fmt.Errorf("unsupported language: %s (supported: javascript)", language)
	}
}

func artifactExt(rt compiler.Runtime) string {
	if rt == compiler.RuntimeWASM {
		return ".wasm"
	}
	return ".js"
}

func (s *Store) createVersion(ctx context.Context, functionID string) (*FunctionVersion, error) {
	var maxVersion int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM function_versions WHERE function_id = ?`,
		functionID).Scan(&maxVersion)

	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generating version id: %w", err)
	}

	// Snapshot source at deploy time for diff view.
	var source string
	_ = s.db.QueryRowContext(ctx, `SELECT source FROM functions WHERE id = ?`, functionID).Scan(&source)

	v := &FunctionVersion{
		ID:         id,
		FunctionID: functionID,
		Version:    maxVersion + 1,
		Source:     source,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO function_versions (id, function_id, version, source) VALUES (?, ?, ?, ?)`,
		v.ID, v.FunctionID, v.Version, v.Source)
	if err != nil {
		return nil, fmt.Errorf("inserting version: %w", err)
	}
	// Fetch the created_at that was set by the DB default.
	_ = s.db.QueryRowContext(ctx,
		`SELECT created_at FROM function_versions WHERE id = ?`, v.ID).Scan(&v.CreatedAt)
	return v, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
