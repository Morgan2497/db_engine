package kv

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func linkSchema() *Schema {
	return &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2}, // (src, dst)
	}
}

func TestTableByPKey(t *testing.T) {
	t.Log("=== 0204 DB CRUD by primary key test start ===")
	t.Log("DB façade: Insert/Select/Update/Delete → EncodeKey/EncodeVal + SetEx/Get/Del")

	path := filepath.Join(t.TempDir(), "table.log")
	t.Logf("[SETUP] log file path=%q", path)

	db := &DB{KV: KV{log: Log{FileName: path}}}
	t.Log("calling db.Open() — opens underlying KV (createFileSync + replay)")
	assert.NoError(t, db.Open())
	defer func() {
		t.Log("calling db.Close()")
		db.Close()
	}()

	schema := linkSchema()
	t.Logf("[SETUP] table=%q cols=[time:i64, src:str, dst:str] PKey=%v", schema.Table, schema.PKey)
	t.Log("[SETUP] PK is (src, dst); time is non-PK value payload")

	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "ROW", row, "full row for Insert/Update")

	// --- Select miss ---
	t.Log("--- Select on empty table (expect ok=false) ---")
	logRow(t, "Select input", row, "EncodeKey → Get; miss should not DecodeVal")
	ok, err := db.Select(schema, row)
	t.Logf("[Select output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	// --- Insert ---
	t.Log("--- Insert (ModeInsert) ---")
	logRow(t, "Insert input", row, "EncodeKey+EncodeVal → SetEx(..., ModeInsert)")
	updated, err := db.Insert(schema, row)
	t.Logf("[Insert output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	// --- Insert conflict ---
	t.Log("--- Insert same PK again (expect updated=false) ---")
	updated, err = db.Insert(schema, row)
	t.Logf("[Insert output] updated=%v err=%v (PK collision — ModeInsert refuses)", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	// --- Select hit (PK-only input row) ---
	t.Log("--- Select with PK-only row (time slot empty until DecodeVal) ---")
	out := Row{
		{}, // time blank
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}
	logRow(t, "Select input", out, "only src/dst needed for key")
	ok, err = db.Select(schema, out)
	t.Logf("[Select output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	logRow(t, "Select result", out, "DecodeVal filled time=123")
	assert.Equal(t, row, out)

	// --- Update ---
	t.Log("--- Update time 123 → 456 (ModeUpdate) ---")
	row[0].I64 = 456
	logRow(t, "Update input", row, "same PK, new non-PK value")
	updated, err = db.Update(schema, row)
	t.Logf("[Update output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	out = Row{
		{},
		{Type: TypeStr, Str: []byte("a")},
		{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	t.Logf("[Select after Update] ok=%v time=%d", ok, out[0].I64)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, row, out)

	// --- Update missing PK ---
	t.Log("--- Update on missing PK (expect updated=false) ---")
	missing := Row{
		{Type: TypeI64, I64: 1},
		{Type: TypeStr, Str: []byte("x")},
		{Type: TypeStr, Str: []byte("y")},
	}
	logRow(t, "Update input", missing, "key does not exist")
	updated, err = db.Update(schema, missing)
	t.Logf("[Update output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	// --- Upsert new + overwrite ---
	t.Log("--- Upsert new key, then overwrite ---")
	ups := Row{
		{Type: TypeI64, I64: 10},
		{Type: TypeStr, Str: []byte("u")},
		{Type: TypeStr, Str: []byte("v")},
	}
	logRow(t, "Upsert input", ups, "ModeUpsert insert")
	updated, err = db.Upsert(schema, ups)
	t.Logf("[Upsert output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	ups[0].I64 = 11
	logRow(t, "Upsert input", ups, "ModeUpsert overwrite")
	updated, err = db.Upsert(schema, ups)
	t.Logf("[Upsert output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	// --- Delete ---
	t.Log("--- Delete by PK ---")
	logRow(t, "Delete input", row, "EncodeKey → Del (tombstone)")
	deleted, err := db.Delete(schema, row)
	t.Logf("[Delete output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.True(t, deleted)

	ok, err = db.Select(schema, row)
	t.Logf("[Select after Delete] ok=%v err=%v (expect miss)", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	// --- Delete missing ---
	t.Log("--- Delete missing key (idempotent) ---")
	deleted, err = db.Delete(schema, row)
	t.Logf("[Delete output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.False(t, deleted)

	t.Log("=== 0204 DB CRUD by primary key test end ===")
}

func TestTableSelectHydratesNonPKey(t *testing.T) {
	t.Log("=== 0204 Select hydrates non-PK columns test start ===")
	t.Log("wallet-style: Select with only PK filled; DecodeVal fills the rest in place")

	path := filepath.Join(t.TempDir(), "wallet.log")
	db := &DB{KV: KV{log: Log{FileName: path}}}
	assert.NoError(t, db.Open())
	defer db.Close()

	schema := &Schema{
		Table: "wallet",
		Cols: []Column{
			{Name: "wallet_id", Type: TypeI64},
			{Name: "owner_name", Type: TypeStr},
			{Name: "balance", Type: TypeI64},
		},
		PKey: []int{0},
	}
	t.Logf("[SETUP] table=%q PKey=[wallet_id]", schema.Table)

	full := Row{
		{Type: TypeI64, I64: 999},
		{Type: TypeStr, Str: []byte("Morgan")},
		{Type: TypeI64, I64: 1500},
	}
	logRow(t, "Insert", full, "all columns required for write")
	updated, err := db.Insert(schema, full)
	t.Logf("[Insert output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	partial := Row{
		{Type: TypeI64, I64: 999}, // PK only
		{},
		{},
	}
	logRow(t, "Select input", partial, "owner_name/balance empty before Select")
	ok, err := db.Select(schema, partial)
	t.Logf("[Select output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	logRow(t, "Select result", partial, "non-PK hydrated from KV value")
	assert.Equal(t, full, partial)

	t.Log("=== 0204 Select hydrates non-PK columns test end ===")
}

func TestTableCRUDSurvivesReopen(t *testing.T) {
	t.Log("=== 0204 DB CRUD survives reopen/replay test start ===")

	path := filepath.Join(t.TempDir(), "persist.log")
	t.Logf("[SETUP] shared log path=%q", path)

	schema := linkSchema()
	row := Row{
		{Type: TypeI64, I64: 123},
		{Type: TypeStr, Str: []byte("bob")},
		{Type: TypeStr, Str: []byte("alice")},
	}

	t.Log("--- phase 1: Insert via DB ---")
	db1 := &DB{KV: KV{log: Log{FileName: path}}}
	assert.NoError(t, db1.Open())
	logRow(t, "Insert", row, "persists through KV log")
	updated, err := db1.Insert(schema, row)
	assert.NoError(t, err)
	assert.True(t, updated)
	assert.NoError(t, db1.Close())

	t.Log("--- phase 2: reopen DB, Select by PK ---")
	db2 := &DB{KV: KV{log: Log{FileName: path}}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	out := Row{
		{},
		{Type: TypeStr, Str: []byte("bob")},
		{Type: TypeStr, Str: []byte("alice")},
	}
	ok, err := db2.Select(schema, out)
	t.Logf("[Select] ok=%v err=%v", ok, err)
	logRow(t, "Select result", out, "replay restored row")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, row, out)

	t.Log("=== 0204 DB CRUD survives reopen/replay test end ===")
}
