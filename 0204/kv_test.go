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

func modeName(m UpdateMode) string {
	switch m {
	case ModeUpsert:
		return "ModeUpsert"
	case ModeInsert:
		return "ModeInsert"
	case ModeUpdate:
		return "ModeUpdate"
	default:
		return "Mode?"
	}
}

func TestKVBasic(t *testing.T) {
	t.Log("=== 0204 KV lifecycle test start (Set delegates to SetEx ModeUpsert) ===")

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

	logKV(t, "SET input", key, val, "Set → SetEx(..., ModeUpsert); expect updated=true")
	updated, err := db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	logKV(t, "SET input", key, val, "identical value → updated=false (idempotent upsert)")
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

	logKV(t, "DEL input", key, nil, "tombstone with CRC, then Sync")
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

	t.Log("=== 0204 KV lifecycle test end ===")
}

func TestKVUpdateMode(t *testing.T) {
	t.Log("=== 0204 SetEx UpdateMode test start ===")
	t.Log("truth table: Insert only if absent; Update only if present+changed; Upsert insert-or-overwrite")

	path := filepath.Join(t.TempDir(), "modes.log")
	t.Logf("[SETUP] log file path=%q", path)

	kv := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv.Open())
	defer func() {
		t.Log("calling Close()")
		kv.Close()
	}()

	// ① ModeUpdate on empty DB → false
	logKV(t, "SetEx input", []byte("k1"), []byte("v1"), modeName(ModeUpdate)+" on empty mem — nothing to update")
	updated, err := kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeUpdate), updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	val, ok, err := kv.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q (expect miss)", ok, val)
	assert.NoError(t, err)
	assert.False(t, ok)

	// ② ModeInsert new key → true
	logKV(t, "SetEx input", []byte("k1"), []byte("v1"), modeName(ModeInsert)+" new key — expect insert")
	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeInsert)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeInsert), updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	val, ok, err = kv.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q", ok, val)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v1"), val)

	// ③ ModeInsert same key → false (PK collision / already exists)
	logKV(t, "SetEx input", []byte("k1"), []byte("xx"), modeName(ModeInsert)+" key exists — expect no-op (SQL INSERT conflict)")
	updated, err = kv.SetEx([]byte("k1"), []byte("xx"), ModeInsert)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeInsert), updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	val, ok, err = kv.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q (still v1 — Insert refused overwrite)", ok, val)
	assert.Equal(t, []byte("v1"), val)

	// ④ ModeUpdate existing, different value → true
	logKV(t, "SetEx input", []byte("k1"), []byte("yy"), modeName(ModeUpdate)+" exists + different val — expect overwrite")
	updated, err = kv.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeUpdate), updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	val, ok, err = kv.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q", ok, val)
	assert.Equal(t, []byte("yy"), val)

	// ⑤ ModeUpdate existing, same value → false (idempotent)
	logKV(t, "SetEx input", []byte("k1"), []byte("yy"), modeName(ModeUpdate)+" exists + same val — expect updated=false")
	updated, err = kv.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeUpdate), updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	// ⑥ ModeUpsert existing, different value → true
	logKV(t, "SetEx input", []byte("k1"), []byte("zz"), modeName(ModeUpsert)+" exists — overwrite")
	updated, err = kv.SetEx([]byte("k1"), []byte("zz"), ModeUpsert)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeUpsert), updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	val, ok, err = kv.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q", ok, val)
	assert.Equal(t, []byte("zz"), val)

	// ⑦ ModeUpsert new key → true
	logKV(t, "SetEx input", []byte("k2"), []byte("tt"), modeName(ModeUpsert)+" new key — insert")
	updated, err = kv.SetEx([]byte("k2"), []byte("tt"), ModeUpsert)
	t.Logf("[SetEx output] mode=%s updated=%v err=%v", modeName(ModeUpsert), updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	val, ok, err = kv.Get([]byte("k2"))
	t.Logf("[GET k2] ok=%v val=%q", ok, val)
	assert.True(t, ok)
	assert.Equal(t, []byte("tt"), val)

	t.Log("[SUMMARY] mem should be {k1:zz, k2:tt}")
	t.Log("=== 0204 SetEx UpdateMode test end ===")
}

