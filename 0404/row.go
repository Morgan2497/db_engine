package kv

import (
	"errors"
	"slices"
)

func check(cond bool) {
	if !cond {
		panic("check failed")
	}
}

// Column defines the name and expected data type for a single field in a table.
type Column struct {
	Name string
	Type CellType
}

// Schema defines the complete blueprint of a table.
type Schema struct {
	Table string
	Cols  []Column
	PKey  []int // Which columns are the primary key?
}

// Encode a Row as KV
// When user types an SQL to insert into a table, it creates this first like schema.NewRow()
// {Type: 0, I64: 0, str/int: nil,}

type Row []Cell

func (schema *Schema) NewRow() Row {
	return make(Row, len(schema.Cols))
}

// It seriealizes the pk columns to form the physical KV key.
func (row Row) EncodeKey(schema *Schema) (key []byte) {
	// 1. Prefix: table name + null-byte separator.
	key = append([]byte(schema.Table), 0x00)

	check(len(row) == len(schema.Cols))

	for _, idx := range schema.PKey {
		value := row[idx]
		// Ensure the cell type matches the shcema definition.
		check(value.Type == schema.Cols[idx].Type)
		key = row[idx].EncodeKey(key)
	}
	return key
}

// It serializes all non-primary key columns to form the physical KV value.
func (row Row) EncodeVal(schema *Schema) (val []byte) {
	// 1. Protect the engine from malformed rows.
	check(len(row) == len(schema.Cols))

	// 2. Iterate sequantially to guarantee strict column ordering.
	for idx, value := range row {
		// 3. If not pk, then proceed.
		if !slices.Contains(schema.PKey, idx) {
			check(value.Type == schema.Cols[idx].Type)
			val = row[idx].EncodeVal(val)
		}
	}
	return val
}

var ErrOutOfRange = errors.New("out of range")
func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
	// 1. Take the prefix ([ 'l', 'i', 'n', 'k', 0x00]) 4 + 1 = 5
	//                                           ^^^^
	prefixLen := len(schema.Table) + 1

	if len(key) < prefixLen {
		return ErrOutOfRange
	}
	
	if string(key[:len(schema.Table)+1]) != schema.Table+"\x00" {
		return ErrOutOfRange
	}
	// Excluded the table name and only get the keys in bytes.
	key = key[prefixLen:]

	check(len(row) == len(schema.Cols))

	for _, idx := range schema.PKey {
		// the empty cell is currently a black slate. ex: NewRow() ran, it created a Cell that was a black slate: {Type: 0, I64: 0, str: nil}
		// so need to make sure that what type it is supposed to be before calling decode func in cell.go.
		row[idx].Type = schema.Cols[idx].Type
		key, err = row[idx].DecodeKey(key)

		if err != nil {
			return err
		}
	}
	return nil
}

func (row Row) DecodeVal(schema *Schema, val []byte) (err error) {
	check(len(row) == len(schema.Cols))

	// Unpacking the bytes.
	for idx := range row {
		// we will decode only if it is non-primary key.
		if !slices.Contains(schema.PKey, idx) {
			row[idx].Type = schema.Cols[idx].Type

			val, err = row[idx].DecodeVal(val)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
