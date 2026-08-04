package kv

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
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
		PKey: []int{1, 2}, // (src, dst)
	}

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err := db.Select(schema, row)
	t.Logf("select missing row: ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)

	updated, err := db.Insert(schema, row)
	t.Logf("insert row: updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	t.Logf("select by pkey: ok=%v row=%v", ok, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	row[0].I64 = 456
	updated, err = db.Update(schema, row)
	t.Logf("update row: updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	ok, err = db.Select(schema, out)
	t.Logf("select after update: ok=%v row=%v", ok, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	deleted, err := db.Delete(schema, row)
	t.Logf("delete row: deleted=%v err=%v", deleted, err)
	assert.True(t, deleted && err == nil)

	ok, err = db.Select(schema, row)
	t.Logf("select after delete: ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)
}

func parseStmt(t *testing.T, s string) interface{} {
	t.Helper()
	t.Logf("sql: %s", s)
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.Nil(t, err)
	return stmt
}

func logSQLResult(t *testing.T, r SQLResult) {
	t.Helper()
	t.Logf("result: updated=%d header=%v values=%v", r.Updated, r.Header, r.Values)
}

func TestSQLByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	s := "create table link (time int64, src string, dst string, primary key (src, dst));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	t.Log("create table: ok")

	s = "insert into link values (123, 'bob', 'alice');"
	r, err := db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 123}}}, r.Values)

	s = "update link set time = 456 where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 456}}}, r.Values)

	// reopen
	t.Log("reopen database")
	err = db.Close()
	require.Nil(t, err)
	db = DB{}
	db.KV.log.FileName = ".test_db"
	err = db.Open()
	require.Nil(t, err)

	s = "delete from link where src = 'bob' and dst = 'alice';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 0, len(r.Values))
}

func TestIterByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		PKey: []int{0},
	}

	N := int64(10)
	for i := int64(0); i < N; i += 2 {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{Type: TypeI64, I64: i},
		}
		updated, err := db.Insert(schema, row)
		require.True(t, updated && err == nil)
	}

	for i := int64(-1); i < N+1; i++ {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{},
		}

		out := []int64{}
		iter, err := db.Seek(schema, row)
		for ; err == nil && iter.Valid(); err = iter.Next() {
			out = append(out, iter.Row()[1].I64)
		}
		require.Nil(t, err)

		expected := []int64{}
		for j := i; j < N; j++ {
			if j >= 0 && j%2 == 0 {
				expected = append(expected, j)
			}
		}
		assert.Equal(t, expected, out)
	}
}
