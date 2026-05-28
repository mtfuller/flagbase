package function_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mtfuller/flagbase/internal/function"
)

// buildWASM compiles Go source to a WASI preview1 WASM binary using the local
// Go toolchain. This mirrors what `flagbase fn build` does on the developer's
// machine before uploading to a server that no longer has Go installed.
func buildWASM(t *testing.T, src string) []byte {
	t.Helper()

	dir, err := os.MkdirTemp("", "flagbase-wasm-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	modContent := "module flagbase-fn-test\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}

	code := strings.TrimSpace(src)
	if !strings.HasPrefix(code, "package") {
		code = "package main\n\n" + code
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600); err != nil {
		t.Fatalf("writing main.go: %v", err)
	}

	outPath := filepath.Join(dir, "fn.wasm")
	cmd := exec.Command("go", "build", "-o", outPath, ".") //nolint:gosec
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading wasm: %v", err)
	}
	return b
}

func TestEngine_InvokeWASI_HelloWorld(t *testing.T) {
	ctx := context.Background()
	eng := function.NewEngine(ctx)
	defer eng.Close(ctx)

	wasm := buildWASM(t, `package main
import "fmt"
func main() { fmt.Print("hello wasm") }`)

	out, err := eng.InvokeWASI(ctx, wasm, 10*time.Second)
	if err != nil {
		t.Fatalf("invoke error: %v", err)
	}
	if !strings.Contains(string(out), "hello wasm") {
		t.Errorf("expected 'hello wasm' in output, got: %q", out)
	}
}

func TestEngine_InvokeWASI_Timeout(t *testing.T) {
	ctx := context.Background()
	eng := function.NewEngine(ctx)
	defer eng.Close(ctx)

	wasm := buildWASM(t, `package main
func main() { for {} }`)

	_, err := eng.InvokeWASI(ctx, wasm, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestStore_UploadAndInvokeWASM(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	wasm := buildWASM(t, fmt.Sprintf(`package main
import "fmt"
func main() { fmt.Print(%q) }`, "hello upload"))

	fn, err := st.Upload(ctx, "hello-upload", "test upload", wasm)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if fn.Status != "ready" {
		t.Fatalf("expected status ready, got %s: %s", fn.Status, fn.Error)
	}

	out, err := st.Invoke(ctx, fn.ID, 10*time.Second)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(string(out), "hello upload") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestStore_UploadRejectsInvalidWASM(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	_, err := st.Upload(ctx, "bad", "", []byte("not wasm at all"))
	if err == nil {
		t.Fatal("expected error for invalid WASM bytes")
	}
}

func TestStore_CreateGoRejected(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	_, err := st.Create(ctx, "go-fn", "", "go", `package main
func main() {}`)
	if err == nil {
		t.Fatal("expected error: Go source cannot be compiled server-side")
	}
}
