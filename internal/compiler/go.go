package compiler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GoCompiler compiles Go source to a WASI preview1 WASM module using the
// system Go toolchain. The compiled module's main() function is the entry
// point; output written to stdout is captured on invocation.
type GoCompiler struct{}

func NewGoCompiler() *GoCompiler { return &GoCompiler{} }

func (c *GoCompiler) Compile(source string) (*Result, error) {
	dir, err := os.MkdirTemp("", "flagbase-fn-go-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Ensure the source starts with a package declaration.
	src := strings.TrimSpace(source)
	if !strings.HasPrefix(src, "package") {
		src = "package main\n\n" + src
	}

	modContent := "module flagbase-fn\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0o600); err != nil {
		return nil, fmt.Errorf("writing go.mod: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		return nil, fmt.Errorf("writing source: %w", err)
	}

	outPath := filepath.Join(dir, "fn.wasm")
	cmd := exec.Command("go", "build", "-o", outPath, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("compilation failed: %s", strings.TrimSpace(string(out)))
	}

	wasm, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("reading compiled wasm: %w", err)
	}

	return &Result{Runtime: RuntimeWASM, Artifact: wasm}, nil
}
