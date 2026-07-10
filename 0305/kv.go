package kv

import (
	"bytes"
)

type KV struct {
	log Log
	// It is an in-memory database right now, everything is stored volatiely in RAM.
	// keys and values are []byte, so they can hold any binary data.
	// Go maps can't ues []byte as keys, string is used.
	mem map[string][]byte
}

//    ptr receiver, attached to the KV struct.
func (kv *KV) Open() error {
	if err := kv.log.Open(); err != nil {
		return err
	}

	kv.mem = map[string][]byte{}

	// Replay the log to reconstruct the map state
	for {
		ent := Entry{}
		eof, err := kv.log.Read(&ent)
		if err != nil {
			return err
		} else if eof {
			break
		}

		if ent.deleted {
			delete(kv.mem, string(ent.key))
		} else {
			kv.mem[string(ent.key)] = ent.val
		}
	}
	return nil
}

func (kv *KV) Close() error { return kv.log.Close() }

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

type UpdateMode int

const (
	ModeUpsert UpdateMode = 0 // insert or update.
	ModeInsert UpdateMode = 1 // Insert new .
	ModeUpdate UpdateMode = 2 // update existing.
)

func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	// 1. Look up the current state.
	prev, exist := kv.mem[string(key)]

	// 2. Eval. the write intent.
	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(prev, val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(prev, val)
	default:
		panic("unreachable")
	}

	// 3. Apply the mutation if the eval. passed.
	if updated {
		if err = kv.log.Write(&Entry{key: key, val: val}); err != nil {
			return false, err
		}
		kv.mem[string(key)] = val
	}
	return
}

// Set stores a value. Reports true if the database state actually changed.
func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	return kv.SetEx(key, val, ModeUpsert)
}

// Del removes a key. Reports true if a key was actually removed.
func (kv *KV) Del(key []byte) (deleted bool, err error) {
	_, deleted = kv.mem[string(key)]
	if deleted {
		// Write tombstone to disk FIRST
		if err = kv.log.Write(&Entry{key: key, deleted: true}); err != nil {
			return false, err
		}
		delete(kv.mem, string(key))
	}
	return
}
