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
	t.Log("=== 0202 Row Encode/Decode test start ===")
	t.Log("layout rule: KV key = table\\0 + PK cells; KV val = non-PK cells")

	// 1. Setup: Define the blueprint
	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2}, // (src, dst) are the key
	}
	t.Logf("[SETUP] table=%q cols=[time:i64, src:str, dst:str] PKey=%v", schema.Table, schema.PKey)
	t.Log("[SETUP] column 0 (time) is NON-PK → goes in value; cols 1,2 are PK → go in key")

	// 2. Data: Create a row to test
	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "ENCODE input", row, "time=123, src=\"a\", dst=\"b\"")

	// 3. Expected Bytes: The hard-coded physical storage format
	// 'link' + 0x00 + len(1) + 'a' + len(1) + 'b'
	wantKey := []byte{'l', 'i', 'n', 'k', 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0, 'b'}
	// 123 as 8-byte LittleEndian
	wantVal := []byte{123, 0, 0, 0, 0, 0, 0, 0}

	t.Logf("[ENCODE expect KEY] table \"link\" + 0x00 + Enc(src) + Enc(dst)")
	t.Logf("[ENCODE expect KEY] bytes=%v", wantKey)
	t.Logf("[ENCODE expect KEY] breakdown: 'l''i''n''k' 0x00 | len=1 'a' | len=1 'b'")
	t.Logf("[ENCODE expect VAL] only non-PK time=123 as LE int64")
	t.Logf("[ENCODE expect VAL] bytes=%v", wantVal)

	gotKey := row.EncodeKey(schema)
	gotVal := row.EncodeVal(schema)
	t.Logf("[ENCODE output KEY] len=%d bytes=%v", len(gotKey), gotKey)
	t.Logf("[ENCODE output VAL] len=%d bytes=%v", len(gotVal), gotVal)

	assert.Equal(t, wantKey, gotKey)
	assert.Equal(t, wantVal, gotVal)
	t.Log("[ENCODE check] key and val match expected wire format")

	// 4. Assert Decode (The Round-Trip)
	t.Log("calling schema.NewRow() — blank cells (Type=0) sized to schema")
	decoded := schema.NewRow()
	logRow(t, "DECODE start", decoded, "blank slate before DecodeKey/DecodeVal")

	t.Log("calling DecodeKey — strips table\\0 prefix, fills PK cells (src, dst)")
	err := decoded.DecodeKey(schema, gotKey)
	t.Logf("[DECODE KEY output] err=%v", err)
	assert.NoError(t, err)
	logRow(t, "DECODE after key", decoded, "PK filled; time still empty")

	t.Log("calling DecodeVal — fills non-PK cells (time)")
	err = decoded.DecodeVal(schema, gotVal)
	t.Logf("[DECODE VAL output] err=%v", err)
	assert.NoError(t, err)
	logRow(t, "DECODE result", decoded, "should match original row")

	assert.Equal(t, row, decoded)
	t.Logf("[DECODE check] time=%d src=%q dst=%q", decoded[0].I64, decoded[1].Str, decoded[2].Str)

	t.Log("=== 0202 Row Encode/Decode test end ===")
}

func TestNewRow(t *testing.T) {
	t.Log("=== 0202 NewRow test start ===")

	schema := &Schema{
		Table: "user",
		Cols: []Column{
			{Name: "id", Type: TypeI64},
			{Name: "name", Type: TypeStr},
		},
		PKey: []int{0},
	}
	t.Logf("[SETUP] table=%q cols=%d PKey=%v", schema.Table, len(schema.Cols), schema.PKey)

	row := schema.NewRow()
	t.Logf("[NewRow output] len=%d (must equal len(schema.Cols))", len(row))
	assert.Len(t, row, 2)

	logRow(t, "NewRow cells", row, "each cell starts as zero value until typed+filled")
	for i, c := range row {
		t.Logf("[CHECK] col[%d] Type=%d I64=%d Str=%v", i, c.Type, c.I64, c.Str)
		assert.Equal(t, CellType(0), c.Type)
		assert.Equal(t, int64(0), c.I64)
		assert.Nil(t, c.Str)
	}

	t.Log("=== 0202 NewRow test end ===")
}

