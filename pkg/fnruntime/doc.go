// Package fnruntime provides Go bindings for Flagbase host functions available
// to WASM functions compiled with GOOS=wasip1 GOARCH=wasm.
//
// Import this package in your function's main package to access bucket storage,
// feature flag evaluation, peer function invocation, and outbound HTTP.
//
// Build constraint note: all non-doc files carry //go:build wasip1 so the package
// compiles only when targeting the WASM runtime. Add the package as a local module
// replace directive or embed the source files directly (the scaffold ZIP includes
// fnruntime/ as a subdirectory so no external dependencies are required).
package fnruntime
