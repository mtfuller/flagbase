package function

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/mtfuller/flagbase/internal/compiler"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/storage"
)

const functionsBucket = "functions"

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

// Store manages function lifecycle: persistence, compilation, and invocation.
type Store struct {
	db     *sql.DB
	store  *storage.LocalAdapter
	engine *Engine
	flags  *feature.Engine
}

func NewStore(db *sql.DB, store *storage.LocalAdapter, engine *Engine, flags *feature.Engine) *Store {
	return &Store{db: db, store: store, engine: engine, flags: flags}
}

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
	return fn, nil
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

	switch compiler.Runtime(fn.Runtime) {
	case compiler.RuntimeWASM:
		return s.invokeWASM(ctx, fn, timeout)
	case compiler.RuntimeJS:
		return s.invokeJS(fn, timeout)
	default:
		return nil, fmt.Errorf("unknown runtime: %s", fn.Runtime)
	}
}

func (s *Store) invokeWASM(ctx context.Context, fn *Function, timeout time.Duration) ([]byte, error) {
	rc, err := s.store.GetObject(ctx, functionsBucket, fn.ID+".wasm")
	if err != nil {
		return nil, fmt.Errorf("loading wasm artifact: %w", err)
	}
	defer rc.Close()
	wasmBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading wasm bytes: %w", err)
	}
	deps := &HostDeps{
		Storage: s.store,
		Flags:   s.flags,
		Store:   s,
	}
	return s.engine.InvokeWASI(ctx, wasmBytes, timeout, deps)
}

func (s *Store) invokeJS(fn *Function, timeout time.Duration) ([]byte, error) {
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

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
