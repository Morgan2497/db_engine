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
// | key size | val size | key data | val data |
// | 4 bytes  | 4 bytes  |   ...    |   ...    |
// For example, key=a and val=bb returns []byte(1, 0, 0, 0, 2, 0, 0, 0, 'a', 'b', 'b').

package kv 
import (
	"encoding/binary"
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
func(ent *Entry) Encode() []byte {
	// Allocate 4 (key size) + 4 (val size) + 1 (deleted flag) + payload lengths
	data := make([]byte, 4+4+1+len(ent.key)+len(ent.val))
	
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))

	// Write deleted flag 
	if ent.deleted {
		data[8] = 1
	} else {
		data[8] = 0
	}

	copy(data[9:], ent.key) 
	copy(data[9+len(ent.key):], ent.val) 
	return data
}

// 2. Deserialization
func (ent *Entry) Decode(r io.Reader) error {
	header := make([]byte, 9) // MUST be 9 to hold the deleted flag
	if _, err := io.ReadFull(r, header); err != nil {
		return err 
	}

	keySize := binary.LittleEndian.Uint32(header[0:4])
	valSize := binary.LittleEndian.Uint32(header[4:8])
	ent.deleted = header[8] == 1

	ent.key = make([]byte, keySize)
	ent.val = make([]byte, valSize)

	if _, err := io.ReadFull(r, ent.key); err != nil {
		return err
	}
	if _, err := io.ReadFull(r, ent.val); err != nil {
		return err
	}

	return nil
}

