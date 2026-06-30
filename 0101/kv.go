package kv

import "bytes"

type KV struct {
	// It is an in-memory database right now, everything is stored volatiely in RAM.
	// keys and values are []byte, so they can hold any binary data.
	// Go maps can't ues []byte as keys, string is used.
	mem map[string][]byte 
}

//    ptr receiver, attached to the KV struct.
func (kv *KV) Open() error {
	kv.mem = map[string][]byte{} // empty
	return nil
}

func (kv *KV) Close() error {
	return nil
}

// Get retrieves a value. Returns false if the key does not exist.
// why does the public API accept a byte slice ([]byte) if our internal map uses a string?
// It treats everything as raw binary data. If our API forced to pass strings, they would have to constantly convert their binary payloads
// like serialized JSON, or raw integers into strings before talking to our database. 
// So we say, "Give me raw data, I will handle the storage details."
func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	// map lookup.
	val, ok = kv.mem[string(key)]
	return val, ok, nil
}

// Set stores a value. Reports true if the database state actually changed.
//     method           input (arguments)     return type
func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	// because we are storing this key inside the map, we must allocate memory 
	// on the heap to create a true, immutable string. 
	kStr := string(key)
	// go map return value is val && boolean
	oldVal, exists := kv.mem[kStr]

	// The state changes if the key is new OR if the value is being modified
	// "555-1111" -> bytes will get [53,53,53,45,49,49,49]
	if !exists || !bytes.Equal(oldVal, val) {
		kv.mem[kStr] = val
		return true, nil
	}

	// If the key exists and the value is identical, state did not change
	return false, nil
}

// Del removes a key. Reports true if a key was actually removed.
func (kv *KV) Del(key []byte) (deleted bool, err error) {
	kStr := string(key)
	_, exists := kv.mem[kStr]
	
	if exists {
		delete(kv.mem, kStr)
		return true, nil
	}

	return false, nil
}

