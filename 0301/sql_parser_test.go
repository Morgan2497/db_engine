package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	t.Log("=== 0301 tryName (identifier lexer) test start ===")
	t.Log("grammar: skip spaces → read [A-Za-z_][A-Za-z0-9_]* → zero-copy slice")

	input := " a b0 _0_ 123 "
	t.Logf("[SETUP] input=%q", input)
	t.Logf("[SETUP] cursor diagram: ' ' 'a' ' ' 'b''0' ' ' '_' '0' '_' ' ' '1''2''3' ' '")

	p := NewParser(input)
	t.Logf("[PARSER] pos=%d buf_len=%d", p.pos, len(p.buf))

	name, ok := p.tryName()
	t.Logf("[tryName #1] ok=%v name=%q pos=%d | expect \"a\"", ok, name, p.pos)
	assert.True(t, ok)
	assert.Equal(t, "a", name)

	name, ok = p.tryName()
	t.Logf("[tryName #2] ok=%v name=%q pos=%d | expect \"b0\"", ok, name, p.pos)
	assert.True(t, ok)
	assert.Equal(t, "b0", name)

	name, ok = p.tryName()
	t.Logf("[tryName #3] ok=%v name=%q pos=%d | expect \"_0_\"", ok, name, p.pos)
	assert.True(t, ok)
	assert.Equal(t, "_0_", name)

	name, ok = p.tryName()
	t.Logf("[tryName #4] ok=%v name=%q pos=%d | expect fail — digits cannot start a name", ok, name, p.pos)
	assert.False(t, ok)

	t.Log("=== 0301 tryName test end ===")
}

func TestParseNameSelectID(t *testing.T) {
	t.Log("=== 0301 tryName on SQL fragment test start ===")

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

	t.Log("=== 0301 tryName on SQL fragment test end ===")
}

func TestParseNameRollbackOnFailure(t *testing.T) {
	t.Log("=== 0301 tryName rollback test start ===")
	t.Log("on failure, pos must rewind so caller can try another production")

	input := "123"
	p := NewParser(input)
	startPos := p.pos

	_, ok := p.tryName()
	t.Logf("[tryName] ok=%v pos=%d startPos=%d | digit start → fail + rollback", ok, p.pos, startPos)
	assert.False(t, ok)
	assert.Equal(t, startPos, p.pos)

	t.Log("=== 0301 tryName rollback test end ===")
}

func TestParseNameMaximalMunch(t *testing.T) {
	t.Log("=== 0301 maximal munch test start ===")
	t.Log("selections is ONE identifier, not select + ions")

	input := "selections"
	p := NewParser(input)
	name, ok := p.tryName()
	t.Logf("[tryName] ok=%v name=%q len=%d", ok, name, len(name))
	assert.True(t, ok)
	assert.Equal(t, "selections", name)
	assert.Equal(t, len(input), p.pos)

	t.Log("=== 0301 maximal munch test end ===")
}
