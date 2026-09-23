package db0901

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
	// PKey  []int : the reason is that whether querying a pk or an idx, the first step is the same.
	// query KV which a pk is just a special index. use indices[0] instead.
	Indices [][]int
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
}

row := Row{
	{Type: TypeI64, I64: 123},
	{Type: TypeStr, Str: []byte("a")},
	{Type: TypeStr, Str: []byte("b")},
}
*/

// It seriealizes the pk columns to form the physical KV key.
func (row Row) EncodeKey(schema *Schema, indexNo int) (key []byte) {
	check(len(row) == len(schema.Cols))
	check(indexNo >= 0 && indexNo < len(schema.Indices))

	//1. Prefix: table name + null-byte separator.
	key = append([]byte(schema.Table), 0x00, byte(indexNo))

	for _, idx := range schema.Indices[indexNo] {
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
		if !slices.Contains(schema.Indices[0], idx) {
			check(value.Type == schema.Cols[idx].Type)
			val = row[idx].EncodeVal(val)
		}
	}
	return val
}

var ErrOutOfRange = errors.New("out of range")

func (row Row) DecodeKey(schema *Schema, indexNo int, key []byte) (err error) {
	check(len(row) == len(schema.Cols))
	check(indexNo >= 0 && indexNo < len(schema.Indices))
	// 1. Take the prefix ([ 'l', 'i', 'n', 'k', 0x00, index]) 4 + 1 + 1= 6
	// the sorted key must begin with: table name + 0x00
	prefixLen := len(schema.Table) + 2

	if len(key) < prefixLen {
		return ErrOutOfRange
	}

	expectedPrefix := schema.Table + "\x00" + string(byte(indexNo))

	if string(key[:prefixLen]) != expectedPrefix {
		return ErrOutOfRange
	}

	// Excluded the table name and only get the keys in bytes.
	key = key[prefixLen:]

	check(len(row) == len(schema.Cols))

	for _, idx := range schema.Indices[indexNo] {
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
		if !slices.Contains(schema.Indices[0], idx) {
			row[idx].Type = schema.Cols[idx].Type

			val, err = row[idx].DecodeVal(val)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func EncodeKeyPrefix(schema *Schema, indexNo int, prefix []Cell, positive bool) (key []byte) {
	check(indexNo >= 0 && indexNo < len(schema.Indices))
	check(len(prefix) <= len(schema.Indices[indexNo]))

	// table name + separate + index namespace
	key = append([]byte(schema.Table), 0x00, byte(indexNo))

	for i, cell := range prefix {
		columnIndex := schema.Indices[indexNo][i]
		check(cell.Type == schema.Cols[columnIndex].Type)
		key = append(key, byte(cell.Type)) // avoid 0xff
		key = cell.EncodeKey(key)
	}

	if positive {
		key = append(key, 0xff) // +infinity
	}

	return key // -infinity
}