func TestDecodeKeyTooShort(t *testing.T) {
	t.Log("=== 0202 DecodeKey too-short prefix test start ===")

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2},
	}
	t.Logf("[SETUP] table=%q needs prefixLen=len(table)+1=%d bytes before PK payload",
		schema.Table, len(schema.Table)+1)

	row := schema.NewRow()
	short := []byte{'l', 'i'} // shorter than "link" + 0x00
	t.Logf("[DECODE KEY input] bytes=%v len=%d — truncated table prefix", short, len(short))

	err := row.DecodeKey(schema, short)
	t.Logf("[DECODE KEY output] err=%v", err)
	assert.Error(t, err)
	assert.EqualError(t, err, "key too short")

	t.Log("=== 0202 DecodeKey too-short prefix test end ===")
}

func TestTablePrefixIsolation(t *testing.T) {
	t.Log("=== 0202 table prefix isolation test start ===")
	t.Log("null byte after table name prevents 'ab' colliding with 'abc' prefixes")

	schemaAB := &Schema{
		Table: "ab",
		Cols:  []Column{{Name: "id", Type: TypeI64}},
		PKey:  []int{0},
	}
	schemaABC := &Schema{
		Table: "abc",
		Cols:  []Column{{Name: "id", Type: TypeI64}},
		PKey:  []int{0},
	}

	row := Row{{Type: TypeI64, I64: 1}}
	logRow(t, "ENCODE input", row, "same logical PK id=1 for both tables")

	keyAB := row.EncodeKey(schemaAB)
	keyABC := row.EncodeKey(schemaABC)
	t.Logf("[ENCODE ab]  bytes=%v", keyAB)
	t.Logf("[ENCODE abc] bytes=%v", keyABC)
	t.Logf("[ENCODE check] ab  starts with %q + 0x00", schemaAB.Table)
	t.Logf("[ENCODE check] abc starts with %q + 0x00", schemaABC.Table)

	assert.NotEqual(t, keyAB, keyABC)
	assert.Equal(t, byte(0), keyAB[len(schemaAB.Table)])
	assert.Equal(t, byte(0), keyABC[len(schemaABC.Table)])
	t.Log("[CHECK] keys differ solely because table\\0 namespaces isolate them")

	t.Log("=== 0202 table prefix isolation test end ===")
}

func TestRowRoundtripViaEncodeOutput(t *testing.T) {
	t.Log("=== 0202 roundtrip using EncodeKey/EncodeVal output (not hand-built bytes) ===")

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2},
	}

	row := Row{
		{Type: TypeI64, I64: 1700000000},
		{Type: TypeStr, Str: []byte("nodeA")},
		{Type: TypeStr, Str: []byte("nodeB")},
	}
	logRow(t, "ENCODE input", row, "README-style example values")

	key := row.EncodeKey(schema)
	val := row.EncodeVal(schema)
	t.Logf("[ENCODE output KEY] len=%d bytes=%v", len(key), key)
	t.Logf("[ENCODE output VAL] len=%d bytes=%v", len(val), val)
	t.Logf("[ENCODE layout KEY] \"link\"\\0 + str(\"nodeA\") + str(\"nodeB\")")
	t.Logf("[ENCODE layout VAL] i64(1700000000) only")

	decoded := schema.NewRow()
	assert.NoError(t, decoded.DecodeKey(schema, key))
	assert.NoError(t, decoded.DecodeVal(schema, val))
	logRow(t, "DECODE result", decoded, "must equal original")

	assert.Equal(t, row, decoded)
	t.Log("=== 0202 roundtrip via encode output test end ===")
}
