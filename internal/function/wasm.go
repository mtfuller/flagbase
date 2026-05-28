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

// Engine wraps a Wazero runtime for executing sandboxed WASM functions.
// Each Invoke call instantiates a fresh module with a hard execution deadline,
// preventing runaway functions from starving shared CPU resources (single-node caveat).
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
	// from linear memory. Functions with no return values or a single return
	// value produce no output bytes.
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
	// Copy because buf is a view into the WASM linear memory which may be
	// reclaimed once the module is closed.
	out := make([]byte, size)
	copy(out, buf)
	return out, nil
}

// InvokeWASI loads and runs a WASI preview1 WASM module (e.g. compiled with
// GOOS=wasip1 GOARCH=wasm). The module's main() is called automatically on
// instantiation; stdout is captured and returned. Exit code 0 is success.
// If deps is non-nil, the "flagbase" host module is registered once and host
// functions (storage, flag evaluation, etc.) become available to WASM code.
func (e *Engine) InvokeWASI(ctx context.Context, wasmBytes []byte, timeout time.Duration, deps *HostDeps) ([]byte, error) {
	// Instantiate the WASI host module once per runtime lifetime.
	e.wasiOnce.Do(func() {
		_, e.wasiErr = wasi_snapshot_preview1.NewBuilder(e.runtime).Instantiate(ctx)
	})
	if e.wasiErr != nil {
		return nil, fmt.Errorf("initialising wasi: %w", e.wasiErr)
	}

	if deps != nil {
		e.hostOnce.Do(func() {
			e.hostErr = registerHostModule(ctx, e.runtime, *deps)
		})
		if e.hostErr != nil {
			return nil, fmt.Errorf("initialising host module: %w", e.hostErr)
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	st := &invState{}
	execCtx = context.WithValue(execCtx, invStateKey{}, st)

	compiled, err := e.runtime.CompileModule(execCtx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("compiling wasm module: %w", err)
	}
	defer compiled.Close(ctx)

	var stdout bytes.Buffer
	cfg := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(io.Discard).
		WithSysNanosleep().
		WithSysNanotime().
		WithSysWalltime()

	_, err = e.runtime.InstantiateModule(execCtx, compiled, cfg)
	// WASI programs signal clean exit via proc_exit(0), which wazero surfaces
	// as a *sys.ExitError with ExitCode() == 0. Treat that as success.
	if err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
			err = nil
		} else {
			return nil, fmt.Errorf("wasm execution: %w", err)
		}
	}

	return stdout.Bytes(), nil
}
