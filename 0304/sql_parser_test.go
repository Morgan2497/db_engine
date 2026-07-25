package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	t.Log("=== 0304 tryName (identifier lexer) test start ===")
	t.Log("grammar: skip spaces → read [A-Za-z_][A-Za-z0-9_]* → zero-copy slice")

	input := " a b0 _0_ 123 "
	t.Logf("[SETUP] input=%q", input)

	p := NewParser(input)
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

	t.Log("=== 0304 tryName test end ===")
}

func TestParseKeyword(t *testing.T) {
	t.Log("=== 0304 tryKeyword test start ===")
	t.Log("case-insensitive match + separator boundary; 0304 adds multi-keyword sequences")

	input := " select  HELLO "
	t.Logf("[SETUP] input=%q", input)
	p := NewParser(input)

	ok := p.tryKeyword("sel")
	t.Logf("[tryKeyword \"sel\"] ok=%v | partial prefix → false", ok)
	assert.False(t, ok)

	ok = p.tryKeyword("SELECT")
	t.Logf("[tryKeyword \"SELECT\"] ok=%v pos=%d", ok, p.pos)
	assert.True(t, ok)

	ok = p.tryKeyword("hello") && p.isEnd()
	t.Logf("[tryKeyword \"hello\"] ok=%v isEnd=%v", ok, p.isEnd())
	assert.True(t, ok)

	p = NewParser(" select  HELLO ")
	ok = p.tryKeyword("select", "hi")
	t.Logf("[tryKeyword \"select\",\"hi\"] ok=%v | wrong second token → false", ok)
	assert.False(t, ok)

	ok = p.tryKeyword("select", "hello") && p.isEnd()
	t.Logf("[tryKeyword \"select\",\"hello\"] ok=%v isEnd=%v | multi-keyword chain", ok, p.isEnd())
	assert.True(t, ok)

	t.Log("=== 0304 tryKeyword test end ===")
}

func testParseValue(t *testing.T, s string, ref Cell, note string) {
	t.Helper()
	t.Logf("[CASE] input=%q | %s", s, note)

	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	t.Logf("[parseValue] err=%v pos=%d isEnd=%v type=%d i64=%d str=%q", err, p.pos, p.isEnd(), out.Type, out.I64, out.Str)
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	t.Log("=== 0304 parseValue test start ===")
	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123}, "signed integer")
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "single-quoted escapes")
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "double-quoted escapes")
	t.Log("=== 0304 parseValue test end ===")
}

func TestParseEqual(t *testing.T) {
	t.Log("=== 0304 parseEqual test start ===")
	t.Log("grammar: column_name = literal_value")

	input := " foo = 123 "
	t.Logf("[SETUP] input=%q", input)

	p := NewParser(input)
	out := NamedCell{}
	err := p.parseEqual(&out)
	t.Logf("[parseEqual] err=%v column=%q value_i64=%d", err, out.column, out.value.I64)
	assert.NoError(t, err)
	assert.Equal(t, "foo", out.column)
	assert.Equal(t, Cell{Type: TypeI64, I64: 123}, out.value)

	t.Log("=== 0304 parseEqual test end ===")
}

func testParseSelect(t *testing.T, s string, ref StmtSelect, note string) {
	t.Helper()
	t.Logf("[CASE] query=%q | %s", s, note)

	p := NewParser(s)
	stmt, err := p.parseStmt()
	t.Logf("[parseStmt] err=%v", err)
	assert.NoError(t, err)

	out, ok := stmt.(*StmtSelect)
	t.Logf("[type assert] *StmtSelect ok=%v table=%q cols=%v keys=%d isEnd=%v", ok, out.table, out.cols, len(out.keys), p.isEnd())
	assert.True(t, ok)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, *out)
}

func TestParseSelect(t *testing.T) {
	t.Log("=== 0304 parseSelect via parseStmt test start ===")

	query := "SELECT a, b FROM t WHERE c=1 AND d='e';"
	expected := StmtSelect{
		table: "t",
		cols:  []string{"a", "b"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseSelect(t, query, expected, "README walkthrough SELECT")

	t.Log("=== 0304 parseSelect test end ===")
}

func testParseStmt(t *testing.T, s string, ref interface{}, note string) {
	t.Helper()
	t.Logf("[CASE] sql=%q | %s", s, note)

	p := NewParser(s)
	out, err := p.parseStmt()
	t.Logf("[parseStmt] err=%v type=%T isEnd=%v", err, out, p.isEnd())
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseStmt(t *testing.T) {
	t.Log("=== 0304 parseStmt dispatch test start ===")
	t.Log("new in 0304: top-level router → SELECT / CREATE / INSERT / UPDATE / DELETE")

	var stmt interface{}

	s := "select a from t where c=1;"
	stmt = &StmtSelect{
		table: "t",
		cols:  []string{"a"},
		keys:  []NamedCell{{column: "c", value: Cell{Type: TypeI64, I64: 1}}},
	}
	testParseStmt(t, s, stmt, "minimal SELECT")

	s = "select a,b_02 from T where c=1 and d='e';"
	stmt = &StmtSelect{
		table: "T",
		cols:  []string{"a", "b_02"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseStmt(t, s, stmt, "SELECT with two WHERE keys")

	s = "select a, b_02 from T where c = 1 and d = 'e' ; "
	stmt = &StmtSelect{
		table: "T",
		cols:  []string{"a", "b_02"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseStmt(t, s, stmt, "extra whitespace tolerated")

	s = "create table t (a string, b int64, primary key (b));"
	stmt = &StmtCreateTable{
		table: "t",
		cols:  []Column{{"a", TypeStr}, {"b", TypeI64}},
		pkey:  []string{"b"},
	}
	testParseStmt(t, s, stmt, "CREATE TABLE with PK")

	s = "insert into t values (1, 'hi');"
	stmt = &StmtInsert{
		table: "t",
		value: []Cell{{Type: TypeI64, I64: 1}, {Type: TypeStr, Str: []byte("hi")}},
	}
	testParseStmt(t, s, stmt, "INSERT VALUES")

	s = "update t set a = 1, b = 2 where c = 3 and d = 4;"
	stmt = &StmtUpdate{
		table: "t",
		value: []NamedCell{{"a", Cell{Type: TypeI64, I64: 1}}, {"b", Cell{Type: TypeI64, I64: 2}}},
		keys:  []NamedCell{{"c", Cell{Type: TypeI64, I64: 3}}, {"d", Cell{Type: TypeI64, I64: 4}}},
	}
	testParseStmt(t, s, stmt, "UPDATE SET + WHERE")

	s = "delete from t where c = 3 and d = 4;"
	stmt = &StmtDelete{
		table: "t",
		keys:  []NamedCell{{"c", Cell{Type: TypeI64, I64: 3}}, {"d", Cell{Type: TypeI64, I64: 4}}},
	}
	testParseStmt(t, s, stmt, "DELETE WHERE")

	t.Log("=== 0304 parseStmt dispatch test end ===")
}
