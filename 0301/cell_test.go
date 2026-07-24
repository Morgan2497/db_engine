package kv

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func logCell(t *testing.T, step string, cell Cell, extra string) {
	t.Helper()
	switch cell.Type {
	case TypeI64:
		t.Logf("[%s] type=I64 i64=%d | %s", step, cell.I64, extra)
	case TypeStr:
		t.Logf("[%s] type=Str str=%q | %s", step, cell.Str, extra)
	default:
		t.Logf("[%s] type=%d | %s", step, cell.Type, extra)
	}
}

func TestCellI64Roundtrip(t *testing.T) {
	t.Log("=== 0301 Cell I64 roundtrip test start ===")

	cell := Cell{Type: TypeI64, I64: -42}
	logCell(t, "ENCODE input", cell, "int64 is always 8 bytes on wire (no length prefix)")

	buf := (&cell).Encode(nil)
	t.Logf("[ENCODE output] len=%d bytes=%v", len(buf), buf)
	t.Logf("[ENCODE check] two's complement LE for -42 → D6 FF FF FF FF FF FF FF")
	assert.Len(t, buf, 8)

	decoded := Cell{Type: TypeI64}
	t.Log("calling Decode — Type must be TypeI64 before decode")
	rest, err := decoded.Decode(buf)
	t.Logf("[DECODE output] err=%v i64=%d rest_len=%d", err, decoded.I64, len(rest))
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, int64(-42), decoded.I64)

	t.Log("=== 0301 Cell I64 roundtrip test end ===")
}

func TestCellStrRoundtrip(t *testing.T) {
	t.Log("=== 0301 Cell Str roundtrip test start ===")

	cell := Cell{Type: TypeStr, Str: []byte("hello")}
	logCell(t, "ENCODE input", cell, "wire = 4-byte length prefix + string bytes")

	buf := (&cell).Encode(nil)
	t.Logf("[ENCODE output] len=%d bytes=%v", len(buf), buf)
	t.Logf("[ENCODE check] first 4 bytes are len=5 → %v", buf[0:4])
	assert.Equal(t, uint32(5), binary.LittleEndian.Uint32(buf[0:4]))

	decoded := Cell{Type: TypeStr}
	t.Log("calling Decode — reads size from first 4 bytes, then slices payload")
	rest, err := decoded.Decode(buf)
	t.Logf("[DECODE output] err=%v str=%q rest_len=%d", err, decoded.Str, len(rest))
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, []byte("hello"), decoded.Str)

	t.Log("=== 0301 Cell Str roundtrip test end ===")
}

func TestCellChainedDecode(t *testing.T) {
	t.Log("=== 0301 chained cell decode test start ===")

	buf := (&Cell{Type: TypeI64, I64: 1}).Encode(nil)
	t.Logf("[ENCODE cell1] i64(1) → %v", buf)
	buf = (&Cell{Type: TypeStr, Str: []byte("x")}).Encode(buf)
	t.Logf("[ENCODE cell2] append str(\"x\") → %v", buf)

	c1 := Cell{Type: TypeI64}
	rest, err := c1.Decode(buf)
	t.Logf("[DECODE cell1] i64=%d rest=%v err=%v", c1.I64, rest, err)
	assert.NoError(t, err)

	c2 := Cell{Type: TypeStr}
	rest, err = c2.Decode(rest)
	t.Logf("[DECODE cell2] str=%q rest=%v err=%v", c2.Str, rest, err)
	assert.NoError(t, err)
	assert.Empty(t, rest)

	t.Log("=== 0301 chained cell decode test end ===")
}

func TestCellEncodeAppend(t *testing.T) {
	t.Log("=== 0301 encode append (toAppend) test start ===")

	base := []byte("prefix")
	t.Logf("[SETUP] toAppend=%q", base)
	buf := (&Cell{Type: TypeI64, I64: 7}).Encode(base)
	t.Logf("[ENCODE output] len=%d bytes=%v", len(buf), buf)
	assert.True(t, len(buf) > len(base))
	assert.Equal(t, "prefix", string(buf[:6]))

	t.Log("=== 0301 encode append test end ===")
}
