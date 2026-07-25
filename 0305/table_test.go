package kv

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableByPKey(t *testing.T) {
	t.Log("=== 0305 DB CRUD by primary key test start ===")
	t.Log("low-level table API before SQL ExecStmt layer")

	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)
	os.Remove(db.KV.log.FileName)

	t.Logf("[SETUP] log file=%q", db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2},
	}
	t.Logf("[SETUP] table=%q PKey=%v", schema.Table, schema.PKey)

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "ROW", row, "insert payload")

	ok, err := db.Select(schema, row)
	t.Logf("[Select missing] ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)

	updated, err := db.Insert(schema, row)
	t.Logf("[Insert] updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	t.Logf("[Select by PK] ok=%v", ok)
	logRow(t, "Select result", out, "hydrated row")
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	row[0].I64 = 456
	updated, err = db.Update(schema, row)
	t.Logf("[Update] updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	ok, err = db.Select(schema, out)
	t.Logf("[Select after Update] ok=%v time=%d", ok, out[0].I64)
	logRow(t, "Select result", out, "time should be 456")
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	deleted, err := db.Delete(schema, row)
	t.Logf("[Delete] deleted=%v err=%v", deleted, err)
	assert.True(t, deleted && err == nil)

	ok, err = db.Select(schema, row)
	t.Logf("[Select after Delete] ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)

	t.Log("=== 0305 DB CRUD by primary key test end ===")
}

func parseStmt(t *testing.T, s string) interface{} {
	t.Helper()
	t.Logf("[PARSE] sql=%q", s)
	p := NewParser(s)
	stmt, err := p.parseStmt()
	t.Logf("[PARSE output] err=%v type=%T", err, stmt)
	require.Nil(t, err)
	return stmt
}

func logSQLResult(t *testing.T, step string, r SQLResult) {
	t.Helper()
	t.Logf("[%s] updated=%d header=%v rows=%d", step, r.Updated, r.Header, len(r.Values))
	for i, row := range r.Values {
		logRow(t, fmt.Sprintf("  result row[%d]", i), row, "")
	}
}

func TestSQLByPKey(t *testing.T) {
	t.Log("=== 0305 SQL ExecStmt end-to-end test start ===")
	t.Log("parseStmt → ExecStmt → table/KV; full SQL CRUD + reopen")

	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)
	os.Remove(db.KV.log.FileName)

	t.Logf("[SETUP] log file=%q", db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	s := "create table link (time int64, src string, dst string, primary key (src, dst));"
	t.Log("--- CREATE TABLE ---")
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	t.Log("[CREATE] schema registered in memory")

	s = "insert into link values (123, 'bob', 'alice');"
	t.Log("--- INSERT ---")
	r, err := db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "INSERT", r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	t.Log("--- SELECT (point query by PK columns) ---")
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "SELECT", r)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 123}}}, r.Values)

	s = "update link set time = 456 where dst = 'alice' and src = 'bob';"
	t.Log("--- UPDATE ---")
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "UPDATE", r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "SELECT after UPDATE", r)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 456}}}, r.Values)

	t.Log("--- REOPEN (durability via KV log replay) ---")
	err = db.Close()
	require.Nil(t, err)
	db = DB{}
	db.KV.log.FileName = ".test_db"
	err = db.Open()
	require.Nil(t, err)

	s = "delete from link where src = 'bob' and dst = 'alice';"
	t.Log("--- DELETE after reopen ---")
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "DELETE", r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, "SELECT after DELETE", r)
	require.Equal(t, 0, len(r.Values))

	t.Log("=== 0305 SQL ExecStmt end-to-end test end ===")
}
