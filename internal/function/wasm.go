package function

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
)

// Engine wraps a Wazero runtime for executing sandboxed WASM functions.
// Each Invoke call instantiates a fresh module with a hard execution deadline,
// preventing runaway functions from starving shared CPU resources (single-node caveat).
type Engine struct {
	runtime wazero.Runtime
}

// NewEngine creates an Engine. The caller must call Close when done.
func NewEngine(ctx context.Context) *Engine {
	return &Engine{runtime: wazero.NewRuntime(ctx)}
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
