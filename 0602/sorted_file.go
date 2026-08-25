package kv

import (
	"encoding/binary"
	"os"
)

type SortedFile struct {
	FileName string
	fp       *os.File
}

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
	var buf [8]byte

	nkeys := kv.Size()
	binary.LittleEndian.PutUint64(buf[:], uint64(nkeys))

	if _, err = file.fp.WriteAt(buf[:], 0); err != nil {
		return err
	}

	writtenKeys := 0
	dataOffset := 8 + 8*nkeys

	iter, err := kv.Iter()
	if err != nil {
		return err
	}

	for iter.Valid() {
		key := iter.Key()
		val := iter.Val()
		binary.LittleEndian.PutUint64(buf[:], uint64(dataOffset))

		indexOffset := 8 + 8*writtenKeys
		if _, err = file.fp.WriteAt(buf[:], int64(indexOffset)); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(len(val)))

		if _, err = file.fp.WriteAt(buf[:], int64(dataOffset)); err != nil {
			return err
		}
		dataOffset += 8

		if _, err = file.fp.WriteAt(key, int64(dataOffset)); err != nil {
			return err
		}
		dataOffset += len(key)

		if _, err = file.fp.WriteAt(val, int64(dataOffset)); err != nil {
			return err
		}
		dataOffset += len(val)
		writtenKeys++

		if err = iter.Next(); err != nil {
			return err
		}
	}
	check(writtenKeys == nkeys)
	return file.fp.Sync()
}
