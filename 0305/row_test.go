package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRowEncode(t *testing.T) {
	// 1. Setup: Define the blueprint
	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src",  Type: TypeStr},
			{Name: "dst",  Type: TypeStr},
		},
		PKey: []int{1, 2}, // (src, dst) are the key
	}

	// 2. Data: Create a row to test
	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}

	// 3. Expected Bytes: The hard-coded physical storage format
	// 'link' + 0x00 + len(1) + 'a' + len(1) + 'b'
	key := []byte{'l', 'i', 'n', 'k', 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0, 'b'}
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
	
	// The final check: Does the decoded row match the original?
	assert.Equal(t, row, decoded)
}
