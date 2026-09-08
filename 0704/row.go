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

/*
* schema := &Schema{
	Table: "link",
	Cols: []Column{
		{Name: "time", Type: TypeI64},
		{Name: "src", Type: TypeStr},
		{Name: "dst", Type: TypeStr},
	},
	PKey: []int{2, 1}, // (dst, src)
}

row := Row{
	{Type: TypeI64, I64: 123},
	{Type: TypeStr, Str: []byte("a")},
	{Type: TypeStr, Str: []byte("b")},
}
*/

// It seriealizes the pk columns to form the physical KV key.
func (row Row) EncodeKey(schema *Schema) (key []byte) {
	//1. Prefix: table name + null-byte separator.
	key = append([]byte(schema.Table), 0x00)

	check(len(row) == len(schema.Cols))

	for _, idx := range schema.PKey {
		value := row[idx]
		// Ensure the cell type matches the shcema definition.
		check(value.Type == schema.Cols[idx].Type)

		// Mark the beginning and type of this primary-key column.
		key = append(key, byte(value.Type)) // avoid 0xff

		// Append the order-preserving cell data.
		key = value.EncodeKey(key)

	}
	return append(key, 0x00) // > -infinity
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
	// the sorted key must begin with: table name + 0x00
	prefixLen := len(schema.Table) + 1

	if len(key) < prefixLen {
		return ErrOutOfRange
	}

	expectedPrefix := schema.Table + "\x00"

	if string(key[:prefixLen]) != expectedPrefix {
		return ErrOutOfRange
	}

	// Excluded the table name and only get the keys in bytes.
	key = key[prefixLen:]

	check(len(row) == len(schema.Cols))

	for _, idx := range schema.PKey {
		// Every encoded PK cell must begin with one type byte.
		// TypeI64: 1 = 0x01
		// TypeStr: 2 = 0x02
		if len(key) < 1 {
			return errors.New("missing primary-key type marker")
		}

		// Read the type marker from the encoded key.
		encodedType := CellType(key[0])

		// Get the type expected by the schema.
		expectedType := schema.Cols[idx].Type

		// A stored key claming a different type is malformed.
		if encodedType != expectedType {
			return errors.New("primary-key type marker does not match schema.")
		}

		// Remove the type marker before decoding the cell value.
		key = key[1:]

		// DecodeKey needs to know which cell decoder to use.
		row[idx].Type = expectedType

		// Decode the cell and kepp the unread bytes.
		key, err = row[idx].DecodeKey(key)

		if err != nil {
			return err
		}
	}

	// After every PK cell has been decoded, exactly one byte should remain: the full-key 0x00 terminator.
	if len(key) != 1 || key[0] != 0x00 {
		return errors.New("invalid or missing full-key terminator.")
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

func EncodeKeyPrefix(schema *Schema, prefix []Cell, positive bool) (key []byte) {
	key = append([]byte(schema.Table), 0x00)

	for _, cell := range prefix {
		key = append(key, byte(cell.Type)) // avoid 0xff
		key = cell.EncodeKey(key)
	}

	if positive {
		key = append(key, 0xff) // +infinity
	}

	return key // -infinity
}
