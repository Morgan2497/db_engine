package kv

import (
	"bytes"
	"slices"
)

/*
* mem map[string][]byte is replaced. 
* map is fundamentally a hash table, and hash tables have absolutely no order.
* While hash table is incredibly fast for singly-row lookups, it physcially 
* scatters data randomly in memory based on a hashing algorithm.
*/

type KV struct {
	log Log
	keys [][]byte
	vals [][]byte
}

// The disk log is "append-only", it contains a messy history of every action ever taken.
// If a user created a post, updated it twice, and then deleted it, te log contains four
// separate entries for that one key. Open() cleans this up. It reads all entries into memeory,
// groups them together by sorting them lexicographically and then iterates through them to
// resolve the final state. 
func (kv *KV) Open() error {
	// 0. Attempts to open the physical disk log. 
	if err := kv.log.Open(); err != nil {
		return err
	}

	// 1. Read all historical log entries into a temporary list.
	entries := []Entry{}
	for {
		ent := Entry{}
		eof, err := kv.log.Read(&ent)
		if err != nil {
			return err
		} else if eof {
			break
		}
		entries = append(entries, ent)
	}

	// 2. Sort the entries by key (preserving chronological order for duplicates.)
	slices.SortStableFunc(entries, func(a, b Entry) int {
		return bytes.Compare(a.key, b.key)
	})

	// 3. Compact the data and populate the in-memory arrays.
	kv.keys, kv.vals = kv.keys[:0], kv.vals[:0]
	for _, ent := range entries {
		n := len(kv.keys)
		// Decuplication: if the key already exits in our array, truncate the old value
		if n > 0 && bytes.Equal(kv.keys[n-1], ent.key) {
			kv.keys, kv.vals = kv.keys[:n-1], kv.vals[:n-1]
		}
		// Only append if it is not a tombstone (deleted record)
		if !ent.deleted {
			kv.keys = append(kv.keys, ent.key)
			kv.vals = append(kv.vals, ent.val)
		}
	}
	return nil
}

func (kv *KV) Close() error { return kv.log.Close() }

// 1. S ~[]E => Your sorted list (Haystack)
// 2. E => The type of items IN the list.
// 3. T any => The type of the value you are searching FOR (Needle)
// 4. x S => The list.
// 5. target T => The simple value you have.
// 6. cmp func(E, T) int => How to compare an item for the list to your value.
// - cmp(a, b) < 0 means a is less than b. returns -1
// - cmp(a, b) > 0 means a is greater than b. returns +1
// - cmp(a, b) == 0 means a is equal to b. returns 0
func BinarySearchFunc[S ~[]E, E, T any](x S, target T, cmp func(E, T) int) (pos int, ok bool) {
	// 1. Define the search boundaries.
	low := 0
	high := len(x)
	
	// 2. Binary search.
	for low < high {
		mid := low + (high - low) / 2
		
		if cmp(x[mid], target) < 0 {
			low = mid + 1
		} else {
			high = mid
		}
	}
	if low < len(x) && cmp(x[low], target) == 0 {
		return low, true
	}
	// Target not found. 
	return low, false
}
// Get retrieves a value. Returns false if the key does not exist.
// why does the public API accept a byte slice ([]byte) if our internal map uses a string?
// It treats everything as raw binary data. If our API forced to pass strings, they would have to constantly convert their binary payloads
// like serialized JSON, or raw integers into strings before talking to our database.
// So we say, "Give me raw data, I will handle the storage details."
func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	if idx, ok := slices.BinarySearchFunc(kv.keys, key, bytes.Compare); ok {
		return kv.vals[idx], true, nil
	}
	return nil, false, nil
}

type UpdateMode int

const (
	ModeUpsert UpdateMode = 0 // insert or update.
	ModeInsert UpdateMode = 1 // Insert new .
	ModeUpdate UpdateMode = 2 // update existing.
)

func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	// 1. Look up the current state.
	idx, exist := slices.BinarySearchFunc(kv.keys, key, bytes.Compare)
	// 2. Eval. the write intent.
	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(kv.vals[idx], val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(kv.vals[idx], val)
	default:
		panic("unreachable")
	}

	// 3. Apply the mutation if the eval. passed.
	if updated {
		// This append-only log step is cruciall for crash recovery. If the server loses power,
		// the data is not lost as the engine will simply read the log during the next Open()
		// to reconstruct the state. 
		if err = kv.log.Write(&Entry{key: key, val: val}); err != nil {
			return false, err
		} 
		if exist { // If the key already exists so for either upsert or update.
			kv.vals[idx] = val
		} else { // If the key does not exist, it is a new upcoming data, so insert.
				kv.keys = slices.Insert(kv.keys, idx, key)
				kv.vals = slices.Insert(kv.vals, idx, val)
			}	
	}
	return
}

// Set stores a value. Reports true if the database state actually changed.
func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	return kv.SetEx(key, val, ModeUpsert)
}

// Del removes a key. Reports true if a key was actually removed.
func (kv *KV) Del(key []byte) (deleted bool, err error) {
	// 1. Locate the exact index of the key in the array.
	idx, exist := slices.BinarySearchFunc(kv.keys, key, bytes.Compare)

	// 2. If it does not exist, there is nothing to delete.
	if !exist {
		return false, nil
	}
	if deleted {
		// Write tombstone to disk FIRST
		if err = kv.log.Write(&Entry{key: key, deleted: true}); err != nil {
			return false, err
		}
		delete(kv.mem, string(key))
	}
	return
}
