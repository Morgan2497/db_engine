package kv

import (
	"bytes"
	"os"
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
	t.Log("=== 0301 KV lifecycle test start (durable KV under parser chapter) ===")

	path := filepath.Join(t.TempDir(), "basic.log")
	t.Logf("[SETUP] log file path=%q", path)

	db := &KV{log: Log{FileName: path}}
	t.Log("calling Open() — createFileSync + CRC-verified replay")
	assert.NoError(t, db.Open())
	defer func() {
		t.Log("calling Close()")
		db.Close()
	}()

	key := []byte("morgankim")
	val := []byte("developer")

	logKV(t, "SET input", key, val, "expect updated=true")
	updated, err := db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	logKV(t, "SET input", key, val, "identical value → updated=false")
	updated, err = db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	logKV(t, "GET input", key, nil, "read from mem")
	got, ok, err := db.Get(key)
	t.Logf("[GET output] ok=%v err=%v val=%q", ok, err, got)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, val, got)

	logKV(t, "DEL input", key, nil, "tombstone")
	deleted, err := db.Del(key)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.True(t, deleted)

	t.Log("=== 0301 KV lifecycle test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0301 Entry serialization test start ===")

	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	logEntry(t, "ENCODE input", ent, "13-byte CRC header")

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)

	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)

	t.Log("=== 0301 Entry serialization test end ===")
}

func TestKVRecovery(t *testing.T) {
	t.Log("=== 0301 KV recovery test start ===")

	path := filepath.Join(t.TempDir(), "test.log")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	logKV(t, "SET", []byte("user1"), []byte("Morgan"), "record 1")
	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)
	logKV(t, "SET", []byte("user1"), []byte("Morgan Kim"), "record 2 override")
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)
	assert.NoError(t, db1.Close())

	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	t.Logf("[GET user1] ok=%v val=%q", ok, val)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	t.Log("=== 0301 KV recovery test end ===")
}

func TestTornWriteRecovery(t *testing.T) {
	t.Log("=== 0301 torn write recovery test start ===")

	path := filepath.Join(t.TempDir(), "torn.db")
	kv := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv.Open())
	_, err := kv.Set([]byte("key1"), []byte("value1"))
	assert.NoError(t, err)
	assert.NoError(t, kv.Close())

	t.Logf("[CORRUPT] append garbage to log tail")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	assert.NoError(t, err)
	_, err = file.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00})
	assert.NoError(t, err)
	assert.NoError(t, file.Close())

	kv2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv2.Open())
	defer kv2.Close()

	val, ok, err := kv2.Get([]byte("key1"))
	t.Logf("[GET key1] ok=%v val=%q (expect value1 survived)", ok, val)
	assert.True(t, ok)
	assert.Equal(t, "value1", string(val))

	t.Log("=== 0301 torn write recovery test end ===")
}
