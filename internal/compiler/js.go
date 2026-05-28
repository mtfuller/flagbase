package compiler

import (
	"fmt"

	"github.com/dop251/goja"
)

// JSCompiler validates JavaScript source and packages it for execution by the
// embedded goja interpreter. No external tooling is required.
type JSCompiler struct{}

func NewJSCompiler() *JSCompiler { return &JSCompiler{} }

func (c *JSCompiler) Compile(source string) (*Result, error) {
	// Parse to catch syntax errors at compile time.
	if _, err := goja.Compile("fn.js", source, false); err != nil {
		return nil, fmt.Errorf("javascript syntax error: %w", err)
	}
	return &Result{Runtime: RuntimeJS, Artifact: []byte(source)}, nil
}
