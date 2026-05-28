package function_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mtfuller/flagbase/internal/compiler"
	"github.com/mtfuller/flagbase/internal/function"
)

func compileGo(t *testing.T, src string) []byte {
	t.Helper()
	c := compiler.NewGoCompiler()
	result, err := c.Compile(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return result.Artifact
}

func TestEngine_InvokeWASI_HelloWorld(t *testing.T) {
	ctx := context.Background()
	eng := function.NewEngine(ctx)
	defer eng.Close(ctx)

	wasm := compileGo(t, `package main
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

	wasm := compileGo(t, `package main
func main() { for {} }`)

	_, err := eng.InvokeWASI(ctx, wasm, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
