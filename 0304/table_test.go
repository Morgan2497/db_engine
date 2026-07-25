package kv

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableByPKey(t *testing.T) {
	t.Log("=== 0304 DB CRUD by primary key test start ===")
	t.Log("table layer: Insert/Select/Update/Delete → KV EncodeKey/EncodeVal")

	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)
	os.Remove(db.KV.log.FileName)

	t.Logf("[SETUP] log file=%q", db.KV.log.FileName)
	t.Log("calling db.Open()")
	err := db.Open()
	assert.Nil(t, err)
	defer func() {
		t.Log("calling db.Close()")
		db.Close()
	}()

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2},
	}
	t.Logf("[SETUP] table=%q PKey=%v (src, dst); time is non-PK value", schema.Table, schema.PKey)

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "ROW", row, "full row for writes")

	t.Log("--- Select on empty table (expect ok=false) ---")
	ok, err := db.Select(schema, row)
	t.Logf("[Select output] ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)

	t.Log("--- Insert (ModeInsert) ---")
	logRow(t, "Insert input", row, "EncodeKey+EncodeVal → SetEx")
	updated, err := db.Insert(schema, row)
	t.Logf("[Insert output] updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	t.Log("--- Select with PK-only row ---")
	logRow(t, "Select input", out, "time blank until DecodeVal")
	ok, err = db.Select(schema, out)
	t.Logf("[Select output] ok=%v err=%v", ok, err)
	logRow(t, "Select result", out, "DecodeVal filled time")
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	t.Log("--- Update time 123 → 456 ---")
	row[0].I64 = 456
	logRow(t, "Update input", row, "same PK, new non-PK value")
	updated, err = db.Update(schema, row)
	t.Logf("[Update output] updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	out = Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	t.Logf("[Select after Update] ok=%v time=%d", ok, out[0].I64)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	t.Log("--- Delete by PK ---")
	logRow(t, "Delete input", row, "EncodeKey → Del tombstone")
	deleted, err := db.Delete(schema, row)
	t.Logf("[Delete output] deleted=%v err=%v", deleted, err)
	assert.True(t, deleted && err == nil)

	ok, err = db.Select(schema, row)
	t.Logf("[Select after Delete] ok=%v err=%v | expect miss", ok, err)
	assert.True(t, !ok && err == nil)

	t.Log("=== 0304 DB CRUD by primary key test end ===")
}
