package kv

import (
	"encoding/binary"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCellI64Roundtrip(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -42}
	buf := (&cell).EncodeVal(nil)
	assert.Len(t, buf, 8)

	decoded := Cell{Type: TypeI64}
	rest, err := decoded.DecodeVal(buf)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, int64(-42), decoded.I64)
}

func TestCellStrRoundtrip(t *testing.T) {
	cell := Cell{Type: TypeStr, Str: []byte("hello")}
	buf := (&cell).EncodeVal(nil)
	assert.Equal(t, uint32(5), binary.LittleEndian.Uint32(buf[0:4]))

	decoded := Cell{Type: TypeStr}
	rest, err := decoded.DecodeVal(buf)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, []byte("hello"), decoded.Str)
}

func TestCellChainedDecode(t *testing.T) {
	buf := (&Cell{Type: TypeI64, I64: 1}).EncodeVal(nil)
	buf = (&Cell{Type: TypeStr, Str: []byte("x")}).EncodeVal(buf)

	c1 := Cell{Type: TypeI64}
	rest, err := c1.DecodeVal(buf)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), c1.I64)

	c2 := Cell{Type: TypeStr}
	rest, err = c2.DecodeVal(rest)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, "x", string(c2.Str))
}

func TestCellEncodeAppend(t *testing.T) {
	base := []byte("prefix")
	buf := (&Cell{Type: TypeI64, I64: 7}).EncodeVal(base)
	assert.True(t, len(buf) > len(base))
	assert.Equal(t, "prefix", string(buf[:6]))
}

func randString() (out []byte) {
	sz := rand.IntN(256)
	for i := 0; i < sz; i++ {
		out = append(out, byte(rand.Uint32N(256)))
	}
	return out
}

func TestTableCellKey(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -2}
	data := []byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}
	assert.Equal(t, data, cell.EncodeKey(nil))
	decoded := Cell{Type: TypeI64}
	rest, err := decoded.DecodeKey(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)

	outKeys := []string{}
	for i := -2; i <= 2; i++ {
		cell = Cell{Type: TypeI64, I64: int64(i)}
		outKeys = append(outKeys, string(cell.EncodeKey(nil)))
	}
	assert.True(t, slices.IsSorted(outKeys))

	cell = Cell{Type: TypeStr, Str: []byte("a\x00s\x01d\x02f")}
	data = []byte{'a', 0x01, 0x01, 's', 0x01, 0x02, 'd', 0x02, 'f', 0}
	assert.Equal(t, data, cell.EncodeKey(nil))
	decoded = Cell{Type: TypeStr}
	rest, err = decoded.DecodeKey(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)

	strKeys := []string{}
	for i := 0; i < 10000; i++ {
		strKeys = append(strKeys, string(randString()))
	}
	slices.Sort(strKeys)

	outKeys = []string{}
	for _, s := range strKeys {
		cell := Cell{Type: TypeStr, Str: []byte(s)}
		outKeys = append(outKeys, string(cell.EncodeKey(nil)))

		decoded = Cell{Type: TypeStr}
		rest, err = decoded.DecodeKey([]byte(outKeys[len(outKeys)-1]))
		assert.True(t, len(rest) == 0 && err == nil && string(decoded.Str) == s)
	}
	assert.True(t, slices.IsSorted(outKeys))
}
