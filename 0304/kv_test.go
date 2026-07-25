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
	t.Log("=== 0304 KV lifecycle test start ===")

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

	_, ok, err = db.Get(key)
	t.Logf("[GET after DEL] ok=%v err=%v | expect missing", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	deleted, err = db.Del([]byte("missing"))
	t.Logf("[DEL missing] deleted=%v err=%v | expect false", deleted, err)
	assert.NoError(t, err)
	assert.False(t, deleted)

	t.Log("=== 0304 KV lifecycle test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0304 Entry serialization test start ===")

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
	assert.False(t, decoded.deleted)

	t.Log("=== 0304 Entry serialization test end ===")
}

func TestEntryTombstone(t *testing.T) {
	t.Log("=== 0304 Entry tombstone test start ===")

	ent := Entry{key: []byte("k1"), deleted: true}
	logEntry(t, "ENCODE input", ent, "deleted flag set — no val on wire")

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)

	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v deleted=%v", err, decoded.deleted)
	assert.NoError(t, err)
	assert.Equal(t, ent.key, decoded.key)
	assert.True(t, decoded.deleted)

	t.Log("=== 0304 Entry tombstone test end ===")
}

func TestKVRecovery(t *testing.T) {
	t.Log("=== 0304 KV recovery test start ===")

	path := filepath.Join(t.TempDir(), "test.log")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	logKV(t, "SET", []byte("user1"), []byte("Morgan"), "record 1")
	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)
	logKV(t, "SET", []byte("user2"), []byte("Alice"), "record 2")
	_, err = db1.Set([]byte("user2"), []byte("Alice"))
	assert.NoError(t, err)
	logKV(t, "SET", []byte("user1"), []byte("Morgan Kim"), "record 3 override user1")
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)
	logKV(t, "DEL", []byte("user2"), nil, "tombstone user2")
	_, err = db1.Del([]byte("user2"))
	assert.NoError(t, err)
	assert.NoError(t, db1.Close())

	t.Log("[REOPEN] replay log into fresh KV")
	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	t.Logf("[GET user1] ok=%v val=%q | expect Morgan Kim", ok, val)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	_, ok, err = db2.Get([]byte("user2"))
	t.Logf("[GET user2] ok=%v | expect missing (deleted)", ok)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0304 KV recovery test end ===")
}

func TestEmptyLogOpen(t *testing.T) {
	t.Log("=== 0304 empty log open test start ===")

	path := filepath.Join(t.TempDir(), "empty.log")
	t.Logf("[SETUP] brand-new log at %q", path)

	db := &KV{log: Log{FileName: path}}
	assert.NoError(t, db.Open())
	defer db.Close()

	_, ok, err := db.Get([]byte("missing"))
	t.Logf("[GET missing] ok=%v err=%v | empty store → not found", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0304 empty log open test end ===")
}

func roundtripEntry(t *testing.T, ent *Entry) {
	t.Helper()
	logEntry(t, "ENCODE input", *ent, "CRC roundtrip")

	enc := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(enc), enc)

	dec := &Entry{}
	err := dec.Decode(bytes.NewReader(enc))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)
	assert.Equal(t, ent.key, dec.key)
	assert.Equal(t, ent.deleted, dec.deleted)
	if !ent.deleted {
		assert.Equal(t, ent.val, dec.val)
	}
}

func TestEntryCRCRoundtrip(t *testing.T) {
	t.Log("=== 0304 Entry CRC roundtrip test start ===")

	t.Run("set", func(t *testing.T) {
		t.Log("[CASE] normal set entry")
		roundtripEntry(t, &Entry{
			key: []byte("test_key"),
			val: []byte("test_value"),
		})
	})
	t.Run("tombstone", func(t *testing.T) {
		t.Log("[CASE] tombstone entry")
		roundtripEntry(t, &Entry{
			key:     []byte("test_key"),
			deleted: true,
		})
	})

	t.Log("=== 0304 Entry CRC roundtrip test end ===")
}

func TestBadChecksum(t *testing.T) {
	t.Log("=== 0304 bad checksum test start ===")

	ent := &Entry{key: []byte("k"), val: []byte("v")}
	enc := ent.Encode()
	t.Logf("[ENCODE] valid entry len=%d", len(enc))

	enc[0] ^= 0xff
	t.Logf("[CORRUPT] flip byte 0 → %v", enc[0])

	dec := &Entry{}
	err := dec.Decode(bytes.NewReader(enc))
	t.Logf("[DECODE] err=%v | expect ErrBadSum", err)
	assert.ErrorIs(t, err, ErrBadSum)

	t.Log("=== 0304 bad checksum test end ===")
}

func TestTornWriteRecovery(t *testing.T) {
	t.Log("=== 0304 torn write recovery test start ===")

	path := filepath.Join(t.TempDir(), "torn.db")
	kv := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv.Open())
	logKV(t, "SET", []byte("key1"), []byte("value1"), "one valid record")
	_, err := kv.Set([]byte("key1"), []byte("value1"))
	assert.NoError(t, err)
	assert.NoError(t, kv.Close())

	t.Logf("[CORRUPT] append partial garbage to log tail at %q", path)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	assert.NoError(t, err)
	_, err = file.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00})
	assert.NoError(t, err)
	assert.NoError(t, file.Close())

	t.Log("[REOPEN] replay should stop at last valid CRC entry")
	kv2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv2.Open())
	defer kv2.Close()

	val, ok, err := kv2.Get([]byte("key1"))
	t.Logf("[GET key1] ok=%v val=%q | torn tail ignored", ok, val)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "value1", string(val))

	t.Log("=== 0304 torn write recovery test end ===")
}
