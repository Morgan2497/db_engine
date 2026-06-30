package kv 

import (
	"encoding/binary"
	"io"
	"os"
	"errors"
	"hash/crc32"
)

type Entry struct {
	key []byte
	val []byte
	// deleting a key in RAM was easy. However, on a physical disk log,
	// you cannot go back, find the key and delete as it only moves forward.
	deleted bool
}

// ErrBadSum is returned when the calculated CRC32 hash does not match the saved header.
var ErrBadSum = errors.New("bad checksum")

// 1. Serialization 
func(ent *Entry) Encode() []byte {
	// Allocate 4 (checksum) + 4 (key size) + 4 (val size) + 1 (deleted flag) + payload lengths
	// Total Header Size = 13
	data := make([]byte, 4+4+4+1+len(ent.key)+len(ent.val))
	// Write sizes
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[8:12], uint32(len(ent.val)))

	// Write deleted flag 
	if ent.deleted {
		data[12] = 1
	} else {
		data[12] = 0
	}

	copy(data[13:], ent.key) 
	copy(data[13+len(ent.key):], ent.val)

	// Calculate the CRC32 checksum of everything EXCEPT the first 4 bytes.
	// we are hashing -> sizes + deleted flag + actual key + actual val.
	checksum := crc32.ChecksumIEEE(data[4:])
	binary.LittleEndian.PutUint32(data[0:4], checksum)
	return data
}

// 2. Deserialization: parse a byte sequence from an io.Reader back into the Entry Struct.
func (ent *Entry) Decode(r io.Reader) error {
	header := make([]byte, 13) // FIXED: Must be 13 to hold the checksum
	if _, err := io.ReadFull(r, header); err != nil {
		return err 
	}
	savedChecksum := binary.LittleEndian.Uint32(header[0:4])

	// decode the header bytes back into numbers 
	keySize := binary.LittleEndian.Uint32(header[4:8])
	valSize := binary.LittleEndian.Uint32(header[8:12])
	
	ent.deleted = header[12] == 1

	// allocate the exact space needed inside the entry field. 
	ent.key = make([]byte, keySize)
	ent.val = make([]byte, valSize)


	// io.EOF: reached the ned of the file (all records are valid.)
	// io.ErrUnexpectedEOF: filed ended prematurely (file size is incorrect, data not fully written.)

	// Read the raw keys, handling potential stream truncation errors
	if _, err := io.ReadFull(r, ent.key); err != nil {
		return err
	}
	
	// Read the raw values, handling potential stream truncation errors
	if _, err := io.ReadFull(r, ent.val); err != nil {
		return err
	}
	
	// Verification: re-calculate the checksum of the data we just read.
	// we feed the hash the exact same bytes we hashed during Encode().
	hash := crc32.NewIEEE()
	hash.Write(header[4:13])
	hash.Write(ent.key)
	hash.Write(ent.val)

	if hash.Sum32() != savedChecksum {
		return ErrBadSum // indicating torn write or hardware corruption detected.
	} 

	return nil
}

// ============================================================================
// 3. DATABASE ENGINE INTEGRATION
// ============================================================================

type Log struct { // FIXED: Lowercase 's'
	FileName string
	fp *os.File // provides methods for file operations (Read, Write, Close)
}

// Initialization of the connection to the physical file on the disk.
func (log *Log) Open() (err error) {
	// OpenFile returns two: *osFile and error.
	// O_RDWR: Opens the file for reading and writing.
	// O_CREATE: Create the file if it doesn't exist yet. 
	// log.fp, err = os.OpenFile(log.FileName, os.O_RDWR|os.O_CREATE, 0o644) // FIXED: log.FileName and 0o644
	log.fp, err = createFileSync(log.FileName)
	return err
}

func (log *Log) Close() error {
	return log.fp.Close()
}

// Log IO interfaces 
func (log *Log) Write(ent *Entry) error {
	if _, err := log.fp.Write(ent.Encode()); err != nil {
		return err
	}
	return log.fp.Sync() // fsync
}

func (log *Log) Read(ent *Entry) (eof bool, err error) { // FIXED: bool
	err = ent.Decode(log.fp)
	
	
	if err == io.EOF || err == io.ErrUnexpectedEOF || err == ErrBadSum { 
		// 1. Tell the recovery loop to stop gracefully as eof hits.
		return true, nil
	}	else if err != nil { // 2. Fatal system error, crash the DB
		return false, err
	} else {
		return false, nil // 3. Successfully read a valid record, keep going. 
	}
}

type KV struct {
	log Log 
	mem map[string][]byte 
}

func (kv *KV) Open() error {
	if err := kv.log.Open(); err != nil { // FIXED: removed parentheses
		return err
	}
	
	kv.mem = make(map[string][]byte) // empty

	// FIXED: Added the missing recovery loop!
	for {
		ent := &Entry{}
		eof, err := kv.log.Read(ent)
		if err != nil {
			return err
		}
		if eof {
			break
		}

		keyStr := string(ent.key)
		if ent.deleted {
			delete(kv.mem, keyStr)
		} else {
			kv.mem[keyStr] = ent.val
		}
	}

	return nil
}

func (kv *KV) Close() error {
	return kv.log.Close()
}

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	ent := &Entry {
		key: key,
		val: val,
		deleted: false,
	}

	// 1. Write to Disk Log FIRST for durability.
	if err := kv.log.Write(ent); err != nil {
		return false, err 
	}

	// 2. Update RAM SECOND for speed.
	keyStr := string(key)
	_, updated = kv.mem[keyStr]
	kv.mem[keyStr] = val

	return updated, nil
}

// Del removes a key. Reports true if a key was actually removed.
func (kv *KV) Del(key []byte) (deleted bool, err error) {
	ent := &Entry {
		key: key,
		val: nil,
		deleted: true,
	}

	// 1. Write Tombstone to Disk Log FIRST 
	if err := kv.log.Write(ent); err != nil {
		return false, err 
	}

	// 2. Delete from RAM SECOND 
	keyStr := string(key)
	_, deleted = kv.mem[keyStr]
	if deleted {
		delete(kv.mem, keyStr)
	}

	return deleted, nil
}


