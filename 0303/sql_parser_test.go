package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	t.Log("=== 0303 tryName (identifier lexer) test start ===")
	t.Log("grammar: skip spaces → read [A-Za-z_][A-Za-z0-9_]* → zero-copy slice")

	input := " a b0 _0_ 123 "
	t.Logf("[SETUP] input=%q", input)

	p := NewParser(input)
	t.Logf("[PARSER] pos=%d buf_len=%d", p.pos, len(p.buf))

	name, ok := p.tryName()
	t.Logf("[tryName #1] ok=%v name=%q pos=%d | expect \"a\"", ok, name, p.pos)
	assert.True(t, ok && name == "a")

	name, ok = p.tryName()
	t.Logf("[tryName #2] ok=%v name=%q pos=%d | expect \"b0\"", ok, name, p.pos)
	assert.True(t, ok && name == "b0")

	name, ok = p.tryName()
	t.Logf("[tryName #3] ok=%v name=%q pos=%d | expect \"_0_\"", ok, name, p.pos)
	assert.True(t, ok && name == "_0_")

	_, ok = p.tryName()
	t.Logf("[tryName #4] ok=%v pos=%d | expect fail — digits cannot start a name", ok, p.pos)
	assert.False(t, ok)

	t.Log("=== 0303 tryName test end ===")
}

func TestParseKeyword(t *testing.T) {
	t.Log("=== 0303 tryKeyword test start ===")
	t.Log("case-insensitive match + separator boundary (maximal munch guard)")

	input := " select  HELLO "
	t.Logf("[SETUP] input=%q", input)
	p := NewParser(input)

	ok := p.tryKeyword("sel")
	t.Logf("[tryKeyword \"sel\"] ok=%v | partial prefix → false", ok)
	assert.False(t, ok)

	ok = p.tryKeyword("SELECT")
	t.Logf("[tryKeyword \"SELECT\"] ok=%v pos=%d | full keyword consumed", ok, p.pos)
	assert.True(t, ok)

	ok = p.tryKeyword("hello") && p.isEnd()
	t.Logf("[tryKeyword \"hello\"] ok=%v isEnd=%v | second keyword + end of input", ok, p.isEnd())
	assert.True(t, ok)

	t.Log("=== 0303 tryKeyword test end ===")
}

func testParseValue(t *testing.T, s string, ref Cell, note string) {
	t.Helper()
	t.Logf("[CASE] input=%q | %s", s, note)

	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	t.Logf("[parseValue] err=%v pos=%d isEnd=%v", err, p.pos, p.isEnd())
	t.Logf("[parseValue output] type=%d i64=%d str=%q | compare to ref", out.Type, out.I64, out.Str)
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	t.Log("=== 0303 parseValue (literals) test start ===")
	t.Log("dispatch: quoted string → parseString; digit/sign → parseInt")

	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123}, "signed integer")
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "single-quoted with escapes")
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "double-quoted with escapes")

	t.Log("=== 0303 parseValue test end ===")
}

func TestParseEqual(t *testing.T) {
	t.Log("=== 0303 parseEqual test start ===")
	t.Log("grammar: column_name = literal_value")

	input := " foo = 123 "
	t.Logf("[SETUP] input=%q", input)

	p := NewParser(input)
	out := NamedCell{}
	err := p.parseEqual(&out)
	t.Logf("[parseEqual] err=%v column=%q", err, out.column)
	t.Logf("[parseEqual value] type=%d i64=%d str=%q | right-hand literal", out.value.Type, out.value.I64, out.value.Str)

	assert.NoError(t, err)
	assert.Equal(t, "foo", out.column)
	assert.Equal(t, Cell{Type: TypeI64, I64: 123}, out.value)

	t.Log("=== 0303 parseEqual test end ===")
}

func testParseSelect(t *testing.T, s string, ref StmtSelect, note string) {
	t.Helper()
	t.Logf("[CASE] query=%q | %s", s, note)

	p := NewParser(s)
	out := StmtSelect{}
	err := p.parseSelect(&out)
	t.Logf("[parseSelect] err=%v table=%q cols=%v keys=%d isEnd=%v",
		err, out.table, out.cols, len(out.keys), p.isEnd())
	for i, k := range out.keys {
		t.Logf("[parseSelect key #%d] column=%q", i, k.column)
		t.Logf("  [key value] type=%d i64=%d str=%q", k.value.Type, k.value.I64, k.value.Str)
	}

	assert.NoError(t, err)
	assert.True(t, p.isEnd(), "expected parser to reach end of string")
	assert.Equal(t, ref, out)
}

func TestParseSelect(t *testing.T) {
	t.Log("=== 0303 parseSelect (full SELECT) test start ===")
	t.Log("grammar: SELECT cols FROM table WHERE col=val AND col=val;")

	query := "SELECT a, b FROM t WHERE c=1 AND d='e';"
	t.Logf("[SETUP] query=%q", query)

	expected := StmtSelect{
		table: "t",
		cols:  []string{"a", "b"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	t.Log("[EXPECT] table=t, cols=[a b], WHERE c=1 AND d='e'")

	testParseSelect(t, query, expected, "README walkthrough query")

	t.Log("=== 0303 parseSelect test end ===")
}
