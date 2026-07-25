package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	t.Log("=== 0302 tryName (identifier lexer) test start ===")
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

	t.Log("=== 0302 tryName test end ===")
}

func TestParseNameSelectID(t *testing.T) {
	t.Log("=== 0302 tryName on SQL fragment test start ===")

	input := "  SELECT id"
	t.Logf("[SETUP] input=%q (README walkthrough)", input)
	t.Log("[EXPECT] skip 2 spaces, token SELECT at [2:8], pos=8")

	p := NewParser(input)
	name, ok := p.tryName()
	t.Logf("[tryName output] ok=%v name=%q pos=%d", ok, name, p.pos)
	assert.True(t, ok)
	assert.Equal(t, "SELECT", name)
	assert.Equal(t, 8, p.pos)

	name, ok = p.tryName()
	t.Logf("[tryName output] ok=%v name=%q pos=%d | second token \"id\"", ok, name, p.pos)
	assert.True(t, ok)
	assert.Equal(t, "id", name)

	t.Log("=== 0302 tryName on SQL fragment test end ===")
}

func TestParseNameRollbackOnFailure(t *testing.T) {
	t.Log("=== 0302 tryName rollback test start ===")
	t.Log("on failure, pos must rewind so caller can try another production")

	input := "123"
	p := NewParser(input)
	startPos := p.pos

	_, ok := p.tryName()
	t.Logf("[tryName] ok=%v pos=%d startPos=%d | digit start → fail + rollback", ok, p.pos, startPos)
	assert.False(t, ok)
	assert.Equal(t, startPos, p.pos)

	t.Log("=== 0302 tryName rollback test end ===")
}

func TestParseNameMaximalMunch(t *testing.T) {
	t.Log("=== 0302 maximal munch test start ===")
	t.Log("selections is ONE identifier, not select + ions")

	input := "selections"
	p := NewParser(input)
	name, ok := p.tryName()
	t.Logf("[tryName] ok=%v name=%q len=%d", ok, name, len(name))
	assert.True(t, ok)
	assert.Equal(t, "selections", name)
	assert.Equal(t, len(input), p.pos)

	t.Log("=== 0302 maximal munch test end ===")
}

func TestParseKeyword(t *testing.T) {
	t.Log("=== 0302 tryKeyword test start ===")
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

	t.Log("=== 0302 tryKeyword test end ===")
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
	t.Log("=== 0302 parseValue (literals) test start ===")
	t.Log("new in 0302: dispatch quoted string → parseString; digit/sign → parseInt")
	t.Log("escape rules: \\' and \\\" inside strings; backslash must escape quotes")

	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123}, "signed integer via parseInt + strconv.ParseInt")
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "single-quoted with escapes")
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "double-quoted with escapes")

	t.Log("=== 0302 parseValue test end ===")
}
