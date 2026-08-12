package db0601

import (
	"encoding/binary"
	"os"
)

type SortedKV interface {
	Size() int
	Iter() (SortedKVIter, error)
}

type SortedKVIter interface {
	Valid() bool
	Key() []byte
	Val() []byte
	Next() error
	Prev() error
}

type SortedFile struct {
	FileName string
	fp       *os.File
}

func (file *SortedFile) Close() error {
	return file.fp.Close()
}

func (file *SortedFile) CreateFromSorted(kv SortedKV) (err error) {
	if file.fp, err = createFileSync(file.FileName); err != nil {
		return err
	}
	if err = file.writeSortedFile(kv); err != nil {
		_ = file.Close()
	}
	return err
}

func (file *SortedFile) writeSortedFile(kv SortedKV) (err error) {
	var buf [4 + 4]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(kv.Size()))
	if _, err = file.fp.WriteAt(buf[:8], 0); err != nil {
		return err
	}

	nkeys := 0
	offset := 8 + 8*kv.Size()
	iter, err := kv.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		key, val := iter.Key(), iter.Val()

		binary.LittleEndian.PutUint64(buf[:8], uint64(offset))
		if _, err = file.fp.WriteAt(buf[:8], int64(8+8*nkeys)); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(len(val)))
		if _, err = file.fp.WriteAt(buf[:4+4], int64(offset)); err != nil {
			return err
		}
		offset += 4 + 4
		if _, err = file.fp.WriteAt(key, int64(offset)); err != nil {
			return err
		}
		offset += len(key)
		if _, err = file.fp.WriteAt(val, int64(offset)); err != nil {
			return err
		}
		offset += len(val)

		nkeys++
	}
	if err != nil {
		return err
	}

	check(nkeys == kv.Size())
	return file.fp.Sync()
}
// QzBQWVJJOUhU https://trialofcode.org/