package kv

import (
	"encoding/binary"
	"errors"
	"io"
)

type CellType uint8

const (
	TypeI64 CellType = 1
	TypeStr CellType = 2
)

type Cell struct {
	Type CellType
	I64  int64
	Str  []byte
}

var ErrUnknownCellType = errors.New("unknown cell type")

func (cell *Cell) EncodeVal(toAppend []byte) []byte {
	switch cell.Type {
	case TypeI64:
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(cell.I64))
		return append(toAppend, buf[:]...)
	case TypeStr:
		toAppend = binary.LittleEndian.AppendUint32(toAppend, uint32(len(cell.Str)))
		return append(toAppend, cell.Str...)
	default:
		return toAppend
	}
}

func (cell *Cell) DecodeVal(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		if len(data) < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		cell.I64 = int64(binary.LittleEndian.Uint64(data[:8]))
		return data[8:], nil
	case TypeStr:
		if len(data) < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		size := int(binary.LittleEndian.Uint32(data[:4]))
		if len(data) < 4+size {
			return nil, io.ErrUnexpectedEOF
		}
		cell.Str = data[4 : 4+size]
		return data[4+size:], nil
	default:
		return nil, ErrUnknownCellType
	}
}

func encodeStrKey(toAppend []byte, input []byte) []byte {
	for _, ch := range input {
		if ch == 0x00 || ch == 0x01 {
			toAppend = append(toAppend, 0x01, ch+1)
		} else {
			toAppend = append(toAppend, ch)
		}
	}
	return append(toAppend, 0x00)
}
{0x23, 0x67, 0x6F, 0x01, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x99}
func decodeStrKey(data []byte) (out []byte, rest []byte, err error) {
	idx := 0 
	for idx < len(data) {
		if data[idx] == 0x00 {
			// structural boundary reached. Return the decoded string and unread bytes.
			return out, data[idx+1:], nil
		}
		if data[idx] == 0x01 {
			// Escape marker detected. Step over it.
			idx++
			if idx >= len(data) {
				return nil, nil, errors.New("unexpected EOF during escape sequence.")
			}
			// Restore the original byte by subtracting 1 
			out = append(out, data[idx]-1)
		} else {
			// Normal character
			out = append(out, data[idx])
		}
		idx++
	}
	return nil, nil, errors.New("missing null terminator.")
}

// This function intercepts a logical database cell (specifically the Primary Key) and
// trasnforms it into an order-preserving, raw bytr slice. It ensures that when the underlying
// LSM-Tree engine executes bytes.Compare(), the physical byte layout perfectly mirrors the
// logical sorting order of the inteter.
/*
cell := &Cell{
	Type: TypeI64,
	I64:  2,
	Str:  nil,
}
// The table prefix ("users" + null-terminator) has already been written to the buffer
toAppend := []byte{0x75, 0x73, 0x65, 0x72, 0x73, 0x00}
*/
func (cell *Cell) EncodeKey(toAppend []byte) []byte {
	// we threw away the binary.LittleEndian.AppendUint32 logic entirely here.
	// Instead of telling the database how long the string is, we shifted to
	// C style Null-Terminated architecture.
	switch cell.Type {
	case TypeI64:
		// Map signed int64 to unsigned space by flippint the Most Significant Bit 
		unsigned = uint64(cell.I64) ^ (1 << 63)
		return binary.BigEndian.AppendUint64(toAppend, unsigned)

	case TypeStr:
		return encodeStrKey(toAppend, cell.Str)
	}

	default:
		return toAppend
	}
}

func (cell *Cell) DecodeKey(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		// we are decoding it in int . It takes up exactly 64 bits of memory.
		// Since there are 8 its in a single byte, we divide 64 by 8.

		if len(data) < 8 {
			return nil, errors.New("unexpected EOF")
		}
		// Read the 8 bytes and reverse the MSB flip.
		unsigned := binary.BigEndian.Uint64(data[:8])
		cell.I64 = int64(unsigned ^ (1 << 63))
		return data[:8], nil

	case TypeStr:
		out, rest, err := decodeStrKey(data)
		if err != nil {
			return nil, err
		}
		cell.Str = out
		return rest, nil
	
	default:
		return data, errors.New("unknown cell type")
	}
}

