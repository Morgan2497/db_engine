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

