package compiler_test

import (
	"strings"
	"testing"

	"github.com/mtfuller/flagbase/internal/compiler"
)

func TestGoCompiler_HelloWorld(t *testing.T) {
	c := compiler.NewGoCompiler()
	result, err := c.Compile(`package main

import "fmt"

func main() {
	fmt.Println("hello from wasm")
}`)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	if result.Runtime != compiler.RuntimeWASM {
		t.Errorf("expected runtime %q, got %q", compiler.RuntimeWASM, result.Runtime)
	}
	if len(result.Artifact) == 0 {
		t.Error("artifact is empty")
	}
	// WASM magic number: \0asm
	if len(result.Artifact) < 4 || string(result.Artifact[:4]) != "\x00asm" {
		t.Error("artifact does not start with WASM magic number")
	}
}

func TestGoCompiler_SyntaxError(t *testing.T) {
	c := compiler.NewGoCompiler()
	_, err := c.Compile(`package main
func main() { INVALID SYNTAX`)
	if err == nil {
		t.Fatal("expected compilation error for invalid syntax")
	}
}

func TestGoCompiler_NoPkgDeclaration(t *testing.T) {
	// Should auto-prepend package main
	c := compiler.NewGoCompiler()
	result, err := c.Compile(`import "fmt"
func main() { fmt.Println("ok") }`)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	if result.Runtime != compiler.RuntimeWASM {
		t.Errorf("expected wasm runtime, got %q", result.Runtime)
	}
}

func TestJSCompiler_ValidSource(t *testing.T) {
	c := compiler.NewJSCompiler()
	result, err := c.Compile(`function handle() { console.log("hi"); }`)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	if result.Runtime != compiler.RuntimeJS {
		t.Errorf("expected runtime %q, got %q", compiler.RuntimeJS, result.Runtime)
	}
	if !strings.Contains(string(result.Artifact), "handle") {
		t.Error("artifact should contain source")
	}
}

func TestJSCompiler_SyntaxError(t *testing.T) {
	c := compiler.NewJSCompiler()
	_, err := c.Compile(`function handle( { invalid syntax`)
	if err == nil {
		t.Fatal("expected syntax error")
	}
}
