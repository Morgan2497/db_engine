package kv

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func logRow(t *testing.T, step string, row Row, extra string) {
	t.Helper()
	parts := make([]string, len(row))
	for i, c := range row {
		switch c.Type {
		case TypeI64:
			parts[i] = "I64:" + strconv.FormatInt(c.I64, 10)
		case TypeStr:
			parts[i] = "Str:" + string(c.Str)
		default:
			parts[i] = "Type0"
		}
	}
	t.Logf("[%s] row=%v | %s", step, parts, extra)
}

func TestRowEncode(t *testing.T) {
	t.Log("=== 0301 Row Encode/Decode test start ===")
	t.Log("layout: KV key = table\\0 + PK cells; KV val = non-PK cells")

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2},
	}
	t.Logf("[SETUP] table=%q PKey=%v", schema.Table, schema.PKey)

	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "ENCODE input", row, "time=123, src=a, dst=b")

	wantKey := []byte{'l', 'i', 'n', 'k', 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0, 'b'}
	wantVal := []byte{123, 0, 0, 0, 0, 0, 0, 0}
	t.Logf("[ENCODE expect KEY] %v", wantKey)
	t.Logf("[ENCODE expect VAL] %v", wantVal)

	gotKey := row.EncodeKey(schema)
	gotVal := row.EncodeVal(schema)
	t.Logf("[ENCODE output KEY] len=%d %v", len(gotKey), gotKey)
	t.Logf("[ENCODE output VAL] len=%d %v", len(gotVal), gotVal)
	assert.Equal(t, wantKey, gotKey)
	assert.Equal(t, wantVal, gotVal)

	decoded := schema.NewRow()
	logRow(t, "DECODE start", decoded, "blank NewRow()")
	assert.NoError(t, decoded.DecodeKey(schema, gotKey))
	logRow(t, "after DecodeKey", decoded, "PK filled")
	assert.NoError(t, decoded.DecodeVal(schema, gotVal))
	logRow(t, "DECODE result", decoded, "should match original")
	assert.Equal(t, row, decoded)

	t.Log("=== 0301 Row Encode/Decode test end ===")
}
