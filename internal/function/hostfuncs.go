package function

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/storage"
	"github.com/mtfuller/flagbase/internal/table"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// invStateKey is the context key for per-invocation state.
type invStateKey struct{}

// invState holds the result and error buffers for a single WASM invocation.
type invState struct {
	result []byte
	errMsg []byte
}

func (st *invState) setResult(data []byte) {
	st.result = data
	st.errMsg = nil
}

func (st *invState) setError(err error) {
	st.errMsg = []byte(err.Error())
	st.result = nil
}

// invStateFromCtx retrieves the invState from context, returning an empty one as fallback.
func invStateFromCtx(ctx context.Context) *invState {
	if v := ctx.Value(invStateKey{}); v != nil {
		if st, ok := v.(*invState); ok {
			return st
		}
	}
	return &invState{}
}

// readStr reads a UTF-8 string from WASM linear memory.
func readStr(m api.Module, ptr, length uint32) string {
	if length == 0 {
		return ""
	}
	buf, ok := m.Memory().Read(ptr, length)
	if !ok {
		return ""
	}
	return string(buf)
}

// readBytes reads and copies bytes from WASM linear memory.
func readBytes(m api.Module, ptr, length uint32) []byte {
	if length == 0 {
		return nil
	}
	buf, ok := m.Memory().Read(ptr, length)
	if !ok {
		return nil
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out
}

// HostDeps are the flagbase services exposed to WASM functions via the "flagbase" host module.
type HostDeps struct {
	Storage storage.BucketAdapter
	Flags   *feature.Engine
	Store   *Store        // for fn_invoke; may be nil to break init cycle
	Tables  *table.Engine // may be nil when tables feature is not wired
}

const errResult = uint32(0xFFFFFFFF)

// registerHostModule builds and instantiates the "flagbase" host module.
func registerHostModule(ctx context.Context, rt wazero.Runtime, deps HostDeps) error {
	b := rt.NewHostModuleBuilder("flagbase")

	// result_read(outPtr, outLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("outPtr", "outLen").
		WithResultNames("written").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			outPtr := uint32(stack[0])
			outLen := uint32(stack[1])
			st := invStateFromCtx(ctx)
			n := copy32(m, outPtr, outLen, st.result)
			stack[0] = uint64(n)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("result_read")

	// error_read(outPtr, outLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("outPtr", "outLen").
		WithResultNames("written").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			outPtr := uint32(stack[0])
			outLen := uint32(stack[1])
			st := invStateFromCtx(ctx)
			n := copy32(m, outPtr, outLen, st.errMsg)
			stack[0] = uint64(n)
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("error_read")

	// bucket_get(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("bucketPtr", "bucketLen", "keyPtr", "keyLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			bucket := readStr(m, uint32(stack[0]), uint32(stack[1]))
			key := readStr(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			rc, err := deps.Storage.GetObject(ctx, bucket, key)
			if err != nil {
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("bucket_get")

	// bucket_put(bucketPtr, bucketLen, keyPtr, keyLen, dataPtr, dataLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("bucketPtr", "bucketLen", "keyPtr", "keyLen", "dataPtr", "dataLen").
		WithResultNames("ok").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			bucket := readStr(m, uint32(stack[0]), uint32(stack[1]))
			key := readStr(m, uint32(stack[2]), uint32(stack[3]))
			data := readBytes(m, uint32(stack[4]), uint32(stack[5]))
			st := invStateFromCtx(ctx)
			err := deps.Storage.PutObject(ctx, bucket, key, bytes.NewReader(data))
			if err != nil {
				st.setError(err)
				stack[0] = 0
				return
			}
			stack[0] = 1
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("bucket_put")

	// bucket_delete(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("bucketPtr", "bucketLen", "keyPtr", "keyLen").
		WithResultNames("ok").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			bucket := readStr(m, uint32(stack[0]), uint32(stack[1]))
			key := readStr(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			err := deps.Storage.DeleteObject(ctx, bucket, key)
			if err != nil {
				st.setError(err)
				stack[0] = 0
				return
			}
			stack[0] = 1
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("bucket_delete")

	// bucket_list(bucketPtr, bucketLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("bucketPtr", "bucketLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			bucket := readStr(m, uint32(stack[0]), uint32(stack[1]))
			st := invStateFromCtx(ctx)
			names, err := deps.Storage.ListObjects(ctx, bucket)
			if err != nil {
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			data, err := json.Marshal(names)
			if err != nil {
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("bucket_list")

	// flag_eval(keyPtr, keyLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("keyPtr", "keyLen").
		WithResultNames("result").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			key := readStr(m, uint32(stack[0]), uint32(stack[1]))
			if deps.Flags == nil {
				stack[0] = uint64(errResult)
				return
			}
			val := deps.Flags.EvaluateBool(key, map[string]interface{}{})
			if val {
				stack[0] = 1
			} else {
				stack[0] = 0
			}
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("flag_eval")

	// fn_invoke(idPtr, idLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("idPtr", "idLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			id := readStr(m, uint32(stack[0]), uint32(stack[1]))
			st := invStateFromCtx(ctx)
			if deps.Store == nil {
				err := fmt.Errorf("fn_invoke: store not available")
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			out, err := deps.Store.Invoke(ctx, id, 30*time.Second)
			if err != nil {
				st.setError(err)
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(out)
			stack[0] = uint64(len(out))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("fn_invoke")

	// http_fetch(reqPtr, reqLen uint32) uint32
	b.NewFunctionBuilder().
		WithParameterNames("reqPtr", "reqLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			reqBytes := readBytes(m, uint32(stack[0]), uint32(stack[1]))
			st := invStateFromCtx(ctx)

			var fetchReq struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers,omitempty"`
				Body    []byte            `json:"body,omitempty"`
			}
			if err := json.Unmarshal(reqBytes, &fetchReq); err != nil {
				st.setError(fmt.Errorf("http_fetch: invalid request JSON: %w", err))
				stack[0] = uint64(errResult)
				return
			}

			var bodyReader io.Reader = http.NoBody
			if len(fetchReq.Body) > 0 {
				bodyReader = bytes.NewReader(fetchReq.Body)
			}

			httpReq, err := http.NewRequestWithContext(ctx, fetchReq.Method, fetchReq.URL, bodyReader)
			if err != nil {
				st.setError(fmt.Errorf("http_fetch: building request: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			for k, v := range fetchReq.Headers {
				httpReq.Header.Set(k, v)
			}

			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(httpReq)
			if err != nil {
				st.setError(fmt.Errorf("http_fetch: executing request: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			defer resp.Body.Close()

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				st.setError(fmt.Errorf("http_fetch: reading response: %w", err))
				stack[0] = uint64(errResult)
				return
			}

			respHeaders := make(map[string]string, len(resp.Header))
			for k, vals := range resp.Header {
				if len(vals) > 0 {
					respHeaders[k] = vals[0]
				}
			}

			fetchResp := struct {
				Status  int               `json:"status"`
				Headers map[string]string `json:"headers"`
				Body    []byte            `json:"body"`
			}{
				Status:  resp.StatusCode,
				Headers: respHeaders,
				Body:    respBody,
			}

			data, err := json.Marshal(fetchResp)
			if err != nil {
				st.setError(fmt.Errorf("http_fetch: encoding response: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("http_fetch")

	// table_get(tableKeyPtr, tableKeyLen, idPtr, idLen uint32) uint32
	// Returns the JSON-encoded Record in the result buffer, or errResult on error.
	b.NewFunctionBuilder().
		WithParameterNames("tableKeyPtr", "tableKeyLen", "idPtr", "idLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			tableKey := readStr(m, uint32(stack[0]), uint32(stack[1]))
			id := readStr(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			if deps.Tables == nil {
				st.setError(fmt.Errorf("table_get: tables not available"))
				stack[0] = uint64(errResult)
				return
			}
			rec, err := deps.Tables.GetRecord(tableKey, id)
			if err != nil {
				st.setError(fmt.Errorf("table_get: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			if rec == nil {
				st.setError(fmt.Errorf("table_get: record not found"))
				stack[0] = uint64(errResult)
				return
			}
			data, err := json.Marshal(rec)
			if err != nil {
				st.setError(fmt.Errorf("table_get: encoding result: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("table_get")

	// table_put(tableKeyPtr, tableKeyLen, dataPtr, dataLen uint32) uint32
	// If data contains "_id", updates that record; otherwise inserts a new one.
	// Returns the JSON-encoded Record in the result buffer.
	b.NewFunctionBuilder().
		WithParameterNames("tableKeyPtr", "tableKeyLen", "dataPtr", "dataLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			tableKey := readStr(m, uint32(stack[0]), uint32(stack[1]))
			rawData := readBytes(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			if deps.Tables == nil {
				st.setError(fmt.Errorf("table_put: tables not available"))
				stack[0] = uint64(errResult)
				return
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(rawData, &payload); err != nil {
				st.setError(fmt.Errorf("table_put: invalid JSON: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			var rec interface{}
			var opErr error
			if existingID, ok := payload["_id"].(string); ok && existingID != "" {
				delete(payload, "_id")
				rec, opErr = deps.Tables.UpdateRecord(tableKey, existingID, payload)
			} else {
				delete(payload, "_id")
				rec, opErr = deps.Tables.InsertRecord(tableKey, payload)
			}
			if opErr != nil {
				st.setError(fmt.Errorf("table_put: %w", opErr))
				stack[0] = uint64(errResult)
				return
			}
			data, err := json.Marshal(rec)
			if err != nil {
				st.setError(fmt.Errorf("table_put: encoding result: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("table_put")

	// table_delete(tableKeyPtr, tableKeyLen, idPtr, idLen uint32) uint32
	// Returns 1 on success, 0 on error (error detail in error buffer).
	b.NewFunctionBuilder().
		WithParameterNames("tableKeyPtr", "tableKeyLen", "idPtr", "idLen").
		WithResultNames("ok").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			tableKey := readStr(m, uint32(stack[0]), uint32(stack[1]))
			id := readStr(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			if deps.Tables == nil {
				st.setError(fmt.Errorf("table_delete: tables not available"))
				stack[0] = 0
				return
			}
			if err := deps.Tables.DeleteRecord(tableKey, id); err != nil {
				st.setError(fmt.Errorf("table_delete: %w", err))
				stack[0] = 0
				return
			}
			stack[0] = 1
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("table_delete")

	// table_query(tableKeyPtr, tableKeyLen, optsPtr, optsLen uint32) uint32
	// opts is a JSON-encoded QueryOptions. Returns a JSON array of Records.
	b.NewFunctionBuilder().
		WithParameterNames("tableKeyPtr", "tableKeyLen", "optsPtr", "optsLen").
		WithResultNames("resultLen").
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			tableKey := readStr(m, uint32(stack[0]), uint32(stack[1]))
			optsBytes := readBytes(m, uint32(stack[2]), uint32(stack[3]))
			st := invStateFromCtx(ctx)
			if deps.Tables == nil {
				st.setError(fmt.Errorf("table_query: tables not available"))
				stack[0] = uint64(errResult)
				return
			}
			var opts table.QueryOptions
			if len(optsBytes) > 0 {
				if err := json.Unmarshal(optsBytes, &opts); err != nil {
					st.setError(fmt.Errorf("table_query: invalid opts JSON: %w", err))
					stack[0] = uint64(errResult)
					return
				}
			}
			records, err := deps.Tables.QueryRecords(tableKey, opts)
			if err != nil {
				st.setError(fmt.Errorf("table_query: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			data, err := json.Marshal(records)
			if err != nil {
				st.setError(fmt.Errorf("table_query: encoding result: %w", err))
				stack[0] = uint64(errResult)
				return
			}
			st.setResult(data)
			stack[0] = uint64(len(data))
		}), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("table_query")

	_, err := b.Instantiate(ctx)
	return err
}

// copy32 copies up to outLen bytes from src into WASM memory at outPtr and returns the count.
func copy32(m api.Module, outPtr, outLen uint32, src []byte) uint32 {
	if len(src) == 0 || outLen == 0 {
		return 0
	}
	n := uint32(len(src))
	if n > outLen {
		n = outLen
	}
	m.Memory().Write(outPtr, src[:n])
	return n
}
