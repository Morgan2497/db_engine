package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	p := NewParser(" a b0 _0_ 123 ")
	name, ok := p.tryName()
	assert.True(t, ok && name == "a")
	name, ok = p.tryName()
	assert.True(t, ok && name == "b0")
	name, ok = p.tryName()
	assert.True(t, ok && name == "_0_")
	_, ok = p.tryName()
	assert.False(t, ok)
}

func TestParseKeyword(t *testing.T) {
	p := NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("sel"))
	assert.True(t, p.tryKeyword("SELECT"))
	assert.True(t, p.tryKeyword("hello") && p.isEnd())
}

func testParseValue(t *testing.T, s string, ref Cell) {
	t.Helper()
	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123})
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
}

func TestParseEqual(t *testing.T) {
	// Test parsing a condition like "column = value"
	p := NewParser(" foo = 123 ")
	out := NamedCell{}
	err := p.parseEqual(&out)
	
	assert.NoError(t, err)
	assert.Equal(t, "foo", out.column)
	assert.Equal(t, Cell{Type: TypeI64, I64: 123}, out.value)
}

func testParseSelect(t *testing.T, s string, ref StmtSelect) {
	t.Helper()
	p := NewParser(s)
	out := StmtSelect{}
	err := p.parseSelect(&out)
	
	assert.NoError(t, err)
	assert.True(t, p.isEnd(), "Expected parser to reach the end of the string")
	assert.Equal(t, ref, out)
}

func TestParseSelect(t *testing.T) {
	// This uses the exact string and expected output we traced earlier!
	query := "SELECT a, b FROM t WHERE c=1 AND d='e';"
	
	expectedOutput := StmtSelect{
		table: "t",
		cols:  []string{"a", "b"},
		keys: []NamedCell{
			{
				column: "c",
				value:  Cell{Type: TypeI64, I64: 1},
			},
			{
				column: "d",
				value:  Cell{Type: TypeStr, Str: []byte("e")},
			},
		},
	}

	testParseSelect(t, query, expectedOutput)
}
