package db0102

import (
	"encoding/binary"
	"io"
)

type Entry struct {
	key []byte
	val []byte
}

func (ent *Entry) Encode() []byte {
	data := make([]byte, 4+4+len(ent.key)+len(ent.val))
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))
	copy(data[8:], ent.key)
	copy(data[8+len(ent.key):], ent.val)
	return data
}

func (ent *Entry) Decode(r io.Reader) error {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	klen := int(binary.LittleEndian.Uint32(header[0:4]))
	vlen := int(binary.LittleEndian.Uint32(header[4:8]))

	data := make([]byte, klen+vlen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	ent.key = data[:klen]
	ent.val = data[klen:]
	return nil
}
// QzBQWVJJOUhU https://trialofcode.org/