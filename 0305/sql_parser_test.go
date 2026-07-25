package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseName(t *testing.T) {
	t.Log("=== 0305 tryName (identifier lexer) test start ===")

	input := " a b0 _0_ 123 "
	t.Logf("[SETUP] input=%q", input)

	p := NewParser(input)
	name, ok := p.tryName()
	t.Logf("[tryName #1] ok=%v name=%q", ok, name)
	assert.True(t, ok && name == "a")

	name, ok = p.tryName()
	t.Logf("[tryName #2] ok=%v name=%q", ok, name)
	assert.True(t, ok && name == "b0")

	name, ok = p.tryName()
	t.Logf("[tryName #3] ok=%v name=%q", ok, name)
	assert.True(t, ok && name == "_0_")

	_, ok = p.tryName()
	t.Logf("[tryName #4] ok=%v | expect fail on digit start", ok)
	assert.False(t, ok)

	t.Log("=== 0305 tryName test end ===")
}

func TestParseKeyword(t *testing.T) {
	t.Log("=== 0305 tryKeyword test start ===")

	p := NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("sel"))
	t.Log("[tryKeyword \"sel\"] ok=false | partial prefix")

	assert.True(t, p.tryKeyword("SELECT"))
	t.Log("[tryKeyword \"SELECT\"] ok=true")

	assert.True(t, p.tryKeyword("hello") && p.isEnd())
	t.Log("[tryKeyword \"hello\"] ok=true isEnd=true")

	p = NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("select", "hi"))
	t.Log("[tryKeyword \"select\",\"hi\"] ok=false | wrong second token")

	assert.True(t, p.tryKeyword("select", "hello") && p.isEnd())
	t.Log("[tryKeyword \"select\",\"hello\"] ok=true | multi-keyword chain")

	t.Log("=== 0305 tryKeyword test end ===")
}

func testParseValue(t *testing.T, s string, ref Cell, note string) {
	t.Helper()
	t.Logf("[CASE] input=%q | %s", s, note)

	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	t.Logf("[parseValue] err=%v type=%d i64=%d str=%q", err, out.Type, out.I64, out.Str)
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	t.Log("=== 0305 parseValue test start ===")
	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123}, "signed integer")
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "single-quoted escapes")
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")}, "double-quoted escapes")
	t.Log("=== 0305 parseValue test end ===")
}

func TestParseEqual(t *testing.T) {
	t.Log("=== 0305 parseEqual test start ===")

	p := NewParser(" foo = 123 ")
	out := NamedCell{}
	err := p.parseEqual(&out)
	t.Logf("[parseEqual] err=%v column=%q value_i64=%d", err, out.column, out.value.I64)
	assert.NoError(t, err)
	assert.Equal(t, "foo", out.column)
	assert.Equal(t, Cell{Type: TypeI64, I64: 123}, out.value)

	t.Log("=== 0305 parseEqual test end ===")
}

func testParseSelect(t *testing.T, s string, ref StmtSelect, note string) {
	t.Helper()
	t.Logf("[CASE] query=%q | %s", s, note)

	p := NewParser(s)
	stmt, err := p.parseStmt()
	assert.NoError(t, err)
	out, ok := stmt.(*StmtSelect)
	t.Logf("[parseStmt] *StmtSelect ok=%v table=%q cols=%v", ok, out.table, out.cols)
	assert.True(t, ok)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, *out)
}

func TestParseSelect(t *testing.T) {
	t.Log("=== 0305 parseSelect test start ===")

	query := "SELECT a, b FROM t WHERE c=1 AND d='e';"
	expected := StmtSelect{
		table: "t",
		cols:  []string{"a", "b"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseSelect(t, query, expected, "full SELECT with AND clause")

	t.Log("=== 0305 parseSelect test end ===")
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
	t.Log("=== 0305 parseStmt dispatch test start ===")
	t.Log("feeds ExecStmt in 0305 — parser output drives SQL execution")

	var stmt interface{}

	s := "select a from t where c=1;"
	stmt = &StmtSelect{
		table: "t",
		cols:  []string{"a"},
		keys:  []NamedCell{{column: "c", value: Cell{Type: TypeI64, I64: 1}}},
	}
	testParseStmt(t, s, stmt, "SELECT")

	s = "select a,b_02 from T where c=1 and d='e';"
	stmt = &StmtSelect{
		table: "T",
		cols:  []string{"a", "b_02"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseStmt(t, s, stmt, "SELECT multi-column")

	s = "select a, b_02 from T where c = 1 and d = 'e' ; "
	stmt = &StmtSelect{
		table: "T",
		cols:  []string{"a", "b_02"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseStmt(t, s, stmt, "SELECT with extra spaces")

	s = "create table t (a string, b int64, primary key (b));"
	stmt = &StmtCreateTable{
		table: "t",
		cols:  []Column{{"a", TypeStr}, {"b", TypeI64}},
		pkey:  []string{"b"},
	}
	testParseStmt(t, s, stmt, "CREATE TABLE")

	s = "insert into t values (1, 'hi');"
	stmt = &StmtInsert{
		table: "t",
		value: []Cell{{Type: TypeI64, I64: 1}, {Type: TypeStr, Str: []byte("hi")}},
	}
	testParseStmt(t, s, stmt, "INSERT")

	s = "update t set a = 1, b = 2 where c = 3 and d = 4;"
	stmt = &StmtUpdate{
		table: "t",
		value: []NamedCell{{"a", Cell{Type: TypeI64, I64: 1}}, {"b", Cell{Type: TypeI64, I64: 2}}},
		keys:  []NamedCell{{"c", Cell{Type: TypeI64, I64: 3}}, {"d", Cell{Type: TypeI64, I64: 4}}},
	}
	testParseStmt(t, s, stmt, "UPDATE")

	s = "delete from t where c = 3 and d = 4;"
	stmt = &StmtDelete{
		table: "t",
		keys:  []NamedCell{{"c", Cell{Type: TypeI64, I64: 3}}, {"d", Cell{Type: TypeI64, I64: 4}}},
	}
	testParseStmt(t, s, stmt, "DELETE")

	t.Log("=== 0305 parseStmt dispatch test end ===")
}
