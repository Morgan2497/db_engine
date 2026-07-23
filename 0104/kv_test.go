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
	t.Log("=== 0104 KV lifecycle test start (log Write + Sync/fsync for durability) ===")

	path := filepath.Join(t.TempDir(), "basic.log")
	t.Logf("[SETUP] log file path=%q", path)

	db := &KV{log: Log{FileName: path}}
	t.Log("calling Open() — createFileSync (dir fsync on Unix), replay into mem")
	assert.NoError(t, db.Open())
	defer func() {
		t.Log("calling Close()")
		db.Close()
	}()

	key := []byte("morgankim")
	val := []byte("developer")

	logKV(t, "SET input", key, val, "log.Write encodes then fp.Sync() before mem update")
	updated, err := db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	logKV(t, "SET input", key, val, "expect updated=false (no Sync — no state change)")
	updated, err = db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	logKV(t, "GET input", key, nil, "read from mem only")
	got, ok, err := db.Get(key)
	t.Logf("[GET output] ok=%v err=%v val=%q", ok, err, got)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, val, got)

	logKV(t, "DEL input", key, nil, "tombstone Write+Sync then delete from mem")
	deleted, err := db.Del(key)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.True(t, deleted)

	logKV(t, "GET input", key, nil, "expect ok=false")
	_, ok, err = db.Get(key)
	t.Logf("[GET output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	missing := []byte("missing")
	logKV(t, "DEL input", missing, nil, "expect deleted=false")
	deleted, err = db.Del(missing)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.False(t, deleted)

	t.Log("=== 0104 KV lifecycle test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0104 Entry serialization test start (same 9-byte wire as 0103) ===")

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

	t.Log("=== 0104 Entry serialization test end ===")
}

func TestEntryTombstone(t *testing.T) {
	t.Log("=== 0104 Entry tombstone test start ===")

	ent := Entry{key: []byte("k1"), deleted: true}
	logEntry(t, "ENCODE input", ent, "tombstone record for Del()")

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)

	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v deleted=%v", err, decoded.deleted)
	assert.NoError(t, err)
	assert.Equal(t, ent.key, decoded.key)
	assert.True(t, decoded.deleted)

	t.Log("=== 0104 Entry tombstone test end ===")
}

func TestKVRecovery(t *testing.T) {
	t.Log("=== 0104 KV recovery test start (fsync'd records survive reopen) ===")

	path := filepath.Join(t.TempDir(), "test.log")
	t.Logf("[SETUP] shared log path=%q", path)

	t.Log("--- phase 1: db1 — each Set/Del does Write+Sync ---")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	logKV(t, "SET", []byte("user1"), []byte("Morgan"), "record 1 → disk via Sync")
	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user2"), []byte("Alice"), "record 2")
	_, err = db1.Set([]byte("user2"), []byte("Alice"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user1"), []byte("Morgan Kim"), "record 3 overrides user1")
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)

	logKV(t, "DEL", []byte("user2"), nil, "record 4 tombstone")
	_, err = db1.Del([]byte("user2"))
	assert.NoError(t, err)

	assert.NoError(t, db1.Close())

	t.Log("--- phase 2: db2 replays fsync'd log ---")
	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	t.Logf("[GET user1] ok=%v val=%q err=%v", ok, val, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	_, ok, err = db2.Get([]byte("user2"))
	t.Logf("[GET user2] ok=%v err=%v (expect false — tombstoned)", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0104 KV recovery test end ===")
}

func TestEmptyLogOpen(t *testing.T) {
	t.Log("=== 0104 empty log test start ===")

	path := filepath.Join(t.TempDir(), "empty.log")
	db := &KV{log: Log{FileName: path}}
	t.Log("Open() on missing file — createFileSync creates it, replay EOF")
	assert.NoError(t, db.Open())
	defer db.Close()

	_, ok, err := db.Get([]byte("missing"))
	t.Logf("[GET missing] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0104 empty log test end ===")
}

func roundtripEntry(t *testing.T, ent *Entry) {
	t.Helper()
	logEntry(t, "ROUNDTRIP input", *ent, "encode → decode → compare")

	enc := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(enc), enc)

	dec := &Entry{}
	err := dec.Decode(bytes.NewReader(enc))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)

	logEntry(t, "ROUNDTRIP result", *dec, "must match input")
	assert.Equal(t, ent.key, dec.key)
	assert.Equal(t, ent.deleted, dec.deleted)
	if !ent.deleted {
		assert.Equal(t, ent.val, dec.val)
	}
}

func TestEntryEncodeDecodeWithDeletedFlag(t *testing.T) {
	t.Log("=== 0104 entry roundtrip test start ===")

	t.Run("set", func(t *testing.T) {
		t.Log("subtest: normal set entry")
		roundtripEntry(t, &Entry{
			key: []byte("test_key"),
			val: []byte("test_value"),
		})
	})
	t.Run("tombstone", func(t *testing.T) {
		t.Log("subtest: tombstone entry")
		roundtripEntry(t, &Entry{
			key:     []byte("test_key"),
			deleted: true,
		})
	})

	t.Log("=== 0104 entry roundtrip test end ===")
}
