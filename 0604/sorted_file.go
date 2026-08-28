package kv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
)

type SortedFile struct {
	FileName string
	fp       *os.File
	nkeys    int
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

type SortedFileIter struct {
	file *SortedFile
	pos  int
	key  []byte
	val  []byte
}

func (file *SortedFile) Close() error {
	return file.fp.Close()
}

func (file *SortedFile) Size() int {
	return file.nkeys
}

func (iter *SortedFileIter) Valid() bool {
	return 0 <= iter.pos && iter.pos < iter.file.nkeys
}

func (iter *SortedFileIter) Key() []byte {
	return iter.key
}

func (iter *SortedFileIter) Val() []byte {
	return iter.val
}
func (iter *SortedFileIter) loadCurrent() (err error) {
	if iter.Valid() {
		iter.key, iter.val, err = iter.file.index(iter.pos)
	}
	return err
}

func (iter *SortedFileIter) Next() error {
	if iter.pos < iter.file.nkeys {
		iter.pos++
	}
	return iter.loadCurrent()
}

func (iter *SortedFileIter) Prev() error {
	if iter.pos >= 0 {
		iter.pos--
	}
	return iter.loadCurrent()
}

func (file *SortedFile) Iter() (SortedKVIter, error) {
	iter := &SortedFileIter{
		file: file,
		pos:  0,
	}
	if err := iter.loadCurrent(); err != nil {
		return nil, err
	}
	return iter, nil
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
	file.nkeys = writtenKeys
	return file.fp.Sync()
}

func (file *SortedFile) index(pos int) (key []byte, val []byte, err error) {
	check(0 <= pos && pos < file.nkeys)
	var buf [8]byte
	if _, err = file.fp.ReadAt(buf[:], int64(8+8*pos)); err != nil {
		return nil, nil, err
	}
	// KV offset
	offset := int64(binary.LittleEndian.Uint64(buf[:]))
	if int64(8+8*file.nkeys) > offset {
		return nil, nil, errors.New("corrupted file")
	}
	// read KV
	if _, err = file.fp.ReadAt(buf[:], offset); err != nil {
		return nil, nil, err
	}

	klen := binary.LittleEndian.Uint32(buf[0:4])
	vlen := binary.LittleEndian.Uint32(buf[4:8])

	data := make([]byte, klen+vlen)
	if _, err = file.fp.ReadAt(data, offset+4+4); err != nil {
		return nil, nil, err
	}
	return data[:klen], data[klen:], nil
}

func (file *SortedFile) findPos(target []byte) (int, error) {
	lo, hi := 0, file.nkeys

	for lo < hi {
		mid := lo + (hi-lo)/2

		key, _, err := file.index(mid)

		if err != nil {
			return -1, err
		}

		result := bytes.Compare(target, key)

		if result > 0 {
			lo = mid + 1
		} else if result < 0 {
			hi = mid
		} else {
			return mid, nil
		}
	}
	return lo, nil
}

func (file *SortedFile) Seek(key []byte) (SortedKVIter, error) {
	pos, err := file.findPos(key)
	if err != nil {
		return nil, err
	}

	iter := &SortedFileIter{
		file: file,
		pos:  pos,
	}

	if err = iter.loadCurrent(); err != nil {
		return nil, err
	}
	return iter, nil
}