func TestSetExModesRecoverFromLog(t *testing.T) {
	t.Log("=== 0204 SetEx modes survive crash/replay test start ===")
	t.Log("only successful SetEx writes append to the log; refused modes must not leave ghost records")

	path := filepath.Join(t.TempDir(), "modes_recover.log")
	t.Logf("[SETUP] shared log path=%q", path)

	t.Log("--- phase 1: mix of accepted and refused SetEx calls ---")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	updated, err := db1.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	t.Logf("[1] ModeUpdate empty → updated=%v (expect false, no log append)", updated)
	assert.NoError(t, err)
	assert.False(t, updated)

	updated, err = db1.SetEx([]byte("k1"), []byte("v1"), ModeInsert)
	t.Logf("[2] ModeInsert k1=v1 → updated=%v (expect true)", updated)
	assert.NoError(t, err)
	assert.True(t, updated)

	updated, err = db1.SetEx([]byte("k1"), []byte("xx"), ModeInsert)
	t.Logf("[3] ModeInsert conflict → updated=%v (expect false, still v1 on disk)", updated)
	assert.NoError(t, err)
	assert.False(t, updated)

	updated, err = db1.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	t.Logf("[4] ModeUpdate k1=yy → updated=%v (expect true)", updated)
	assert.NoError(t, err)
	assert.True(t, updated)

	updated, err = db1.SetEx([]byte("k2"), []byte("tt"), ModeUpsert)
	t.Logf("[5] ModeUpsert k2=tt → updated=%v (expect true)", updated)
	assert.NoError(t, err)
	assert.True(t, updated)

	assert.NoError(t, db1.Close())

	t.Log("--- phase 2: reopen + replay — only accepted writes should apply ---")
	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("k1"))
	t.Logf("[GET k1] ok=%v val=%q err=%v (expect yy)", ok, val, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("yy"), val)

	val, ok, err = db2.Get([]byte("k2"))
	t.Logf("[GET k2] ok=%v val=%q err=%v (expect tt)", ok, val, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("tt"), val)

	t.Log("=== 0204 SetEx modes survive crash/replay test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0204 Entry serialization test start (13-byte header: CRC + fields) ===")

	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	logEntry(t, "ENCODE input", ent, "CRC covers bytes from offset 4 onward")

	got := ent.Encode()
	t.Logf("[ENCODE expect] wire: crc32(4) + keySize(4) + valSize(4) + deleted(1) + key + val")
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)
	t.Logf("[ENCODE check] crc32 at [0:4]=%v keySize at [4:8]=%d valSize at [8:12]=%d deleted at [12]=%d",
		got[0:4], got[4], got[8], got[12])

	var decoded Entry
	t.Log("calling Decode — recomputes CRC and compares")
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)

	logEntry(t, "DECODE result", decoded, "should match original")
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)
	assert.False(t, decoded.deleted)

	t.Log("=== 0204 Entry serialization test end ===")
}

func TestEntryTombstone(t *testing.T) {
	t.Log("=== 0204 Entry tombstone test start ===")

	ent := Entry{key: []byte("k1"), deleted: true}
	logEntry(t, "ENCODE input", ent, "valSize=0 on wire, deleted=1")

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)

	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v deleted=%v", err, decoded.deleted)
	assert.NoError(t, err)
	assert.Equal(t, ent.key, decoded.key)
	assert.True(t, decoded.deleted)

	t.Log("=== 0204 Entry tombstone test end ===")
}

func TestKVRecovery(t *testing.T) {
	t.Log("=== 0204 KV recovery test start (CRC-verified replay) ===")

	path := filepath.Join(t.TempDir(), "test.log")
	t.Logf("[SETUP] shared log path=%q", path)

	t.Log("--- phase 1: db1 writes CRC-protected records ---")
	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	logKV(t, "SET", []byte("user1"), []byte("Morgan"), "record 1")
	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user2"), []byte("Alice"), "record 2")
	_, err = db1.Set([]byte("user2"), []byte("Alice"))
	assert.NoError(t, err)

	logKV(t, "SET", []byte("user1"), []byte("Morgan Kim"), "record 3")
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)

	logKV(t, "DEL", []byte("user2"), nil, "record 4 tombstone")
	_, err = db1.Del([]byte("user2"))
	assert.NoError(t, err)

	assert.NoError(t, db1.Close())

	t.Log("--- phase 2: db2 replays — each record CRC-checked ---")
	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	t.Logf("[GET user1] ok=%v val=%q err=%v", ok, val, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	_, ok, err = db2.Get([]byte("user2"))
	t.Logf("[GET user2] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0204 KV recovery test end ===")
}

