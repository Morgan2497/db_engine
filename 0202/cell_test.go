package kv

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCellI64Roundtrip(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -42}
	buf := (&cell).Encode(nil)
	assert.Len(t, buf, 8)

	decoded := Cell{Type: TypeI64}
	rest, err := decoded.Decode(buf)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, int64(-42), decoded.I64)
}

func TestCellStrRoundtrip(t *testing.T) {
	cell := Cell{Type: TypeStr, Str: []byte("hello")}
	buf := (&cell).Encode(nil)
	assert.Equal(t, uint32(5), binary.LittleEndian.Uint32(buf[0:4]))

	decoded := Cell{Type: TypeStr}
	rest, err := decoded.Decode(buf)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, []byte("hello"), decoded.Str)
}

func TestCellChainedDecode(t *testing.T) {
	buf := (&Cell{Type: TypeI64, I64: 1}).Encode(nil)
	buf = (&Cell{Type: TypeStr, Str: []byte("x")}).Encode(buf)

	c1 := Cell{Type: TypeI64}
	rest, err := c1.Decode(buf)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), c1.I64)

	c2 := Cell{Type: TypeStr}
	rest, err = c2.Decode(rest)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, "x", string(c2.Str))
}

func TestCellEncodeAppend(t *testing.T) {
	base := []byte("prefix")
	buf := (&Cell{Type: TypeI64, I64: 7}).Encode(base)
	assert.True(t, len(buf) > len(base))
	assert.Equal(t, "prefix", string(buf[:6]))
}
