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

	// 3. Expected bytes for the Chapter 0405 key format:
	//    table + separator + typed PK cells + full-key terminator.
	tablePrefix := []byte{'l', 'i', 'n', 'k', 0}
	dstCell := []byte{byte(TypeStr), 'b', 0}
	srcCell := []byte{byte(TypeStr), 'a', 0}
	fullKeyTerminator := []byte{0}

	t.Logf("[KEY 1] table prefix:          % x  (%q + 0x00)", tablePrefix, schema.Table)
	t.Logf("[KEY 2] dst type marker:        %02x  (TypeStr=%d)", byte(TypeStr), TypeStr)
	t.Logf("[KEY 3] encoded dst cell:       % x  ([type][b][string-end])", dstCell)
	t.Logf("[KEY 4] src type marker:        %02x  (TypeStr=%d)", byte(TypeStr), TypeStr)
	t.Logf("[KEY 5] encoded src cell:       % x  ([type][a][string-end])", srcCell)
	t.Logf("[KEY 6] full-key terminator:    % x", fullKeyTerminator)

	key := slices.Concat(tablePrefix, dstCell, srcCell, fullKeyTerminator)
	actualKey := row.EncodeKey(schema)

	t.Logf("[KEY EXPECTED] % x", key)
	t.Logf("[KEY ACTUAL]   % x", actualKey)
	t.Logf("[KEY LAYOUT]   link\\x00 | TypeStr b\\x00 | TypeStr a\\x00 | full-key 0x00")

	// 123 as 8-byte LittleEndian
	val := []byte{123, 0, 0, 0, 0, 0, 0, 0}
	t.Logf("[VALUE] non-PK column time=123: % x", val)

	// 4. Assert Encode
	assert.Equal(t, key, actualKey)
	assert.Equal(t, val, row.EncodeVal(schema))

	// 5. Assert Decode (The Round-Trip)
	t.Log("[DECODE] decoding the new key format back into a Row")
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
	for rowIndex := range rows {
		row = rows[rowIndex]
		key = row.EncodeKey(schema)
		keys = append(keys, string(key))
		t.Logf(
			"[SORT %d] PK=(dst=%q, src=%q) encoded=% x",
			rowIndex+1,
			row[2].Str,
			row[1].Str,
			key,
		)

		decoded = schema.NewRow()
		err = decoded.DecodeKey(schema, key)
		assert.Nil(t, err)
		err = decoded.DecodeVal(schema, val)
		assert.Nil(t, err)
		assert.Equal(t, row, decoded)
	}
	t.Logf("[SORT RESULT] encoded keys sorted=%t", slices.IsSorted(keys))
	assert.True(t, slices.IsSorted(keys))
}
