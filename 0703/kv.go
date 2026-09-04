package kv

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
)

type KVOptions struct {
	Dirpath string
}

type KV struct {
	Options KVOptions
	// metadata
	meta    KVMetaStore
	version uint64
	//data
	log  Log         // WAL: durable recent-write history.
	mem  SortedArray // MemTale: queryable recent changes.
	main SortedFile  // SSTable: durable older database state.
	MultiClosers
}

func (kv *KV) Open() (err error) {
	// 0. Attempts to open the physical disk log.
	if err = kv.openAll(); err != nil {
		_ = kv.Close()
	}
	return err
}

func (kv *KV) openAll() error {
	// return an error for permission denied, invalid path, disk/filesystem error.
	// okay for dir creation, dir already exists.
	err := os.Mkdir(kv.Options.Dirpath, 0o755)
	// if already exists, it returns an error.
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	if err := kv.openMeta(); err != nil {
		return err
	}

	if err := kv.openLog(); err != nil {
		return err
	}
	return kv.openSSTable()
}

func (kv *KV) openMeta() error {
	kv.meta.slots[0].FileName = path.Join(kv.Options.Dirpath, "meta0")

	kv.meta.slots[1].FileName = path.Join(kv.Options.Dirpath, "meta1")

	if err := kv.meta.Open(); err != nil {
		return err
	}

	kv.MultiClosers = append(kv.MultiClosers, &kv.meta)
	return nil
}

func (kv *KV) openLog() error {
	kv.log.FileName = path.Join(kv.Options.Dirpath, "kv_log")
	if err := kv.log.Open(); err != nil {
		return err
	}

	kv.MultiClosers = append(kv.MultiClosers, &kv.log)

	entries := []Entry{}
	for {
		ent := Entry{}
		eof, err := kv.log.Read(&ent)
		if err != nil {
			return err
		}
		if eof {
			break
		}
		entries = append(entries, ent)
	}

	slices.SortStableFunc(entries, func(a, b Entry) int {
		return bytes.Compare(a.key, b.key)
	})

	kv.mem.Clear()
	for _, ent := range entries {
		n := kv.mem.Size()
		if n > 0 && bytes.Equal(kv.mem.Key(n-1), ent.key) {
			kv.mem.Pop()
		}
		kv.mem.Push(ent.key, ent.val, ent.deleted)
	}
	return nil
}

func (kv *KV) openSSTable() error {
	meta := kv.meta.Get()
	kv.version = meta.Version

	if meta.SSTable != "" {
		kv.main.FileName = path.Join(kv.Options.Dirpath, meta.SSTable)
		if err := kv.main.Open(); err != nil {
			return err
		}
		kv.MultiClosers = append(kv.MultiClosers, &kv.main)
	}
	return nil
}

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
		mid := low + (high-low)/2

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
	iter, err := kv.Seek(key)
	ok = err == nil && iter.Valid() && bytes.Equal(iter.Key(), key)
	if ok {
		val = iter.Val()
	}
	return val, ok, err
}

type UpdateMode int

const (
	ModeUpsert UpdateMode = 0 // insert or update.
	ModeInsert UpdateMode = 1 // Insert new .
	ModeUpdate UpdateMode = 2 // update existing.
)

func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	// 1. Look up the current state.
	oldVal, exist, err := kv.Get(key)
	if err != nil {
		return false, err
	}

	// 2. Eval. the write intent.
	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(oldVal, val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(oldVal, val)
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

		memUpdated, memErr := kv.mem.Set(key, val)
		check(memErr == nil && memUpdated)
	}
	return updated, nil
}

// Set stores a value. Reports true if the database state actually changed.
func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	return kv.SetEx(key, val, ModeUpsert)
}

func (kv *KV) Del(key []byte) (deleted bool, err error) {
	// Check the logical database: MemTable + SSTable.
	if _, exist, err := kv.Get(key); err != nil || !exist {
		return false, err
	}

	// Make the deletion durable first.
	if err = kv.log.Write(&Entry{key: key, deleted: true}); err != nil {
		return false, err
	}

	// Record the tombstone in the MemTable.
	_, err = kv.mem.Del(key)
	check(err == nil)

	return true, nil
}

