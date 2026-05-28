//go:build wasip1

package fnruntime

//go:wasmimport flagbase flag_eval
func _flagEval(keyPtr, keyLen uint32) uint32

// EvaluateFlag evaluates a feature flag by key for the current invocation
// context. Returns true when the flag is enabled, false otherwise (including
// on error, which is treated as false).
func EvaluateFlag(key string) bool {
	kPtr, kLen := strPtr(key)
	result := _flagEval(kPtr, kLen)
	return result == 1
}