func TestEmptyLogOpen(t *testing.T) {
	t.Log("=== 0204 empty log test start ===")

	path := filepath.Join(t.TempDir(), "empty.log")
	db := &KV{log: Log{FileName: path}}
	t.Log("Open() — empty file, replay EOF on first Read")
	assert.NoError(t, db.Open())
	defer db.Close()

	_, ok, err := db.Get([]byte("missing"))
	t.Logf("[GET missing] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	t.Log("=== 0204 empty log test end ===")
}

func roundtripEntry(t *testing.T, ent *Entry) {
	t.Helper()
	logEntry(t, "ROUNDTRIP input", *ent, "CRC computed on Encode, verified on Decode")

	enc := ent.Encode()
	t.Logf("[ENCODE output] len=%d crc_prefix=%v", len(enc), enc[0:4])

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

func TestEntryCRCRoundtrip(t *testing.T) {
	t.Log("=== 0204 CRC roundtrip test start ===")

	t.Run("set", func(t *testing.T) {
		t.Log("subtest: set entry with CRC")
		roundtripEntry(t, &Entry{
			key: []byte("test_key"),
			val: []byte("test_value"),
		})
	})
	t.Run("tombstone", func(t *testing.T) {
		t.Log("subtest: tombstone with CRC")
		roundtripEntry(t, &Entry{
			key:     []byte("test_key"),
			deleted: true,
		})
	})

	t.Log("=== 0204 CRC roundtrip test end ===")
}

func TestBadChecksum(t *testing.T) {
	t.Log("=== 0204 bad checksum test start ===")

	ent := &Entry{key: []byte("k"), val: []byte("v")}
	enc := ent.Encode()
	t.Logf("[SETUP] valid encoded len=%d crc_prefix=%v", len(enc), enc[0:4])

	enc[0] ^= 0xff
	t.Logf("[CORRUPT] flipped crc byte[0]: 0x%02x → 0x%02x", enc[0]^0xff, enc[0])
	t.Log("[EXPECT] Decode returns ErrBadSum — replay would stop at this record")

	dec := &Entry{}
	err := dec.Decode(bytes.NewReader(enc))
	t.Logf("[DECODE output] err=%v", err)
	assert.ErrorIs(t, err, ErrBadSum)

	t.Log("=== 0204 bad checksum test end ===")
}

func TestTornWriteRecovery(t *testing.T) {
	t.Log("=== 0204 torn write recovery test start ===")

	path := filepath.Join(t.TempDir(), "torn.db")
	t.Logf("[SETUP] log path=%q", path)

	t.Log("--- phase 1: write one valid CRC record ---")
	kv := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv.Open())

	logKV(t, "SET", []byte("key1"), []byte("value1"), "valid entry — Write+Sync")
	_, err := kv.Set([]byte("key1"), []byte("value1"))
	assert.NoError(t, err)
	assert.NoError(t, kv.Close())

	t.Log("--- phase 2: append raw garbage (simulates torn/corrupt tip) ---")
	garbage := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	t.Logf("[CORRUPT] appending %v without valid CRC header", garbage)

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	assert.NoError(t, err)
	_, err = file.Write(garbage)
	assert.NoError(t, err)
	assert.NoError(t, file.Close())

	t.Log("--- phase 3: reopen — replay applies good prefix, ignores garbage tip ---")
	kv2 := &KV{log: Log{FileName: path}}
	t.Log("Open() replays: record 1 CRC ok, tip fails → eof=true, err=nil")
	assert.NoError(t, kv2.Open())
	defer kv2.Close()

	val, ok, err := kv2.Get([]byte("key1"))
	t.Logf("[GET key1] ok=%v val=%q err=%v (expect value1 survived)", ok, val, err)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "value1", string(val))

	t.Log("=== 0204 torn write recovery test end ===")
}
