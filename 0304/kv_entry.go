// 0102: Serialization
// To store data types from a programming language on disk or send them over a network,
// they must be converted into a byte sequence which is called "Serialization"
// This is the process of flattening a complex, living data structure from your programming
// language into a single, continuous stream of raw binary bytes ([]byte).
//
// In step 0101, our key-value store lived entirely in computer's RAM. RAM is fast
// but RAM is volatile, the second the program exits, terminal closes, the data inside
// kv.mem disappears.
// To make our database durable, the data must be written to Non-Volatile Storage
// (SSD or Hard Drive).
//
// 0105: CRC32 checksum prepended for atomicity (13-byte header).
// | crc32 | key size | val size | deleted | key data | val data |

package kv

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

type Entry struct {
	key []byte
	val []byte
	// deleting a key in RAM was easy. However, on a phyiscal disk log,
	// you cannot go back, find the key and delete as it only moves forward.
	deleted bool
}

// 1. Serialization
func (ent *Entry) Encode() []byte {
	valLen := len(ent.val)
	if ent.deleted {
		valLen = 0
	}
	data := make([]byte, 4+4+4+1+len(ent.key)+valLen)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.key)))
	copy(data[4+4+4+1:], ent.key)
	if ent.deleted {
		data[4+4+4] = 1
	} else {
		binary.LittleEndian.PutUint32(data[8:12], uint32(len(ent.val)))
		copy(data[4+4+4+1+len(ent.key):], ent.val)
	}
	binary.LittleEndian.PutUint32(data[0:4], crc32.ChecksumIEEE(data[4:]))
	return data
}

var ErrBadSum = errors.New("bad checksum")

// 2. Deserialization
func (ent *Entry) Decode(r io.Reader) error {
	var header [4 + 4 + 4 + 1]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	klen := int(binary.LittleEndian.Uint32(header[4:8]))
	vlen := int(binary.LittleEndian.Uint32(header[8:12]))
	deleted := header[4+4+4]

	data := make([]byte, klen+vlen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	h := crc32.NewIEEE()
	h.Write(header[4:])
	h.Write(data)
	if h.Sum32() != binary.LittleEndian.Uint32(header[0:4]) {
		return ErrBadSum
	}

	ent.key = data[:klen]
	if deleted != 0 {
		ent.deleted = true
	} else {
		ent.deleted = false
		ent.val = data[klen:]
	}
	return nil
}
