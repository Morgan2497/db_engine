package kv

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"slices"
)

type KVMetaStore struct {
	slots [2]KVMetaItem
	MultiClosers
}

type KVMetaItem struct {
	FileName string
	fp       *os.File
	data     KVMetaData
}

type KVMetaData struct {
	Version uint64
	SSTable string // file name
}

func (meta *KVMetaStore) Open() error {
	for i := range meta.slots {
		fp, data, err := openMetafile(meta.slots[i].FileName)
		if err != nil {
			_ = meta.Close()
			return err
		}
		meta.slots[i].fp, meta.slots[i].data = fp, data
		meta.MultiClosers = append(meta.MultiClosers, fp)
	}
	return nil
}

func openMetafile(filename string) (fp *os.File, data KVMetaData, err error) {
	if fp, err = createFileSync(filename); err != nil {
		return nil, KVMetaData{}, err
	}

	if data, err = readMetaFile(fp); err != nil {
		_ = fp.Close()
		return nil, KVMetaData{}, err
	}

	return fp, data, nil
}

func writeMetaFile(fp *os.File, data KVMetaData) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// {"Version":124,"SSTable":"sstable_124"}
	// [0 0 0 0 | 0 0 0 0 | 123 34 86 101 114 115 ...]
	// checksum		size				JSON payload
	b = slices.Concat(make([]byte, 8), b)

	// payload length -> size
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))

	// checksum length
	binary.LittleEndian.PutUint32(b[0:4], crc32.ChecksumIEEE(b[4:]))

	if _, err = fp.WriteAt(b, 0); err != nil {
		return err
	}

	return fp.Sync()
}

func readMetaFile(fp *os.File) (data KVMetaData, err error) {
	b, err := io.ReadAll(fp)
	if err != nil {
		return KVMetaData{}, err
	}

	/*
	   | checksum | payload size | JSON payload |
	   | 4 bytes  |   4 bytes    | size bytes   |
	*/
	// at least 8 bytes?
	// 1. no
	if len(b) <= 8 {
		return KVMetaData{}, nil
	}

	// 2. yes
	// decode the checksum && payload size.
	sum := binary.LittleEndian.Uint32(b[0:4])
	size := binary.LittleEndian.Uint32(b[4:8])

	if len(b) < 8+int(size) {
		return KVMetaData{}, nil
	}

	if sum != crc32.ChecksumIEEE(b[4:8+size]) {
		return KVMetaData{}, nil
	}

	if err = json.Unmarshal(b[8:8+size], &data); err != nil {
		return KVMetaData{}, nil
	}
	return data, nil
}

func (meta *KVMetaStore) current() int {
	if meta.slots[0].data.Version > meta.slots[1].data.Version {
		return 0
	} else {
		return 1
	}
}

func (meta *KVMetaStore) Get() KVMetaData {
	return meta.slots[meta.current()].data
}

/*
slot 0 -> [ version 124 | data yyy | crc32 ] - newer
slot 1 -> [ version 123 | data zzz | crc32 ]
*/
func (meta *KVMetaStore) Set(data KVMetaData) error {
	cur := meta.current()
	if err := writeMetaFile(meta.slots[1-cur].fp, data); err != nil {
		return err
	}

	// always overwrite in the older slot to prevent atomicity.
	meta.slots[1-cur].data = data
	return nil
}
