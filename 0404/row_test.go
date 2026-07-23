package kv

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRowEncode(t *testing.T) {
	// 1. Setup: Define the blueprint
	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{2, 1}, // (dst, src)
	}

	// 2. Data: Create a row to test
	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}

	// 3. Expected Bytes: null-terminated order-preserving key encoding
	key := []byte{'l', 'i', 'n', 'k', 0, 'b', 0, 'a', 0}
	// 123 as 8-byte LittleEndian
	val := []byte{123, 0, 0, 0, 0, 0, 0, 0}

	// 4. Assert Encode
	assert.Equal(t, key, row.EncodeKey(schema))
	assert.Equal(t, val, row.EncodeVal(schema))

	// 5. Assert Decode (The Round-Trip)
	decoded := schema.NewRow()

	err := decoded.DecodeKey(schema, key)
	assert.Nil(t, err)

	err = decoded.DecodeVal(schema, val)
	assert.Nil(t, err)

	assert.Equal(t, row, decoded)

	rows := []Row{
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("ba")},
			Cell{Type: TypeStr, Str: []byte("b")},
		},
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("a")},
			Cell{Type: TypeStr, Str: []byte("bb")},
		},
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("a")},
			Cell{Type: TypeStr, Str: []byte("bba")},
		},
	}
	keys := []string{}
	for _, row = range rows {
		key = row.EncodeKey(schema)
		keys = append(keys, string(key))

		decoded = schema.NewRow()
		err = decoded.DecodeKey(schema, key)
		assert.Nil(t, err)
		err = decoded.DecodeVal(schema, val)
		assert.Nil(t, err)
		assert.Equal(t, row, decoded)
	}
	assert.True(t, slices.IsSorted(keys))
}
