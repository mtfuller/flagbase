package compiler_test

import (
	"strings"
	"testing"

	"github.com/mtfuller/flagbase/internal/compiler"
)

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
