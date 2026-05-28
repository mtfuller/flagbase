package compiler

// Runtime identifies how a compiled function artifact is executed.
type Runtime string

const (
	RuntimeWASM Runtime = "wasm" // executed via Wazero (WASI preview1)
	RuntimeJS   Runtime = "js"   // executed via embedded goja interpreter
)

// Language is the source language of a function.
type Language string

const (
	LanguageGo         Language = "go"
	LanguageJavaScript Language = "javascript"
)

// Result holds the output of a successful compilation.
type Result struct {
	Runtime  Runtime
	Artifact []byte // wasm bytes for Go, raw JS source for JavaScript
}

// Compiler compiles function source code into an executable artifact.
type Compiler interface {
	Compile(source string) (*Result, error)
}
