//go:build wasip1

package fnruntime

import "encoding/json"

//go:wasmimport flagbase bucket_get
func _bucketGet(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32

//go:wasmimport flagbase bucket_put
func _bucketPut(bucketPtr, bucketLen, keyPtr, keyLen, dataPtr, dataLen uint32) uint32

//go:wasmimport flagbase bucket_delete
func _bucketDelete(bucketPtr, bucketLen, keyPtr, keyLen uint32) uint32

//go:wasmimport flagbase bucket_list
func _bucketList(bucketPtr, bucketLen uint32) uint32

const errResult = uint32(0xFFFFFFFF)

// GetObject retrieves an object from the named bucket. Returns the object bytes
// or an error if the object does not exist or cannot be read.
func GetObject(bucket, key string) ([]byte, error) {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	size := _bucketGet(bPtr, bLen, kPtr, kLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	return readResult(size), nil
}

// PutObject stores data under key in the named bucket. Returns an error on
// failure.
func PutObject(bucket, key string, data []byte) error {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	dPtr := bufPtr(data)
	ok := _bucketPut(bPtr, bLen, kPtr, kLen, dPtr, uint32(len(data)))
	if ok == 0 {
		return &hostError{msg: readLastError()}
	}
	return nil
}

// DeleteObject removes an object from the named bucket.
func DeleteObject(bucket, key string) error {
	bPtr, bLen := strPtr(bucket)
	kPtr, kLen := strPtr(key)
	ok := _bucketDelete(bPtr, bLen, kPtr, kLen)
	if ok == 0 {
		return &hostError{msg: readLastError()}
	}
	return nil
}

// ListObjects returns the names of all objects in the named bucket.
func ListObjects(bucket string) ([]string, error) {
	bPtr, bLen := strPtr(bucket)
	size := _bucketList(bPtr, bLen)
	if size == errResult {
		return nil, &hostError{msg: readLastError()}
	}
	data := readResult(size)
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, &hostError{msg: "decoding list response: " + err.Error()}
	}
	return names, nil
}

// hostError is a simple error type for host function failures.
type hostError struct{ msg string }

func (e *hostError) Error() string { return e.msg }