func (kv *KV) Seek(key []byte) (SortedKVIter, error) {
	m := MergedSortedKV{&kv.mem, &kv.main}
	iter, err := m.Seek(key)
	if err != nil {
		return nil, err
	}
	return filterDeleted(iter)
}

func filterDeleted(iter SortedKVIter) (SortedKVIter, error) {
	for iter.Valid() && iter.Deleted() {
		if err := iter.Next(); err != nil {
			return nil, err
		}
	}
	return NoDeletedIter{iter}, nil
}

type NoDeletedIter struct {
	SortedKVIter // inherits all method.
}

type RangedKVIter struct {
	iter SortedKVIter
	stop []byte
	desc bool
}

func (iter *RangedKVIter) Key() []byte {
	return iter.iter.Key()
}

func (iter *RangedKVIter) Val() []byte {
	return iter.iter.Val()
}

func (iter *RangedKVIter) Valid() bool {
	if !iter.iter.Valid() {
		return false
	}

	r := bytes.Compare(iter.iter.Key(), iter.stop)
	if iter.desc && r < 0 {
		return false
	} else if !iter.desc && r > 0 {
		return false
	}
	return true
}
func (iter *RangedKVIter) Next() error {
	if !iter.Valid() {
		return nil
	}

	if iter.desc {
		return iter.iter.Prev()
	}

	return iter.iter.Next()
}

func (iter NoDeletedIter) Next() (err error) {
	err = iter.SortedKVIter.Next()

	for err == nil && iter.Valid() && iter.Deleted() {
		err = iter.SortedKVIter.Next()
	}
	return err
}

func (iter NoDeletedIter) Prev() (err error) {
	err = iter.SortedKVIter.Prev()

	for err == nil && iter.Valid() && iter.Deleted() {
		err = iter.SortedKVIter.Prev()
	}
	return err
}

func (kv *KV) Range(start, stop []byte, desc bool) (*RangedKVIter, error) {
	iter, err := kv.Seek(start)
	if err != nil {
		return nil, err
	}

	// Seek finds the first key >= start.
	// A descending scan needs the first key <= start.
	if desc {
		seekWhenPastEnd := !iter.Valid()
		seekLandedAboveStart := false

		// Key() is only safe when the iterator is valid.
		if !seekWhenPastEnd {
			seekLandedAboveStart = bytes.Compare(iter.Key(), start) > 0
		}

		needTMoveBackWard := seekWhenPastEnd || seekLandedAboveStart

		if needTMoveBackWard {
			if err := iter.Prev(); err != nil {
				return nil, err
			}
		}
	}

	return &RangedKVIter{
		iter: iter,
		stop: stop,
		desc: desc,
	}, nil

}

func (kv *KV) Compact() error {
	// reserve a new unique sstable version.
	kv.version++
	// create a new version for upsert.
	sstable := fmt.Sprintf("sstable_%d", kv.version)
	// becomes something like kv_test/sstable_3
	filename := path.Join(kv.Options.Dirpath, sstable)
	file := SortedFile{FileName: filename}

	// combine recent memtable data iwth the old sstable.
	merged := MergedSortedKV{&kv.mem, &kv.main}

	// write the recent memtable data wit the old sstable.
	if err := file.CreateFromSorted(merged); err != nil {
		_ = os.Remove(filename)
		return err
	}

	// prepare metadata that points to the new sstable.
	meta := kv.meta.Get()
	meta.Version = kv.version
	meta.SSTable = sstable

	// atomically store the new metadata.
	if err := kv.meta.Set(meta); err != nil {
		_ = file.Close()
		return err
	}

	// ------------ At this point the old one can go ------------
	_ = kv.main.Close()
	_ = os.Remove(kv.main.FileName)

	// make the new sstable durable (main one)
	kv.main = file

	kv.mem.Clear()

	return kv.log.Truncate()
}
