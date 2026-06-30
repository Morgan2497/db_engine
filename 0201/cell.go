package kv 
import (
	"encoding/binary"
	"errors"
)

// CellType acts as our enum to differentiate the data inside the Cell struct.
type CellType uint8

const (
	TypeI64 CellType = 1
	TypeStr CellType = 2
)

// Cell simulates a C-style union, trading a small amount of RAM for high-speed. type-safe execution.
type Cell struct {
	Type CellType
	I64 int64
	Str []byte
}

/*
* offset := len(toAppend)
* we measure how long the slice currently is. If it already has 15 bytes in it from previous columns,
* len() returns 15. This tells us that the next one should be placed starting exactly at idx 15.
* Otherwise, we would overwrite the previous column's data. 
*/ 


func (cell *Cell) Encode(toAppend []byte) []byte {
	switch cell.Type {
	case TypeI64:
		// Need 8 bytes for a 64-bit integer.
		offset := len(toAppend)

		// Extend the slice capacity by 8 bytes.
		toAppend = append(toAppend, make([]byte, 8)...)

		// Cast the signed int64 to an unsigned uint64 (zero CPU cost) so binary.LittleEndian can handle it.
		// if it was -5 -> we will store it as 5.
		// 5 in 64-bit binary is 61 zeros followed by 101.
		// [00000000] [00000000] [00000000] [00000000] [00000000] [00000000] [00000000] [00000101]
		// Two's Complement (Flip the bits)
		// To make it negative, the computer first flips every single 0 to a 1, and every 1 to a 0.
		// [11111111] [11111111] [11111111] [11111111] [11111111] [11111111] [11111111] [11111010]
		// Two's Complement (Add 1)
		// Then, it adds exactly 1 to the final result.
		// [11111111] [11111111] [11111111] [11111111] [11111111] [11111111] [11111111] [11111011]
		// 255        255           ...       ...        ...       ...         ...        251
		// it reverses the order using LittleEndian.
		binary.LittleEndian.PutUint64(toAppend[offset:], uint64(cell.I64))

		return toAppend
	
	case TypeStr:
		// We need 4 bytes for the string length header.
		offset := len(toAppend)
		toAppend = append(toAppend, make([]byte, 4)...)

		// Write the length of the string into those 4 bytes.
		binary.LittleEndian.PutUint32(toAppend[offset:], uint32(len(cell.Str)))
		toAppend = append(toAppend, cell.Str...)

		return toAppend

	default:
		panic("Unknown cell type")
	} 
}

func (cell *Cell) Decode(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		// An int64 must be 8 bytes. 
		if len(data) < 8 {
			return nil, errors.New("not enough data for int64")
		}
		cell.I64 = int64(binary.LittleEndian.Uint64(data[:8]))

		return data[8:], nil

	case TypeStr:
		if len(data) < 4 {
			return nil, errors.New("not enough data for string header")
		}
		// To find the str len in header section.
		strLen := int(binary.LittleEndian.Uint32(data[:4]))
		cell.Str = make([]byte, strLen)
		copy(cell.Str, data[4 : 4+strLen])

		return data[4+strLen:], nil

	default:
		return nil, errors.New("unknown cell type")
	}
}
