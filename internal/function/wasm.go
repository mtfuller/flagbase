package function

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// WASIMetrics holds performance counters captured during a single WASM invocation.
type WASIMetrics struct {
	PeakMemoryBytes uint32
	HostCalls       int
}

// Engine wraps a Wazero runtime for executing sandboxed WASM functions.
// Each Invoke call instantiates a fresh module with a hard execution deadline,
// preventing runaway functions from starving shared CPU resources.
type Engine struct {
	runtime  wazero.Runtime
	wasiOnce sync.Once
	wasiErr  error
	hostOnce sync.Once
	hostErr  error
}

// NewEngine creates an Engine. The caller must call Close when done.
// WithCloseOnContextDone enables hard interruption of tight WASM loops
// when the execution context deadline is exceeded.
func NewEngine(ctx context.Context) *Engine {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	return &Engine{runtime: wazero.NewRuntimeWithConfig(ctx, cfg)}
}

// Close releases all resources held by the runtime.
func (e *Engine) Close(ctx context.Context) {
	_ = e.runtime.Close(ctx)
}

// Invoke loads, instantiates, and calls the exported "handle" function in a WASM
// module at wasmPath. The execution is bounded by timeout to guard against
// infinite loops in user-supplied functions.
func (e *Engine) Invoke(ctx context.Context, wasmPath string, timeout time.Duration) ([]byte, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("reading wasm module: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mod, err := e.runtime.Instantiate(execCtx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("instantiating wasm module: %w", err)
	}
	defer mod.Close(ctx)

	handleFn := mod.ExportedFunction("handle")
	if handleFn == nil {
		return nil, fmt.Errorf("wasm module at %s does not export 'handle'", wasmPath)
	}

	results, err := handleFn.Call(execCtx)
	if err != nil {
		return nil, fmt.Errorf("wasm execution: %w", err)
	}

	// If the WASM function returns (ptr uint32, size uint32), read that slice
	// from linear memory. Functions with no return values produce no output bytes.
	if len(results) < 2 {
		return []byte{}, nil
	}
	ptr := uint32(results[0])
	size := uint32(results[1])
	if size == 0 {
		return []byte{}, nil
	}
	buf, ok := mod.Memory().Read(ptr, size)
	if !ok {
		return nil, fmt.Errorf("wasm: cannot read output at ptr=%d size=%d (memory size=%d)",
			ptr, size, mod.Memory().Size())
	}
	out := make([]byte, size)
	copy(out, buf)
	return out, nil
}

// InvokeWASI loads and runs a WASI preview1 WASM module. The module's main()
// is called automatically on instantiation; stdout is captured and returned.
// m is populated with compute metrics if non-nil.
func (e *Engine) InvokeWASI(ctx context.Context, wasmBytes []byte, timeout time.Duration, deps *HostDeps, m *WASIMetrics) ([]byte, error) {
	var stdout bytes.Buffer
	if err := e.invokeWASIWith(ctx, wasmBytes, timeout, deps, &stdout, m); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// InvokeWASIStream runs a WASI WASM module and writes stdout directly to w as
// bytes arrive, enabling real-time streaming to callers.
// m is populated with compute metrics if non-nil.
func (e *Engine) InvokeWASIStream(ctx context.Context, wasmBytes []byte, timeout time.Duration, deps *HostDeps, w io.Writer, m *WASIMetrics) error {
	return e.invokeWASIWith(ctx, wasmBytes, timeout, deps, w, m)
}

// invokeWASIWith is the shared implementation backing InvokeWASI and InvokeWASIStream.
func (e *Engine) invokeWASIWith(ctx context.Context, wasmBytes []byte, timeout time.Duration, deps *HostDeps, stdout io.Writer, m *WASIMetrics) error {
	// Instantiate the WASI host module once per runtime lifetime.
	e.wasiOnce.Do(func() {
		_, e.wasiErr = wasi_snapshot_preview1.NewBuilder(e.runtime).Instantiate(ctx)
	})
	if e.wasiErr != nil {
		return fmt.Errorf("initialising wasi: %w", e.wasiErr)
	}

	if deps != nil {
		e.hostOnce.Do(func() {
			e.hostErr = registerHostModule(ctx, e.runtime, *deps)
		})
		if e.hostErr != nil {
			return fmt.Errorf("initialising host module: %w", e.hostErr)
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	st := &invState{}
	// Wire span/metric callbacks when a tracer is available.
	if deps != nil && deps.Tracer != nil {
		traceID := deps.TraceID
		tracer := deps.Tracer
		st.recordSpan = func(eventType, status string, startedAt time.Time, durationMs int64, metadata map[string]interface{}) {
			st.spanSeq++
			_, _ = tracer.RecordSpan(traceID, "", eventType, status, startedAt, durationMs, st.spanSeq, metadata)
		}
	}
	if deps != nil && deps.DB != nil && deps.FnID != "" {
		db := deps.DB
		fnID := deps.FnID
		invID := deps.InvID
		traceID := deps.TraceID
		st.recordCustomMetric = func(name string, value float64, tags string) {
			id, _ := newID()
			_, _ = db.ExecContext(ctx, `
				INSERT INTO function_custom_metrics
					(id, trace_id, function_id, invocation_id, metric_name, metric_value, tags)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, traceID, fnID, invID, name, value, tags)
		}
	}
	execCtx = context.WithValue(execCtx, invStateKey{}, st)

	compiled, err := e.runtime.CompileModule(execCtx, wasmBytes)
	if err != nil {
		return fmt.Errorf("compiling wasm module: %w", err)
	}
	defer compiled.Close(ctx)

	cfg := wazero.NewModuleConfig().
		WithStdout(stdout).
		WithStderr(io.Discard).
		WithSysNanosleep().
		WithSysNanotime().
		WithSysWalltime().
		WithName(fmt.Sprintf("fn-%d", time.Now().UnixNano()))

	mod, err := e.runtime.InstantiateModule(execCtx, compiled, cfg)
	if mod != nil {
		// Capture peak memory before closing — memory can only grow in WASM so
		// the final size is the peak.
		if mem := mod.Memory(); mem != nil {
			st.peakMemoryBytes = mem.Size()
		}
		defer mod.Close(ctx)
	}

	// Populate caller-supplied metrics struct.
	if m != nil {
		m.PeakMemoryBytes = st.peakMemoryBytes
		m.HostCalls = st.hostCalls
	}

	// WASI programs signal clean exit via proc_exit(0), which wazero surfaces
	// as a *sys.ExitError with ExitCode() == 0. Treat that as success.
	if err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
			return nil
		}
		return fmt.Errorf("wasm execution: %w", err)
	}
	return nil
}

