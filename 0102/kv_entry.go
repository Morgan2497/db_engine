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
// Example trace (key="k1", val="xxx"):
// Offset:  0    	4    	8       10
// 			[2,0,0,0][3,0,0,0]['k','1']['x','x','x']
//
package kv 
import (
	"encoding/binary"
	"io"
)

type Entry struct {
	key []byte
	val []byte
}

// 1. Serialization 
func(ent *Entry) Encode() []byte {
	// if key = a, val = bb 
	// 									 4+4+1+2 = 11 bytes.
	// data = [0,0,0,0,0,0,0,0,0,0,0]
	data := make([]byte, 4+4+len(ent.key)+len(ent.val))
	// PutUint32: a method for encoding a 32-bit unsigned integer into a byte. 
	// data[0:4] to reserve 4 bytes for the key.
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
	// same but for value size. 
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))
	copy(data[8:], ent.key) // key copy = a
	copy(data[8+len(ent.key):], ent.val) // value copy = bb 
	return data
}

// 2. Deserialization: parse a byte sequence from an io.Reader back into the Entry Struct.
// io.Reader is for input
// io.Writer is the corresponding output interface. 
func (ent *Entry) Decode(r io.Reader) error {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return err 
	}

	// decode the header bytes back into numbers 
	keySize := binary.LittleEndian.Uint32(header[0:4])
	valSize := binary.LittleEndian.Uint32(header[4:8])

	// allocate the exact space needed inside the entry field. 
	ent.key = make([]byte, keySize)
	ent.val = make([]byte, valSize)

	// Read the raw keys, handling potential stream truncation errors
	if _, err := io.ReadFull(r, ent.key); err != nil {
		return err
	}
	
	// Read the raw values, handling potential stream truncation errors
	if _, err := io.ReadFull(r, ent.val); err != nil {
		return err
	}

	return nil
}

