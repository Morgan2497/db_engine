package kv

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func logKV(t *testing.T, step string, key, val []byte, extra string) {
	t.Helper()
	if val != nil {
		t.Logf("[%s] key=%q val=%q | %s", step, key, val, extra)
	} else {
		t.Logf("[%s] key=%q | %s", step, key, extra)
	}
}

func logEntry(t *testing.T, step string, ent Entry, extra string) {
	t.Helper()
	if ent.deleted {
		t.Logf("[%s] key=%q deleted=true | %s", step, ent.key, extra)
	} else {
		t.Logf("[%s] key=%q val=%q deleted=false | %s", step, ent.key, ent.val, extra)
	}
}

func TestKVBasic(t *testing.T) {
	t.Log("=== 0103 KV lifecycle test start (append-only log + replay) ===")

	path := filepath.Join(t.TempDir(), "basic.log")
	t.Logf("[SETUP] log file path=%q", path)

	db := &KV{log: Log{FileName: path}}
	t.Log("calling Open() — create/open log, replay into empty mem")
	assert.NoError(t, db.Open())
	defer func() {
		t.Log("calling Close() — flush file handle")
		db.Close()
	}()

	key := []byte("morgankim")
	val := []byte("developer")

	logKV(t, "SET input", key, val, "expect updated=true; appends Entry to log then updates mem")
	updated, err := db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	logKV(t, "SET input", key, val, "expect updated=false (identical value — no log append)")
	updated, err = db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	logKV(t, "GET input", key, nil, "expect ok=true from mem (no disk read)")
	got, ok, err := db.Get(key)
	t.Logf("[GET output] ok=%v err=%v val=%q", ok, err, got)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, val, got)

	logKV(t, "DEL input", key, nil, "expect deleted=true; tombstone appended to log")
	deleted, err := db.Del(key)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.True(t, deleted)

	logKV(t, "GET input", key, nil, "expect ok=false after tombstone")
	_, ok, err = db.Get(key)
	t.Logf("[GET output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	missing := []byte("missing")
	logKV(t, "DEL input", missing, nil, "expect deleted=false (key never in mem)")
	deleted, err = db.Del(missing)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.False(t, deleted)

	t.Log("=== 0103 KV lifecycle test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0103 Entry serialization test start (9-byte header + deleted flag) ===")

	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	logEntry(t, "ENCODE input", ent, "key len=2, val len=3")

	want := []byte{
		2, 0, 0, 0,
		3, 0, 0, 0,
		0,
		'k', '1', 'x', 'x', 'x',
	}
	t.Logf("[ENCODE expect] wire: keySize(4) + valSize(4) + deleted(1) + key + val")
	t.Logf("[ENCODE expect] bytes=%v", want)

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)
	assert.Equal(t, want, got)

	t.Log("calling Decode(bytes.NewReader(got))")
	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)

	logEntry(t, "DECODE result", decoded, "should match original Entry")
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)
	assert.False(t, decoded.deleted)

	t.Log("=== 0103 Entry serialization test end ===")
}

func TestEntryTombstone(t *testing.T) {
	t.Log("=== 0103 Entry tombstone test start ===")

	ent := Entry{key: []byte("k1"), deleted: true}
	logEntry(t, "ENCODE input", ent, "tombstone: val omitted on wire (valSize=0)")

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)
	t.Logf("[ENCODE check] deleted byte at offset 8 = %d", got[8])

	var decoded Entry
	t.Log("calling Decode on tombstone bytes")
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)

	logEntry(t, "DECODE result", decoded, "deleted flag should be true")
	assert.Equal(t, ent.key, decoded.key)
	assert.True(t, decoded.deleted)

	t.Log("=== 0103 Entry tombstone test end ===")
}

func TestKVRecovery(t *testing.T) {
	t.Log("=== 0103 KV recovery test start (close + reopen replays log) ===")

	path := filepath.Join(t.TempDir(), "test.log")
	t.Logf("[SETUP] shared log path=%q", path)

	t.Log("--- phase 1: db1 writes 4 log records ---")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	logKV(t, "SET", []byte("user1"), []byte("Morgan"), "log record 1")
	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user2"), []byte("Alice"), "log record 2")
	_, err = db1.Set([]byte("user2"), []byte("Alice"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user1"), []byte("Morgan Kim"), "log record 3 — overwrites user1 in mem")
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)

	logKV(t, "DEL", []byte("user2"), nil, "log record 4 — tombstone")
	_, err = db1.Del([]byte("user2"))
	assert.NoError(t, err)

	t.Log("closing db1 — log file persists on disk")
	assert.NoError(t, db1.Close())

	t.Log("--- phase 2: db2 opens same log and replays ---")
	db2 := &KV{log: Log{FileName: path}}
	t.Log("calling Open() — fresh mem={}, replay all 4 records in order")
	assert.NoError(t, db2.Open())
	defer db2.Close()

	logKV(t, "GET", []byte("user1"), nil, "expect Morgan Kim (record 3 wins)")
	val, ok, err := db2.Get([]byte("user1"))
	t.Logf("[GET output] ok=%v err=%v val=%q", ok, err, val)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	logKV(t, "GET", []byte("user2"), nil, "expect miss (tombstone in record 4)")
	_, ok, err = db2.Get([]byte("user2"))
	t.Logf("[GET output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0103 KV recovery test end ===")
}

func TestEmptyLogOpen(t *testing.T) {
	t.Log("=== 0103 empty log test start ===")

	path := filepath.Join(t.TempDir(), "empty.log")
	t.Logf("[SETUP] new log path=%q (file does not exist yet)", path)

	db := &KV{log: Log{FileName: path}}
	t.Log("calling Open() — create empty log, replay hits EOF immediately")
	assert.NoError(t, db.Open())
	defer db.Close()

	logKV(t, "GET", []byte("missing"), nil, "expect ok=false on empty database")
	_, ok, err := db.Get([]byte("missing"))
	t.Logf("[GET output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0103 empty log test end ===")
}
